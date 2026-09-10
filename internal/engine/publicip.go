package engine

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
)

// PublicAddress is the egress address seen from the outside for one family.
type PublicAddress struct {
	AddressFamily model.AddressFamily `json:"address_family"`
	IP            string              `json:"ip,omitempty"`
	Error         string              `json:"error,omitempty"`
	CheckedAt     time.Time           `json:"checked_at"`
}

type publicIPWatcher struct {
	mu      sync.RWMutex
	current map[model.AddressFamily]PublicAddress
}

func newPublicIPWatcher() *publicIPWatcher {
	return &publicIPWatcher{current: make(map[model.AddressFamily]PublicAddress)}
}

// PublicAddresses returns the last observed egress addresses.
func (e *Engine) PublicAddresses() []PublicAddress {
	e.publicIP.mu.RLock()
	defer e.publicIP.mu.RUnlock()

	var out []PublicAddress
	for _, af := range []model.AddressFamily{model.IPv4, model.IPv6} {
		if addr, ok := e.publicIP.current[af]; ok {
			out = append(out, addr)
		}
	}
	return out
}

// RefreshPublicIP re-checks the egress address of every enabled family and
// records an event whenever it changes, which is how an ISP re-assignment or a
// failover to a backup WAN becomes visible.
func (e *Engine) RefreshPublicIP(ctx context.Context) {
	for _, af := range e.cfg.Families() {
		endpoint := e.cfg.Network.PublicIPEndpointV4
		if af == model.IPv6 {
			endpoint = e.cfg.Network.PublicIPEndpointV6
		}
		if endpoint == "" {
			continue
		}

		observed := PublicAddress{AddressFamily: af, CheckedAt: time.Now().UTC()}
		ip, err := fetchPublicIP(ctx, endpoint, af)
		if err != nil {
			observed.Error = err.Error()
		} else {
			observed.IP = ip
		}

		e.publicIP.mu.Lock()
		previous, had := e.publicIP.current[af]
		e.publicIP.current[af] = observed
		e.publicIP.mu.Unlock()

		if had && previous.IP != "" && observed.IP != "" && previous.IP != observed.IP {
			e.detector.emit(ctx, model.Event{
				Type:     model.EventPublicIPChange,
				Severity: model.SeverityWarning,
				Message:  "Public " + string(af) + " address changed from " + previous.IP + " to " + observed.IP,
				Details: map[string]string{
					"previous":       previous.IP,
					"current":        observed.IP,
					"address_family": string(af),
				},
			})
		}
	}
}

func fetchPublicIP(ctx context.Context, endpoint string, af model.AddressFamily) (string, error) {
	network := "tcp4"
	if af == model.IPv6 {
		network = "tcp6"
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, addr)
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "GlobalNetBench/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(body))
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return "", err
	}
	return addr.Unmap().String(), nil
}
