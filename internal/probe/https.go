package probe

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
)

type HTTPResult struct {
	Success    bool
	StatusCode int
	RemoteAddr string
	Bytes      int64
	Error      string

	DNS     time.Duration
	Connect time.Duration
	TLS     time.Duration
	TTFB    time.Duration
	Total   time.Duration

	HasDNS bool
	HasTLS bool
}

// HTTPTiming performs one small request and breaks the wall time down into DNS,
// TCP, TLS and time-to-first-byte. Connection reuse is disabled so every run
// measures a complete handshake rather than a warm socket.
func HTTPTiming(ctx context.Context, url string, af model.AddressFamily, timeout time.Duration, maxBytes int64) HTTPResult {
	network := TCPNetworkFor(af)

	transport := &http.Transport{
		DisableKeepAlives:   true,
		DisableCompression:  true,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: timeout,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			// Pinning the network to tcp4/tcp6 also pins resolution to A or AAAA,
			// which is what makes the two families independently measurable.
			return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, addr)
		},
	}
	defer transport.CloseIdleConnections()

	var dnsStart, connectStart, tlsStart, firstByte, start time.Time
	result := HTTPResult{}

	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				result.DNS = time.Since(dnsStart)
				result.HasDNS = true
			}
		},
		ConnectStart: func(string, string) {
			if connectStart.IsZero() {
				connectStart = time.Now()
			}
		},
		ConnectDone: func(_, addr string, err error) {
			if err == nil && result.Connect == 0 {
				result.Connect = time.Since(connectStart)
				result.RemoteAddr = addr
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				result.TLS = time.Since(tlsStart)
				result.HasTLS = true
			}
		},
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(reqCtx, trace), http.MethodGet, url, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("User-Agent", "GlobalNetBench/0.1 (+https://github.com/ffaerber/global-net-bench)")
	req.Header.Set("Accept", "*/*")

	client := &http.Client{
		Transport: transport,
		// Redirects would measure a different endpoint than the one configured.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	start = time.Now()
	resp, err := client.Do(req)
	if err != nil {
		result.Total = time.Since(start)
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()

	if !firstByte.IsZero() {
		result.TTFB = firstByte.Sub(start)
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBytes))
	result.Total = time.Since(start)
	result.Bytes = n
	result.StatusCode = resp.StatusCode
	result.Success = resp.StatusCode < 500
	return result
}
