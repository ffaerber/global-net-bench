package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrTracerouteUnsupported is returned when only an unprivileged datagram ICMP
// socket is available: those never receive the TTL-exceeded replies a
// traceroute is built from.
var ErrTracerouteUnsupported = errors.New("traceroute needs a raw ICMP socket (CAP_NET_RAW)")

// seqStride reserves a block of sequence numbers per TTL so a reply can be
// mapped back to the hop that produced it.
const seqStride = 8

type TracerouteOptions struct {
	MaxHops       int
	QueriesPerHop int
	Timeout       time.Duration
	// WaveSize is how many TTLs are probed before waiting for replies. Larger
	// waves finish faster; smaller waves send fewer packets past the target.
	WaveSize int
	Spacing  time.Duration
}

func (o TracerouteOptions) withDefaults() TracerouteOptions {
	if o.MaxHops <= 0 || o.MaxHops > 64 {
		o.MaxHops = 30
	}
	if o.QueriesPerHop <= 0 {
		o.QueriesPerHop = 2
	}
	if o.QueriesPerHop > seqStride {
		o.QueriesPerHop = seqStride
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if o.WaveSize <= 0 {
		o.WaveSize = 5
	}
	if o.Spacing <= 0 {
		o.Spacing = 20 * time.Millisecond
	}
	return o
}

type Hop struct {
	TTL   int
	Addr  netip.Addr
	RTTs  []time.Duration
	Final bool
}

// Responded reports whether anything answered at this TTL.
func (h Hop) Responded() bool { return h.Addr.IsValid() }

type TracerouteResult struct {
	Destination netip.Addr
	Hops        []Hop
	// Reached is true when the destination itself answered, meaning the path is
	// complete rather than truncated at MaxHops.
	Reached bool
}

// Fingerprint hashes the responding hops so route changes can be detected
// cheaply. Silent hops are excluded: an intermittently quiet router would
// otherwise look like a route change every time it declines to answer.
func (r TracerouteResult) Fingerprint() string {
	var parts []string
	for _, hop := range r.Hops {
		if hop.Responded() {
			parts = append(parts, strconv.Itoa(hop.TTL)+":"+hop.Addr.String())
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:16]
}

// Path returns the ordered list of responding hop addresses.
func (r TracerouteResult) Path() []string {
	var out []string
	for _, hop := range r.Hops {
		if hop.Responded() {
			out = append(out, hop.Addr.String())
		}
	}
	return out
}

type traceReply struct {
	from netip.Addr
	at   time.Time
	kind replyKind
}

// Traceroute maps the path to a destination using ICMP echo probes with
// increasing TTL.
func Traceroute(ctx context.Context, dst netip.Addr, opts TracerouteOptions) (*TracerouteResult, error) {
	opts = opts.withDefaults()

	conn, err := openICMP(dst.Is6())
	if err != nil {
		return nil, err
	}
	defer conn.close()
	if !conn.supportsTraceroute() {
		return nil, ErrTracerouteUnsupported
	}

	id := newProbeID()
	payload := []byte("globalnetbench-traceroute")

	var mu sync.Mutex
	replies := make(map[int]traceReply)
	sendTimes := make(map[int]time.Time)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 1500)
		for {
			reply, ok, err := conn.read(buf)
			if err != nil {
				return
			}
			if !ok {
				continue
			}
			// Echo replies come straight from the destination and carry our id;
			// quoted errors carry the id we originally sent.
			if reply.id != id {
				continue
			}
			mu.Lock()
			if _, seen := replies[reply.seq]; !seen {
				replies[reply.seq] = traceReply{from: reply.from, at: time.Now(), kind: reply.kind}
			}
			mu.Unlock()
		}
	}()

	result := &TracerouteResult{Destination: dst}
	maxTTLProbed := 0

probing:
	for base := 1; base <= opts.MaxHops; base += opts.WaveSize {
		last := base + opts.WaveSize - 1
		if last > opts.MaxHops {
			last = opts.MaxHops
		}
		for ttl := base; ttl <= last; ttl++ {
			if err := conn.setTTL(ttl); err != nil {
				return nil, fmt.Errorf("set ttl %d: %w", ttl, err)
			}
			for q := 0; q < opts.QueriesPerHop; q++ {
				seq := ttl*seqStride + q
				mu.Lock()
				sendTimes[seq] = time.Now()
				mu.Unlock()
				if err := conn.send(dst, id, seq, payload); err != nil {
					mu.Lock()
					delete(sendTimes, seq)
					mu.Unlock()
					continue
				}
				if !sleepCtx(ctx, opts.Spacing) {
					break probing
				}
			}
			maxTTLProbed = ttl
		}

		if !sleepCtx(ctx, opts.Timeout) {
			break probing
		}

		// Stop as soon as the destination has answered somewhere in this wave;
		// probing further would only send more packets at the target.
		mu.Lock()
		reached := false
		for seq, reply := range replies {
			if reply.kind == replyEcho || reply.from == dst {
				if seq/seqStride <= last {
					reached = true
					break
				}
			}
		}
		mu.Unlock()
		if reached {
			break
		}
	}

	conn.close()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	for ttl := 1; ttl <= maxTTLProbed; ttl++ {
		hop := Hop{TTL: ttl}
		for q := 0; q < opts.QueriesPerHop; q++ {
			seq := ttl*seqStride + q
			reply, ok := replies[seq]
			if !ok {
				continue
			}
			if sent, ok := sendTimes[seq]; ok {
				hop.RTTs = append(hop.RTTs, reply.at.Sub(sent))
			}
			if !hop.Addr.IsValid() {
				hop.Addr = reply.from
			}
			if reply.kind == replyEcho || reply.from == dst {
				hop.Final = true
			}
		}
		result.Hops = append(result.Hops, hop)
		if hop.Final {
			result.Reached = true
			break
		}
	}

	// Trailing silent hops carry no information about the path.
	for len(result.Hops) > 0 && !result.Hops[len(result.Hops)-1].Responded() {
		result.Hops = result.Hops[:len(result.Hops)-1]
	}
	return result, nil
}

// AvgRTT returns the mean RTT observed at a hop.
func (h Hop) AvgRTT() *float64 {
	if len(h.RTTs) == 0 {
		return nil
	}
	var sum float64
	for _, rtt := range h.RTTs {
		sum += msFloat(rtt)
	}
	avg := sum / float64(len(h.RTTs))
	return &avg
}

// SortHops keeps hops in TTL order after any concurrent assembly.
func SortHops(hops []Hop) {
	sort.Slice(hops, func(i, j int) bool { return hops[i].TTL < hops[j].TTL })
}
