package probe

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// newProbeID picks a per-run ICMP identifier. A raw socket receives every ICMP
// reply on the host, so concurrent probes must not share an id or they would
// steal each other's replies.
func newProbeID() int {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 1
	}
	return int(binary.BigEndian.Uint16(b[:]))
}

const (
	protoICMPv4 = 1
	protoICMPv6 = 58
)

// icmpConn hides whether we got a raw socket (needs CAP_NET_RAW) or an
// unprivileged datagram "ping" socket. Raw is preferred because only it
// receives the TTL-exceeded replies that traceroute depends on.
type icmpConn struct {
	pc        *icmp.PacketConn
	v6        bool
	datagram  bool
	closeOnce sync.Once
}

// ErrICMPUnavailable is returned when neither socket type can be opened.
type ErrICMPUnavailable struct{ Err error }

func (e *ErrICMPUnavailable) Error() string {
	return "icmp sockets unavailable (needs CAP_NET_RAW or net.ipv4.ping_group_range): " + e.Err.Error()
}

func (e *ErrICMPUnavailable) Unwrap() error { return e.Err }

func openICMP(v6 bool) (*icmpConn, error) {
	raw, dgram, listenAddr := "ip4:icmp", "udp4", "0.0.0.0"
	if v6 {
		raw, dgram, listenAddr = "ip6:ipv6-icmp", "udp6", "::"
	}
	if pc, err := icmp.ListenPacket(raw, listenAddr); err == nil {
		return &icmpConn{pc: pc, v6: v6}, nil
	}
	pc, err := icmp.ListenPacket(dgram, listenAddr)
	if err != nil {
		return nil, &ErrICMPUnavailable{Err: err}
	}
	return &icmpConn{pc: pc, v6: v6, datagram: true}, nil
}

// ProbeICMPSupport reports whether ICMP probing is possible at all, and whether
// the socket is raw (privileged), which is what traceroute additionally needs.
func ProbeICMPSupport(v6 bool) (privileged bool, err error) {
	conn, err := openICMP(v6)
	if err != nil {
		return false, err
	}
	defer conn.close()
	return !conn.datagram, nil
}

// supportsTraceroute reports whether TTL-exceeded replies will reach us.
// Datagram ping sockets deliver those to the error queue instead, which the
// portable API cannot read.
func (c *icmpConn) supportsTraceroute() bool { return !c.datagram }

func (c *icmpConn) close() {
	c.closeOnce.Do(func() { c.pc.Close() })
}

func (c *icmpConn) protocol() int {
	if c.v6 {
		return protoICMPv6
	}
	return protoICMPv4
}

func (c *icmpConn) echoType() icmp.Type {
	if c.v6 {
		return ipv6.ICMPTypeEchoRequest
	}
	return ipv4.ICMPTypeEcho
}

func (c *icmpConn) dstAddr(ip netip.Addr) net.Addr {
	if c.datagram {
		return &net.UDPAddr{IP: ip.AsSlice(), Zone: ip.Zone()}
	}
	return &net.IPAddr{IP: ip.AsSlice(), Zone: ip.Zone()}
}

func (c *icmpConn) setTTL(ttl int) error {
	if c.v6 {
		return c.pc.IPv6PacketConn().SetHopLimit(ttl)
	}
	return c.pc.IPv4PacketConn().SetTTL(ttl)
}

// send writes an echo request. The datagram socket rewrites the ICMP id, so
// replies must be matched on sequence number rather than id.
func (c *icmpConn) send(ip netip.Addr, id, seq int, payload []byte) error {
	msg := icmp.Message{
		Type: c.echoType(),
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: payload},
	}
	encoded, err := msg.Marshal(nil)
	if err != nil {
		return err
	}
	if _, err := c.pc.WriteTo(encoded, c.dstAddr(ip)); err != nil {
		return fmt.Errorf("send icmp: %w", err)
	}
	return nil
}

// icmpReply is a decoded inbound ICMP message relevant to our probes.
type icmpReply struct {
	from    netip.Addr
	kind    replyKind
	id, seq int
}

type replyKind int

const (
	replyEcho replyKind = iota
	replyTimeExceeded
	replyUnreachable
)

// read decodes one inbound packet, returning ok=false for anything that is not
// one of our own probe's replies.
func (c *icmpConn) read(buf []byte) (icmpReply, bool, error) {
	n, peer, err := c.pc.ReadFrom(buf)
	if err != nil {
		return icmpReply{}, false, err
	}
	from, ok := addrFromNet(peer)
	if !ok {
		return icmpReply{}, false, nil
	}

	data := buf[:n]
	// A raw IPv4 socket may hand back the full datagram including its header.
	if !c.v6 && !c.datagram && len(data) >= 20 && data[0]>>4 == 4 {
		headerLen := int(data[0]&0x0f) * 4
		if headerLen <= len(data) {
			data = data[headerLen:]
		}
	}

	msg, err := icmp.ParseMessage(c.protocol(), data)
	if err != nil {
		return icmpReply{}, false, nil
	}

	switch body := msg.Body.(type) {
	case *icmp.Echo:
		if msg.Type == ipv4.ICMPTypeEchoReply || msg.Type == ipv6.ICMPTypeEchoReply {
			return icmpReply{from: from, kind: replyEcho, id: body.ID, seq: body.Seq}, true, nil
		}
	case *icmp.TimeExceeded:
		if id, seq, ok := echoFromQuotedPacket(body.Data, c.v6); ok {
			return icmpReply{from: from, kind: replyTimeExceeded, id: id, seq: seq}, true, nil
		}
	case *icmp.DstUnreach:
		if id, seq, ok := echoFromQuotedPacket(body.Data, c.v6); ok {
			return icmpReply{from: from, kind: replyUnreachable, id: id, seq: seq}, true, nil
		}
	}
	return icmpReply{}, false, nil
}

// echoFromQuotedPacket digs the original echo id and sequence out of the
// original-datagram excerpt that error messages carry.
func echoFromQuotedPacket(quoted []byte, v6 bool) (id, seq int, ok bool) {
	headerLen := 40
	if !v6 {
		if len(quoted) < 20 {
			return 0, 0, false
		}
		headerLen = int(quoted[0]&0x0f) * 4
	}
	if len(quoted) < headerLen+8 {
		return 0, 0, false
	}
	inner := quoted[headerLen:]
	// inner[0]=type, [1]=code, [2:4]=checksum, [4:6]=id, [6:8]=seq
	return int(inner[4])<<8 | int(inner[5]), int(inner[6])<<8 | int(inner[7]), true
}

func addrFromNet(addr net.Addr) (netip.Addr, bool) {
	switch a := addr.(type) {
	case *net.IPAddr:
		return normalizeIP(a.IP)
	case *net.UDPAddr:
		return normalizeIP(a.IP)
	}
	return netip.Addr{}, false
}

func normalizeIP(ip net.IP) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
