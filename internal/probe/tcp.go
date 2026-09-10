package probe

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"time"
)

type TCPResult struct {
	Success   bool          `json:"success"`
	Duration  time.Duration `json:"-"`
	TimedOut  bool          `json:"timed_out"`
	Error     string        `json:"error,omitempty"`
	LocalAddr string        `json:"local_addr,omitempty"`
}

// TCPConnect measures how long the three-way handshake takes. It is the most
// useful reachability signal when ICMP is filtered.
func TCPConnect(ctx context.Context, ip netip.Addr, port int, timeout time.Duration) TCPResult {
	network := "tcp4"
	if ip.Is6() {
		network = "tcp6"
	}
	address := net.JoinHostPort(ip.String(), strconv.Itoa(port))

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := net.Dialer{}
	start := time.Now()
	conn, err := dialer.DialContext(dialCtx, network, address)
	elapsed := time.Since(start)

	if err != nil {
		result := TCPResult{Duration: elapsed, Error: err.Error()}
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			result.TimedOut = true
		} else if dialCtx.Err() == context.DeadlineExceeded {
			result.TimedOut = true
		}
		return result
	}
	local := conn.LocalAddr().String()
	conn.Close()
	return TCPResult{Success: true, Duration: elapsed, LocalAddr: local}
}
