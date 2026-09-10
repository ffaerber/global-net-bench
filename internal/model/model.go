// Package model defines the core domain types shared by the test engine,
// storage layer and API.
package model

import "time"

type AddressFamily string

const (
	IPv4 AddressFamily = "ipv4"
	IPv6 AddressFamily = "ipv6"
)

type Protocol string

const (
	ProtoICMP  Protocol = "icmp"
	ProtoTCP   Protocol = "tcp"
	ProtoHTTPS Protocol = "https"
	ProtoDNS   Protocol = "dns"
)

// Capability names a test type a target is able to serve.
const (
	CapICMP       = "icmp"
	CapTCP        = "tcp"
	CapHTTPS      = "https"
	CapTraceroute = "traceroute"
)

// Target is a single endpoint inside a region. A region should carry more than
// one so a single broken host does not condemn the whole region.
type Target struct {
	ID           string   `json:"id"`
	Region       string   `json:"region"`
	Hostname     string   `json:"hostname"`
	IPv4         bool     `json:"ipv4"`
	IPv6         bool     `json:"ipv6"`
	Capabilities []string `json:"capabilities"`
	Ports        []int    `json:"ports,omitempty"`
	HTTPSURL     string   `json:"https_url,omitempty"`
}

func (t Target) Supports(capability string) bool {
	for _, c := range t.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Region groups targets and carries the weighting and physical expectation used
// when scoring.
type Region struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name"`
	Weight        float64  `json:"weight"`
	Local         bool     `json:"local"`
	ExpectedRTTMS float64  `json:"expected_rtt_ms,omitempty"`
	Targets       []Target `json:"targets"`
}

// Measurement is one probe result. Protocol-specific fields are nil when the
// probe that produced the row does not measure them.
type Measurement struct {
	ID            int64         `json:"id,omitempty"`
	Timestamp     time.Time     `json:"timestamp"`
	Site          string        `json:"site"`
	Region        string        `json:"region"`
	Target        string        `json:"target"`
	Protocol      Protocol      `json:"protocol"`
	AddressFamily AddressFamily `json:"address_family"`
	Success       bool          `json:"success"`
	LatencyMS     *float64      `json:"latency_ms,omitempty"`

	MinRTTMS    *float64 `json:"min_rtt_ms,omitempty"`
	AvgRTTMS    *float64 `json:"avg_rtt_ms,omitempty"`
	MedianRTTMS *float64 `json:"median_rtt_ms,omitempty"`
	P95RTTMS    *float64 `json:"p95_rtt_ms,omitempty"`
	P99RTTMS    *float64 `json:"p99_rtt_ms,omitempty"`
	JitterMS    *float64 `json:"jitter_ms,omitempty"`
	LossRatio   *float64 `json:"loss_ratio,omitempty"`

	DNSMS     *float64 `json:"dns_ms,omitempty"`
	ConnectMS *float64 `json:"connect_ms,omitempty"`
	TLSMS     *float64 `json:"tls_ms,omitempty"`
	TTFBMS    *float64 `json:"ttfb_ms,omitempty"`

	HTTPStatus    *int   `json:"http_status,omitempty"`
	ResponseBytes *int64 `json:"response_bytes,omitempty"`

	ResolvedIP string `json:"resolved_ip,omitempty"`
	Port       *int   `json:"port,omitempty"`
	Resolver   string `json:"resolver,omitempty"`
	Error      string `json:"error,omitempty"`
}

type RouteHop struct {
	Hop      int      `json:"hop"`
	IP       string   `json:"ip"`
	Hostname string   `json:"hostname,omitempty"`
	RTTMS    *float64 `json:"rtt_ms,omitempty"`
	LossPct  *float64 `json:"loss_pct,omitempty"`
}

type Route struct {
	ID            int64         `json:"id,omitempty"`
	Timestamp     time.Time     `json:"timestamp"`
	Site          string        `json:"site"`
	Region        string        `json:"region"`
	Target        string        `json:"target"`
	AddressFamily AddressFamily `json:"address_family"`
	Fingerprint   string        `json:"fingerprint"`
	HopCount      int           `json:"hop_count"`
	Complete      bool          `json:"complete"`
	Hops          []RouteHop    `json:"hops"`
}

// Event types emitted by the detector.
const (
	EventConnectivityLost = "connectivity_lost"
	EventRegionalOutage   = "regional_outage"
	EventTargetRecovered  = "target_recovered"
	EventRegionRecovered  = "region_recovered"
	EventPacketLossSpike  = "packet_loss_spike"
	EventLatencySpike     = "latency_spike"
	EventJitterSpike      = "jitter_spike"
	EventRouteChange      = "route_change"
	EventDNSFailure       = "dns_failure"
	EventIPv6Failure      = "ipv6_failure"
	EventPublicIPChange   = "public_ip_change"
)

const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

type Event struct {
	ID        int64             `json:"id,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Site      string            `json:"site"`
	Type      string            `json:"type"`
	Severity  string            `json:"severity"`
	Region    string            `json:"region,omitempty"`
	Target    string            `json:"target,omitempty"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
}

// Status classifications derived from a score.
const (
	StatusExcellent = "excellent"
	StatusGood      = "good"
	StatusFair      = "fair"
	StatusDegraded  = "degraded"
	StatusDown      = "down"
	StatusUnknown   = "unknown"
)

// StatusForScore maps a 0-100 score onto a coarse label.
func StatusForScore(score float64) string {
	switch {
	case score >= 95:
		return StatusExcellent
	case score >= 85:
		return StatusGood
	case score >= 70:
		return StatusFair
	case score > 0:
		return StatusDegraded
	default:
		return StatusDown
	}
}

// Baseline is a rolling summary of a target's normal behaviour over a window.
type Baseline struct {
	Window      string   `json:"window"`
	Samples     int      `json:"samples"`
	MedianRTTMS float64  `json:"median_rtt_ms"`
	P95RTTMS    float64  `json:"p95_rtt_ms"`
	LowRTTMS    float64  `json:"low_rtt_ms"`
	HighRTTMS   float64  `json:"high_rtt_ms"`
	LossRatio   float64  `json:"loss_ratio"`
	JitterMS    *float64 `json:"jitter_ms,omitempty"`
}

func Float(v float64) *float64 { return &v }
func Int(v int) *int           { return &v }
func Int64(v int64) *int64     { return &v }
