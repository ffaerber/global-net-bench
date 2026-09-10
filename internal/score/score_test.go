package score

import (
	"testing"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/store"
)

func icmp(target string, rtt, jitter, loss float64) model.Measurement {
	return model.Measurement{
		Timestamp:     time.Now().UTC(),
		Region:        "test",
		Target:        target,
		Protocol:      model.ProtoICMP,
		AddressFamily: model.IPv4,
		Success:       true,
		LatencyMS:     model.Float(rtt),
		AvgRTTMS:      model.Float(rtt),
		JitterMS:      model.Float(jitter),
		LossRatio:     model.Float(loss),
	}
}

func region(targets ...model.Target) []model.Region {
	return []model.Region{{ID: "test", DisplayName: "Test", Weight: 1, Targets: targets}}
}

func TestDistantButHealthyRegionScoresFull(t *testing.T) {
	// 280 ms to Sydney is physics, not a fault: with the baseline matching the
	// observed latency the score must stay at 100.
	regions := region(model.Target{ID: "syd", Region: "test", Hostname: "syd.example"})
	snapshot := Compute(Input{
		Regions: regions,
		Latest:  []model.Measurement{icmp("syd", 280, 2, 0)},
		Baselines: map[store.BaselineKey]model.Baseline{
			{Target: "syd", Protocol: model.ProtoICMP, AddressFamily: model.IPv4}: {MedianRTTMS: 278},
		},
		IPv4Enabled: true,
	})

	if snapshot.GlobalScore != 100 {
		t.Errorf("global score = %v, want 100 for a distant but normal path", snapshot.GlobalScore)
	}
	if snapshot.GlobalStatus != model.StatusExcellent {
		t.Errorf("status = %q, want excellent", snapshot.GlobalStatus)
	}
}

func TestLatencyAboveBaselineIsPenalised(t *testing.T) {
	regions := region(model.Target{ID: "tokyo", Region: "test", Hostname: "tokyo.example"})
	snapshot := Compute(Input{
		Regions: regions,
		Latest:  []model.Measurement{icmp("tokyo", 267, 2, 0)},
		Baselines: map[store.BaselineKey]model.Baseline{
			{Target: "tokyo", Protocol: model.ProtoICMP, AddressFamily: model.IPv4}: {MedianRTTMS: 195},
		},
		IPv4Enabled: true,
	})

	if snapshot.GlobalScore >= 100 {
		t.Errorf("score = %v, want a penalty for latency 37%% above baseline", snapshot.GlobalScore)
	}
}

func TestPacketLossDominatesTheScore(t *testing.T) {
	regions := region(model.Target{ID: "lossy", Region: "test", Hostname: "lossy.example"})
	snapshot := Compute(Input{
		Regions:     regions,
		Latest:      []model.Measurement{icmp("lossy", 50, 1, 0.05)},
		IPv4Enabled: true,
	})

	if snapshot.GlobalScore > 60 {
		t.Errorf("score = %v, want a heavy penalty for 5%% packet loss", snapshot.GlobalScore)
	}
	if snapshot.PacketLoss != 0.05 {
		t.Errorf("packet loss = %v, want 0.05", snapshot.PacketLoss)
	}
}

func TestUnreachableTargetScoresZero(t *testing.T) {
	regions := region(model.Target{ID: "dead", Region: "test", Hostname: "dead.example"})
	snapshot := Compute(Input{
		Regions: regions,
		Latest: []model.Measurement{{
			Timestamp: time.Now().UTC(), Region: "test", Target: "dead",
			Protocol: model.ProtoTCP, AddressFamily: model.IPv4, Success: false,
		}},
		IPv4Enabled: true,
	})

	if snapshot.GlobalScore != 0 {
		t.Errorf("score = %v, want 0", snapshot.GlobalScore)
	}
	if snapshot.GlobalStatus != model.StatusDown {
		t.Errorf("status = %q, want down", snapshot.GlobalStatus)
	}
}

func TestOneBrokenTargetDoesNotCondemnTheRegion(t *testing.T) {
	// The spec calls for at least two targets per region precisely so a single
	// broken host cannot make a whole region look unhealthy.
	regions := region(
		model.Target{ID: "good", Region: "test", Hostname: "good.example"},
		model.Target{ID: "dead", Region: "test", Hostname: "dead.example"},
	)
	snapshot := Compute(Input{
		Regions: regions,
		Latest: []model.Measurement{
			icmp("good", 40, 1, 0),
			{Timestamp: time.Now().UTC(), Region: "test", Target: "dead",
				Protocol: model.ProtoTCP, AddressFamily: model.IPv4, Success: false},
		},
		IPv4Enabled: true,
	})

	if snapshot.GlobalScore != 100 {
		t.Errorf("score = %v, want 100 when a healthy sibling target exists", snapshot.GlobalScore)
	}
}

func TestBrokenIPv6DoesNotSinkTheScore(t *testing.T) {
	regions := region(model.Target{ID: "dual", Region: "test", Hostname: "dual.example"})
	v6Failure := model.Measurement{
		Timestamp: time.Now().UTC(), Region: "test", Target: "dual",
		Protocol: model.ProtoICMP, AddressFamily: model.IPv6, Success: false,
		Error: "resolve: no ipv6 address",
	}
	snapshot := Compute(Input{
		Regions:     regions,
		Latest:      []model.Measurement{icmp("dual", 45, 1, 0), v6Failure},
		IPv4Enabled: true,
		IPv6Enabled: true,
	})

	if snapshot.GlobalScore != 100 {
		t.Errorf("score = %v, want 100: a broken IPv6 path is reported separately", snapshot.GlobalScore)
	}
	if snapshot.IPv6.Status != "down" {
		t.Errorf("ipv6 status = %q, want down", snapshot.IPv6.Status)
	}
	if snapshot.IPv4.Status != "healthy" {
		t.Errorf("ipv4 status = %q, want healthy", snapshot.IPv4.Status)
	}
}

func TestStaleMeasurementsAreIgnored(t *testing.T) {
	regions := region(model.Target{ID: "old", Region: "test", Hostname: "old.example"})
	stale := icmp("old", 40, 1, 0)
	stale.Timestamp = time.Now().Add(-2 * time.Hour).UTC()

	snapshot := Compute(Input{
		Regions:     regions,
		Latest:      []model.Measurement{stale},
		MaxAge:      5 * time.Minute,
		IPv4Enabled: true,
	})

	if snapshot.Regions[0].HasData {
		t.Error("a two-hour-old measurement should not count as current data")
	}
	if snapshot.StaleRegions != 1 {
		t.Errorf("stale regions = %d, want 1", snapshot.StaleRegions)
	}
	if snapshot.GlobalStatus != model.StatusUnknown {
		t.Errorf("status = %q, want unknown", snapshot.GlobalStatus)
	}
}

func TestGlobalScoreIsWeighted(t *testing.T) {
	regions := []model.Region{
		{ID: "a", DisplayName: "A", Weight: 3, Targets: []model.Target{{ID: "a1", Region: "a"}}},
		{ID: "b", DisplayName: "B", Weight: 1, Targets: []model.Target{{ID: "b1", Region: "b"}}},
	}
	a := icmp("a1", 40, 1, 0)
	a.Region = "a"
	b := icmp("b1", 40, 1, 0.05)
	b.Region = "b"

	snapshot := Compute(Input{Regions: regions, Latest: []model.Measurement{a, b}, IPv4Enabled: true})

	scores := map[string]float64{}
	for _, r := range snapshot.Regions {
		scores[r.ID] = r.Score
	}
	want := (scores["a"]*3 + scores["b"]*1) / 4
	if diff := snapshot.GlobalScore - want; diff > 0.11 || diff < -0.11 {
		t.Errorf("global score = %v, want ~%v", snapshot.GlobalScore, want)
	}
}

func TestStatusForScore(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{100, model.StatusExcellent},
		{95, model.StatusExcellent},
		{90, model.StatusGood},
		{75, model.StatusFair},
		{40, model.StatusDegraded},
		{0, model.StatusDown},
	}
	for _, tc := range cases {
		if got := model.StatusForScore(tc.score); got != tc.want {
			t.Errorf("StatusForScore(%v) = %q, want %q", tc.score, got, tc.want)
		}
	}
}
