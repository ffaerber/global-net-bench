package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, "sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func TestMeasurementRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	original := model.Measurement{
		Timestamp: now, Site: "s1", Region: "eu", Target: "fra-01",
		Protocol: model.ProtoICMP, AddressFamily: model.IPv4, Success: true,
		LatencyMS: model.Float(48.5), JitterMS: model.Float(2.1),
		LossRatio: model.Float(0), P95RTTMS: model.Float(51.2),
		ResolvedIP: "203.0.113.9",
	}
	if err := st.InsertMeasurements(ctx, []model.Measurement{original}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := st.Measurements(ctx, MeasurementFilter{Site: "s1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d measurements, want 1", len(got))
	}

	m := got[0]
	if !m.Timestamp.Equal(now) {
		t.Errorf("timestamp = %v, want %v", m.Timestamp, now)
	}
	if m.Protocol != model.ProtoICMP || m.AddressFamily != model.IPv4 {
		t.Errorf("protocol/family = %v/%v", m.Protocol, m.AddressFamily)
	}
	if m.LatencyMS == nil || *m.LatencyMS != 48.5 {
		t.Errorf("latency = %v, want 48.5", m.LatencyMS)
	}
	if m.ResolvedIP != "203.0.113.9" {
		t.Errorf("resolved ip = %q", m.ResolvedIP)
	}
	// Fields the probe never set must come back nil rather than zero.
	if m.TLSMS != nil {
		t.Errorf("tls_ms = %v, want nil", *m.TLSMS)
	}
}

func TestMeasurementFilters(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	now := time.Now().UTC()
	measurements := []model.Measurement{
		{Timestamp: now, Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoICMP, AddressFamily: model.IPv4, Success: true},
		{Timestamp: now, Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoTCP, AddressFamily: model.IPv4, Success: true},
		{Timestamp: now, Site: "s1", Region: "asia", Target: "b", Protocol: model.ProtoICMP, AddressFamily: model.IPv6, Success: false},
		{Timestamp: now.Add(-48 * time.Hour), Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoICMP, AddressFamily: model.IPv4, Success: true},
	}
	if err := st.InsertMeasurements(ctx, measurements); err != nil {
		t.Fatalf("insert: %v", err)
	}

	cases := []struct {
		name   string
		filter MeasurementFilter
		want   int
	}{
		{"all", MeasurementFilter{Site: "s1"}, 4},
		{"by region", MeasurementFilter{Site: "s1", Region: "eu"}, 3},
		{"by protocol", MeasurementFilter{Site: "s1", Protocol: model.ProtoICMP}, 3},
		{"by family", MeasurementFilter{Site: "s1", AddressFamily: model.IPv6}, 1},
		{"by target", MeasurementFilter{Site: "s1", Target: "b"}, 1},
		{"recent only", MeasurementFilter{Site: "s1", Since: now.Add(-time.Hour)}, 3},
		{"other site", MeasurementFilter{Site: "s2"}, 0},
		{"limited", MeasurementFilter{Site: "s1", Limit: 2}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.Measurements(ctx, tc.filter)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("got %d, want %d", len(got), tc.want)
			}
		})
	}
}

func TestMeasurementsAreReturnedNewestFirst(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	now := time.Now().UTC()
	if err := st.InsertMeasurements(ctx, []model.Measurement{
		{Timestamp: now.Add(-2 * time.Minute), Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoICMP, AddressFamily: model.IPv4, LatencyMS: model.Float(1)},
		{Timestamp: now, Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoICMP, AddressFamily: model.IPv4, LatencyMS: model.Float(3)},
		{Timestamp: now.Add(-time.Minute), Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoICMP, AddressFamily: model.IPv4, LatencyMS: model.Float(2)},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := st.Measurements(ctx, MeasurementFilter{Site: "s1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.After(got[i-1].Timestamp) {
			t.Fatalf("results are not ordered newest first: %v then %v", got[i-1].Timestamp, got[i].Timestamp)
		}
	}
}

func TestRouteRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	route := model.Route{
		Timestamp: time.Now().UTC(), Site: "s1", Region: "asia", Target: "tokyo-01",
		AddressFamily: model.IPv4, Fingerprint: "abc123", HopCount: 2, Complete: true,
		Hops: []model.RouteHop{
			{Hop: 1, IP: "192.168.1.1", RTTMS: model.Float(0.8)},
			{Hop: 2, IP: "203.0.113.1"},
		},
	}
	if err := st.InsertRoute(ctx, &route); err != nil {
		t.Fatalf("insert route: %v", err)
	}

	got, err := st.Routes(ctx, RouteFilter{Target: "tokyo-01"})
	if err != nil {
		t.Fatalf("query routes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d routes, want 1", len(got))
	}
	if got[0].Fingerprint != "abc123" || !got[0].Complete {
		t.Errorf("route = %+v", got[0])
	}
	if len(got[0].Hops) != 2 || got[0].Hops[0].IP != "192.168.1.1" {
		t.Errorf("hops = %+v", got[0].Hops)
	}
	if got[0].Hops[1].RTTMS != nil {
		t.Error("a hop with no RTT should decode as nil")
	}
}

func TestEventRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	event := model.Event{
		Timestamp: time.Now().UTC(), Site: "s1", Type: model.EventRouteChange,
		Severity: model.SeverityWarning, Region: "asia", Target: "tokyo-01",
		Message: "Route changed", Details: map[string]string{"hop_count": "14"},
	}
	if err := st.InsertEvent(ctx, &event); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	got, err := st.Events(ctx, EventFilter{Type: model.EventRouteChange})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].Details["hop_count"] != "14" {
		t.Errorf("details = %+v", got[0].Details)
	}

	none, err := st.Events(ctx, EventFilter{Type: model.EventDNSFailure})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("got %d events for an unmatched type, want 0", len(none))
	}
}

func TestBaselinesNeedEnoughSamples(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	now := time.Now().UTC()
	var measurements []model.Measurement
	for i := 0; i < 10; i++ {
		measurements = append(measurements, model.Measurement{
			Timestamp: now.Add(-time.Duration(i) * time.Minute),
			Site:      "s1", Region: "eu", Target: "fra-01",
			Protocol: model.ProtoICMP, AddressFamily: model.IPv4, Success: true,
			LatencyMS: model.Float(float64(40 + i)), LossRatio: model.Float(0),
		})
	}
	if err := st.InsertMeasurements(ctx, measurements); err != nil {
		t.Fatalf("insert: %v", err)
	}

	key := BaselineKey{Target: "fra-01", Protocol: model.ProtoICMP, AddressFamily: model.IPv4}

	sparse, err := st.Baselines(ctx, "s1", time.Hour, 20)
	if err != nil {
		t.Fatalf("baselines: %v", err)
	}
	if _, ok := sparse[key]; ok {
		t.Error("a baseline was learned from fewer samples than the minimum")
	}

	learned, err := st.Baselines(ctx, "s1", time.Hour, 5)
	if err != nil {
		t.Fatalf("baselines: %v", err)
	}
	baseline, ok := learned[key]
	if !ok {
		t.Fatal("no baseline learned")
	}
	if baseline.Samples != 10 {
		t.Errorf("samples = %d, want 10", baseline.Samples)
	}
	if baseline.MedianRTTMS < 44 || baseline.MedianRTTMS > 45 {
		t.Errorf("median = %v, want ~44.5", baseline.MedianRTTMS)
	}
}

func TestBaselinesIgnoreFailures(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	now := time.Now().UTC()
	var measurements []model.Measurement
	for i := 0; i < 6; i++ {
		measurements = append(measurements, model.Measurement{
			Timestamp: now, Site: "s1", Region: "eu", Target: "fra-01",
			Protocol: model.ProtoICMP, AddressFamily: model.IPv4, Success: true,
			LatencyMS: model.Float(50),
		})
	}
	// A failed probe records a huge timeout value that must not skew normality.
	measurements = append(measurements, model.Measurement{
		Timestamp: now, Site: "s1", Region: "eu", Target: "fra-01",
		Protocol: model.ProtoICMP, AddressFamily: model.IPv4, Success: false,
		LatencyMS: model.Float(5000),
	})
	if err := st.InsertMeasurements(ctx, measurements); err != nil {
		t.Fatalf("insert: %v", err)
	}

	learned, err := st.Baselines(ctx, "s1", time.Hour, 5)
	if err != nil {
		t.Fatalf("baselines: %v", err)
	}
	baseline := learned[BaselineKey{Target: "fra-01", Protocol: model.ProtoICMP, AddressFamily: model.IPv4}]
	if baseline.MedianRTTMS != 50 {
		t.Errorf("median = %v, want 50: failed probes must be excluded", baseline.MedianRTTMS)
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	now := time.Now().UTC()
	if err := st.InsertMeasurements(ctx, []model.Measurement{
		{Timestamp: now, Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoICMP, AddressFamily: model.IPv4},
		{Timestamp: now.Add(-72 * time.Hour), Site: "s1", Region: "eu", Target: "a", Protocol: model.ProtoICMP, AddressFamily: model.IPv4},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	event := model.Event{Timestamp: now.Add(-72 * time.Hour), Site: "s1", Type: model.EventLatencySpike, Severity: model.SeverityInfo, Message: "old"}
	if err := st.InsertEvent(ctx, &event); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	if err := st.Prune(ctx, now.Add(-24*time.Hour), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	remaining, err := st.Measurements(ctx, MeasurementFilter{Site: "s1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("got %d measurements after prune, want 1", len(remaining))
	}

	// Events are the record of what happened and are deliberately never pruned.
	events, err := st.Events(ctx, EventFilter{Site: "s1"})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events after prune, want 1: events must be kept", len(events))
	}
}

func TestInsertNoMeasurementsIsNotAnError(t *testing.T) {
	if err := newTestStore(t).InsertMeasurements(context.Background(), nil); err != nil {
		t.Errorf("inserting nothing returned %v", err)
	}
}
