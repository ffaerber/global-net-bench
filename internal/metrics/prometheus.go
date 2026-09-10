// Package metrics renders the Prometheus text exposition format directly, which
// keeps the binary free of a client-library dependency for a handful of gauges.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ffaerber/global-net-bench/internal/engine"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/score"
)

const prefix = "globalnetbench_"

type writer struct {
	out io.Writer
	err error
}

func (w *writer) header(name, help, kind string) {
	w.printf("# HELP %s%s %s\n", prefix, name, help)
	w.printf("# TYPE %s%s %s\n", prefix, name, kind)
}

func (w *writer) sample(name string, labels []label, value float64) {
	w.printf("%s%s%s %s\n", prefix, name, formatLabels(labels), strconv.FormatFloat(value, 'g', -1, 64))
}

func (w *writer) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.out, format, args...)
}

type label struct {
	name  string
	value string
}

// Render writes every metric derived from the current snapshot and the latest
// measurement of each series.
func Render(out io.Writer, snapshot score.Snapshot, latest []model.Measurement, routeChanges map[engine.RouteChangeKey]int) error {
	w := &writer{out: out}

	w.header("global_score", "Weighted aggregate connectivity score across all configured regions (0-100).", "gauge")
	w.sample("global_score", []label{{"site", snapshot.Site}}, snapshot.GlobalScore)

	if snapshot.LocalScore != nil {
		w.header("local_score", "Health of the local connection and ISP (0-100).", "gauge")
		w.sample("local_score", []label{{"site", snapshot.Site}}, *snapshot.LocalScore)
	}

	w.header("region_score", "Per-region connectivity score (0-100).", "gauge")
	for _, region := range snapshot.Regions {
		if region.HasData {
			w.sample("region_score", []label{{"site", snapshot.Site}, {"region", region.ID}}, region.Score)
		}
	}

	w.header("address_family_up", "Whether an address family reaches any target (1) or none (0).", "gauge")
	for _, fam := range []struct {
		name   string
		health score.FamilyHealth
	}{{"ipv4", snapshot.IPv4}, {"ipv6", snapshot.IPv6}} {
		if !fam.health.Enabled {
			continue
		}
		up := 0.0
		if fam.health.Success > 0 {
			up = 1
		}
		w.sample("address_family_up", []label{{"site", snapshot.Site}, {"address_family", fam.name}}, up)
	}

	// Sorting keeps the exposition stable between scrapes, which makes diffs
	// and manual inspection far easier.
	sorted := append([]model.Measurement(nil), latest...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Target != sorted[j].Target {
			return sorted[i].Target < sorted[j].Target
		}
		if sorted[i].Protocol != sorted[j].Protocol {
			return sorted[i].Protocol < sorted[j].Protocol
		}
		return sorted[i].AddressFamily < sorted[j].AddressFamily
	})

	type gauge struct {
		name  string
		help  string
		value func(model.Measurement) *float64
	}
	gauges := []gauge{
		{"latency_ms", "Most recent latency in milliseconds.", func(m model.Measurement) *float64 { return m.LatencyMS }},
		{"jitter_ms", "Mean absolute difference between consecutive round trips.", func(m model.Measurement) *float64 { return m.JitterMS }},
		{"packet_loss_ratio", "Fraction of probe packets lost (0-1).", func(m model.Measurement) *float64 { return m.LossRatio }},
		{"tcp_connect_ms", "TCP handshake duration in milliseconds.", func(m model.Measurement) *float64 { return m.ConnectMS }},
		{"tls_handshake_ms", "TLS handshake duration in milliseconds.", func(m model.Measurement) *float64 { return m.TLSMS }},
		{"http_ttfb_ms", "HTTP time to first byte in milliseconds.", func(m model.Measurement) *float64 { return m.TTFBMS }},
		{"dns_lookup_ms", "DNS resolution time in milliseconds.", func(m model.Measurement) *float64 { return m.DNSMS }},
		{"rtt_p95_ms", "95th percentile round trip time in milliseconds.", func(m model.Measurement) *float64 { return m.P95RTTMS }},
	}

	for _, g := range gauges {
		var emitted bool
		for _, m := range sorted {
			value := g.value(m)
			if value == nil {
				continue
			}
			if !emitted {
				w.header(g.name, g.help, "gauge")
				emitted = true
			}
			w.sample(g.name, seriesLabels(snapshot.Site, m), *value)
		}
	}

	w.header("target_up", "Whether the most recent probe of this series succeeded.", "gauge")
	for _, m := range sorted {
		up := 0.0
		if m.Success {
			up = 1
		}
		w.sample("target_up", seriesLabels(snapshot.Site, m), up)
	}

	w.header("route_changes_total", "Route changes detected since this process started.", "counter")
	keys := make([]engine.RouteChangeKey, 0, len(routeChanges))
	for k := range routeChanges {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Target < keys[j].Target })
	for _, k := range keys {
		w.sample("route_changes_total",
			[]label{{"site", snapshot.Site}, {"region", k.Region}, {"target", k.Target}},
			float64(routeChanges[k]))
	}

	return w.err
}

// seriesLabels keeps to the low-cardinality label set: per-hop IPs and other
// unbounded values deliberately never reach Prometheus.
func seriesLabels(site string, m model.Measurement) []label {
	labels := []label{
		{"site", site},
		{"region", m.Region},
		{"target", m.Target},
		{"protocol", string(m.Protocol)},
		{"address_family", string(m.AddressFamily)},
	}
	if m.Port != nil {
		labels = append(labels, label{"port", strconv.Itoa(*m.Port)})
	}
	return labels
}

func formatLabels(labels []label) string {
	if len(labels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, l := range labels {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.name)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(l.value))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabelValue(v string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(v)
}
