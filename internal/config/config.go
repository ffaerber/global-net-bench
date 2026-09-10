// Package config loads and validates the GlobalNetBench YAML configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ffaerber/global-net-bench/internal/model"
)

// Duration wraps time.Duration so it can be written as "60s" or "3h" in YAML.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

type Config struct {
	Server     Server     `yaml:"server"`
	Site       Site       `yaml:"site"`
	Monitoring Monitoring `yaml:"monitoring"`
	Network    Network    `yaml:"network"`
	Regions    []Region   `yaml:"regions"`
	Tests      Tests      `yaml:"tests"`
	Database   Database   `yaml:"database"`
	Retention  Retention  `yaml:"retention"`
	Prometheus Prometheus `yaml:"prometheus"`
	GeoIP      GeoIP      `yaml:"geoip"`
	Scoring    Scoring    `yaml:"scoring"`
	Thresholds Thresholds `yaml:"thresholds"`
}

// GeoIP points at local MaxMind-format databases used to place traceroute hops
// on the globe. Both files are optional and are looked up locally, so enabling
// this sends nothing to a third party.
type GeoIP struct {
	Enabled bool   `yaml:"enabled"`
	CityDB  string `yaml:"city_db"`
	ASNDB   string `yaml:"asn_db"`
}

// Thresholds decide when a measurement becomes an event. Latency is judged
// against the learned baseline rather than an absolute number, because a
// physically distant region is not unhealthy merely for being far away.
type Thresholds struct {
	PacketLossWarn     float64 `yaml:"packet_loss_warn"`
	PacketLossCritical float64 `yaml:"packet_loss_critical"`
	JitterWarnMS       float64 `yaml:"jitter_warn_ms"`
	LatencySpikeFactor float64 `yaml:"latency_spike_factor"`
	MinLatencyDeltaMS  float64 `yaml:"min_latency_delta_ms"`
}

type Server struct {
	Listen string `yaml:"listen"`
}

// Site identifies which machine, WAN or interface produced a measurement, so
// results from several vantage points can later be compared.
type Site struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type Monitoring struct {
	HealthInterval  Duration `yaml:"health_interval"`
	QualityInterval Duration `yaml:"quality_interval"`
	RouteInterval   Duration `yaml:"route_interval"`
	// Concurrency caps how many targets are probed at once, so monitoring stays
	// low-overhead and never looks like a burst of scanning traffic.
	Concurrency int `yaml:"concurrency"`
}

type Network struct {
	IPv4 bool `yaml:"ipv4"`
	IPv6 bool `yaml:"ipv6"`
	// PublicIPEndpoints are queried to discover the current egress address.
	PublicIPEndpointV4 string `yaml:"public_ip_endpoint_v4"`
	PublicIPEndpointV6 string `yaml:"public_ip_endpoint_v6"`
}

type Region struct {
	ID          string  `yaml:"id"`
	DisplayName string  `yaml:"display_name"`
	Weight      float64 `yaml:"weight"`
	Local       bool    `yaml:"local"`
	// Latitude and Longitude place the region on the dashboard globe. Both are
	// pointers so that "not configured" is distinguishable from a deliberate
	// 0,0 in the Gulf of Guinea.
	Latitude      *float64 `yaml:"latitude"`
	Longitude     *float64 `yaml:"longitude"`
	ExpectedRTTMS float64  `yaml:"expected_rtt_ms"`
	Targets       []Target `yaml:"targets"`
}

type Target struct {
	ID           string   `yaml:"id"`
	Hostname     string   `yaml:"hostname"`
	IPv4         *bool    `yaml:"ipv4"`
	IPv6         *bool    `yaml:"ipv6"`
	Capabilities []string `yaml:"capabilities"`
	Ports        []int    `yaml:"ports"`
	HTTPSURL     string   `yaml:"https_url"`
}

type Tests struct {
	ICMP       ICMPTest       `yaml:"icmp"`
	TCP        TCPTest        `yaml:"tcp"`
	HTTPS      HTTPSTest      `yaml:"https"`
	DNS        DNSTest        `yaml:"dns"`
	Traceroute TracerouteTest `yaml:"traceroute"`
}

type ICMPTest struct {
	Enabled bool     `yaml:"enabled"`
	Packets int      `yaml:"packets"`
	Spacing Duration `yaml:"spacing"`
	Timeout Duration `yaml:"timeout"`
	// QualityPackets is the larger burst used by the quality test, which exists
	// to characterise jitter and loss rather than plain reachability.
	QualityPackets int `yaml:"quality_packets"`
}

type TCPTest struct {
	Enabled bool     `yaml:"enabled"`
	Ports   []int    `yaml:"ports"`
	Timeout Duration `yaml:"timeout"`
}

type HTTPSTest struct {
	Enabled  bool     `yaml:"enabled"`
	Timeout  Duration `yaml:"timeout"`
	MaxBytes int64    `yaml:"max_bytes"`
}

type DNSTest struct {
	Enabled   bool       `yaml:"enabled"`
	Timeout   Duration   `yaml:"timeout"`
	QueryName string     `yaml:"query_name"`
	Resolvers []Resolver `yaml:"resolvers"`
}

type Resolver struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
}

type TracerouteTest struct {
	Enabled       bool     `yaml:"enabled"`
	MaxHops       int      `yaml:"max_hops"`
	Timeout       Duration `yaml:"timeout"`
	QueriesPerHop int      `yaml:"queries_per_hop"`
}

type Database struct {
	Type string `yaml:"type"`
	DSN  string `yaml:"dsn"`
}

type Retention struct {
	RawMeasurements Duration `yaml:"raw_measurements"`
	Routes          Duration `yaml:"routes"`
}

type Prometheus struct {
	Enabled bool `yaml:"enabled"`
}

type Scoring struct {
	// BaselineWindow is the history window used to learn what "normal" means.
	BaselineWindow Duration `yaml:"baseline_window"`
	// MinBaselineSamples guards against scoring off a handful of points.
	MinBaselineSamples int `yaml:"min_baseline_samples"`
}

// Load reads, defaults and validates a configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	setString(&c.Server.Listen, "0.0.0.0:8080")
	setString(&c.Site.ID, "default")
	setString(&c.Site.Name, "Local")

	setDuration(&c.Monitoring.HealthInterval, 60*time.Second)
	setDuration(&c.Monitoring.QualityInterval, 5*time.Minute)
	setDuration(&c.Monitoring.RouteInterval, 15*time.Minute)
	setInt(&c.Monitoring.Concurrency, 6)

	setString(&c.Network.PublicIPEndpointV4, "https://api.ipify.org")
	setString(&c.Network.PublicIPEndpointV6, "https://api6.ipify.org")

	setInt(&c.Tests.ICMP.Packets, 10)
	setInt(&c.Tests.ICMP.QualityPackets, 50)
	setDuration(&c.Tests.ICMP.Spacing, 100*time.Millisecond)
	setDuration(&c.Tests.ICMP.Timeout, 2*time.Second)

	if len(c.Tests.TCP.Ports) == 0 {
		c.Tests.TCP.Ports = []int{443}
	}
	setDuration(&c.Tests.TCP.Timeout, 5*time.Second)

	setDuration(&c.Tests.HTTPS.Timeout, 10*time.Second)
	if c.Tests.HTTPS.MaxBytes <= 0 {
		c.Tests.HTTPS.MaxBytes = 64 * 1024
	}

	setDuration(&c.Tests.DNS.Timeout, 3*time.Second)
	setString(&c.Tests.DNS.QueryName, "www.example.com.")

	setInt(&c.Tests.Traceroute.MaxHops, 30)
	setInt(&c.Tests.Traceroute.QueriesPerHop, 2)
	setDuration(&c.Tests.Traceroute.Timeout, 2*time.Second)

	setString(&c.Database.Type, "sqlite")
	if c.Database.DSN == "" {
		if c.Database.Type == "sqlite" {
			c.Database.DSN = "globalnetbench.db"
		}
	}

	setDuration(&c.Retention.RawMeasurements, 30*24*time.Hour)
	setDuration(&c.Retention.Routes, 90*24*time.Hour)

	setDuration(&c.Scoring.BaselineWindow, 7*24*time.Hour)
	setInt(&c.Scoring.MinBaselineSamples, 20)

	setFloat(&c.Thresholds.PacketLossWarn, 0.02)
	setFloat(&c.Thresholds.PacketLossCritical, 0.10)
	setFloat(&c.Thresholds.JitterWarnMS, 30)
	setFloat(&c.Thresholds.LatencySpikeFactor, 1.35)
	setFloat(&c.Thresholds.MinLatencyDeltaMS, 10)

	for i := range c.Regions {
		r := &c.Regions[i]
		if r.Weight == 0 {
			r.Weight = 1.0
		}
		if r.DisplayName == "" {
			r.DisplayName = r.ID
		}
		for j := range r.Targets {
			t := &r.Targets[j]
			if t.ID == "" {
				t.ID = fmt.Sprintf("%s-%02d", r.ID, j+1)
			}
			if len(t.Capabilities) == 0 {
				t.Capabilities = []string{model.CapICMP, model.CapTCP, model.CapHTTPS, model.CapTraceroute}
			}
			if t.IPv4 == nil {
				v := true
				t.IPv4 = &v
			}
			if t.IPv6 == nil {
				v := true
				t.IPv6 = &v
			}
		}
	}
}

func (c *Config) validate() error {
	if !c.Network.IPv4 && !c.Network.IPv6 {
		return fmt.Errorf("network: at least one of ipv4/ipv6 must be enabled")
	}
	if len(c.Regions) == 0 {
		return fmt.Errorf("regions: at least one region must be configured")
	}
	switch c.Database.Type {
	case "sqlite", "postgres":
	default:
		return fmt.Errorf("database.type: must be sqlite or postgres, got %q", c.Database.Type)
	}
	if c.Database.Type == "postgres" && c.Database.DSN == "" {
		return fmt.Errorf("database.dsn: required for postgres")
	}

	if c.GeoIP.Enabled {
		if c.GeoIP.CityDB == "" && c.GeoIP.ASNDB == "" {
			return fmt.Errorf("geoip.enabled: set geoip.city_db and/or geoip.asn_db, or disable geoip")
		}
		// Failing here beats starting up and quietly drawing an empty map.
		for _, db := range []struct{ field, path string }{
			{"geoip.city_db", c.GeoIP.CityDB},
			{"geoip.asn_db", c.GeoIP.ASNDB},
		} {
			if db.path == "" {
				continue
			}
			if _, err := os.Stat(db.path); err != nil {
				return fmt.Errorf("%s: %w", db.field, err)
			}
		}
	}

	seenRegion := map[string]bool{}
	seenTarget := map[string]bool{}
	for _, r := range c.Regions {
		if r.ID == "" {
			return fmt.Errorf("regions: every region needs an id")
		}
		if seenRegion[r.ID] {
			return fmt.Errorf("regions: duplicate region id %q", r.ID)
		}
		seenRegion[r.ID] = true
		if len(r.Targets) == 0 {
			return fmt.Errorf("region %s: at least one target is required", r.ID)
		}
		if (r.Latitude == nil) != (r.Longitude == nil) {
			return fmt.Errorf("region %s: latitude and longitude must be set together", r.ID)
		}
		if r.Latitude != nil && (*r.Latitude < -90 || *r.Latitude > 90) {
			return fmt.Errorf("region %s: latitude %g is outside -90..90", r.ID, *r.Latitude)
		}
		if r.Longitude != nil && (*r.Longitude < -180 || *r.Longitude > 180) {
			return fmt.Errorf("region %s: longitude %g is outside -180..180", r.ID, *r.Longitude)
		}
		for _, t := range r.Targets {
			if seenTarget[t.ID] {
				return fmt.Errorf("region %s: duplicate target id %q", r.ID, t.ID)
			}
			seenTarget[t.ID] = true
			if t.Hostname == "" {
				return fmt.Errorf("target %s: hostname is required", t.ID)
			}
			if t.HTTPSURL != "" {
				u, err := url.Parse(t.HTTPSURL)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
					return fmt.Errorf("target %s: https_url must be an http(s) URL", t.ID)
				}
			}
		}
	}
	for _, res := range c.Tests.DNS.Resolvers {
		if res.Address == "" {
			return fmt.Errorf("dns resolver %q: address is required", res.Name)
		}
	}
	return nil
}

// ModelRegions converts the configured regions into the shared domain types.
func (c *Config) ModelRegions() []model.Region {
	out := make([]model.Region, 0, len(c.Regions))
	for _, r := range c.Regions {
		mr := model.Region{
			ID:            r.ID,
			DisplayName:   r.DisplayName,
			Weight:        r.Weight,
			Local:         r.Local,
			Latitude:      r.Latitude,
			Longitude:     r.Longitude,
			ExpectedRTTMS: r.ExpectedRTTMS,
		}
		for _, t := range r.Targets {
			ports := t.Ports
			if len(ports) == 0 {
				ports = c.Tests.TCP.Ports
			}
			httpsURL := t.HTTPSURL
			if httpsURL == "" && t.Supports(model.CapHTTPS) {
				httpsURL = "https://" + t.Hostname + "/"
			}
			mr.Targets = append(mr.Targets, model.Target{
				ID:           t.ID,
				Region:       r.ID,
				Hostname:     t.Hostname,
				IPv4:         t.IPv4 == nil || *t.IPv4,
				IPv6:         t.IPv6 == nil || *t.IPv6,
				Capabilities: t.Capabilities,
				Ports:        ports,
				HTTPSURL:     httpsURL,
			})
		}
		out = append(out, mr)
	}
	return out
}

func (t Target) Supports(capability string) bool {
	for _, c := range t.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Families lists the address families enabled globally.
func (c *Config) Families() []model.AddressFamily {
	var out []model.AddressFamily
	if c.Network.IPv4 {
		out = append(out, model.IPv4)
	}
	if c.Network.IPv6 {
		out = append(out, model.IPv6)
	}
	return out
}

func setString(dst *string, def string) {
	if *dst == "" {
		*dst = def
	}
}

func setInt(dst *int, def int) {
	if *dst <= 0 {
		*dst = def
	}
}

func setFloat(dst *float64, def float64) {
	if *dst == 0 {
		*dst = def
	}
}

func setDuration(dst *Duration, def time.Duration) {
	if *dst == 0 {
		*dst = Duration(def)
	}
}
