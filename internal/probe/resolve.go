package probe

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
)

// NetworkFor returns the Go network suffix for an address family.
func NetworkFor(af model.AddressFamily) string {
	if af == model.IPv6 {
		return "ip6"
	}
	return "ip4"
}

// TCPNetworkFor returns the dial network that pins a connection to one family.
func TCPNetworkFor(af model.AddressFamily) string {
	if af == model.IPv6 {
		return "tcp6"
	}
	return "tcp4"
}

// Resolve looks up a host in a single address family. A literal IP is returned
// as-is when it matches the requested family, and rejected when it does not.
func Resolve(ctx context.Context, host string, af model.AddressFamily) (netip.Addr, time.Duration, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if addr.Is6() != (af == model.IPv6) {
			return netip.Addr{}, 0, fmt.Errorf("%s is not an %s address", host, af)
		}
		return addr, 0, nil
	}

	start := time.Now()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, NetworkFor(af), host)
	elapsed := time.Since(start)
	if err != nil {
		return netip.Addr{}, elapsed, err
	}
	if len(addrs) == 0 {
		return netip.Addr{}, elapsed, fmt.Errorf("no %s address for %s", af, host)
	}
	return addrs[0].Unmap(), elapsed, nil
}
