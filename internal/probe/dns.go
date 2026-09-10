package probe

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
)

type DNSResult struct {
	Success  bool
	Duration time.Duration
	Answers  int
	TimedOut bool
	ServFail bool
	NotFound bool
	Error    string
}

// DNSLookup times a single query against one resolver. The address family
// selects the record type, so A and AAAA resolution can be compared.
func DNSLookup(ctx context.Context, resolverAddr, name string, af model.AddressFamily, timeout time.Duration) DNSResult {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// Force our own resolver choice; ignore the system-supplied address.
			if strings.HasPrefix(network, "tcp") {
				network = "tcp"
			} else {
				network = "udp"
			}
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, withDefaultDNSPort(resolverAddr))
		},
	}

	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	addrs, err := resolver.LookupNetIP(lookupCtx, NetworkFor(af), name)
	result := DNSResult{Duration: time.Since(start), Answers: len(addrs)}

	if err != nil {
		result.Error = err.Error()
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			result.TimedOut = dnsErr.IsTimeout
			result.NotFound = dnsErr.IsNotFound
			// The Go resolver surfaces SERVFAIL/REFUSED as "server misbehaving".
			result.ServFail = strings.Contains(dnsErr.Err, "server misbehaving")
		}
		if lookupCtx.Err() == context.DeadlineExceeded {
			result.TimedOut = true
		}
		// A resolver that authoritatively says "no such record" still answered.
		result.Success = result.NotFound
		return result
	}

	result.Success = true
	return result
}

func withDefaultDNSPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, "53")
}
