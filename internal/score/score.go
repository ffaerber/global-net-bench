// Package score turns raw measurements into the several health numbers the
// dashboard shows. There is deliberately no single opaque "speed" figure.
package score

import (
	"math"
	"sort"
	"time"

	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/store"
)

// Penalty records why a score is not 100, so the UI can explain itself.
type Penalty struct {
	Reason string  `json:"reason"`
	Points float64 `json:"points"`
}

type FamilyHealth struct {
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
	Success int    `json:"success"`
	Total   int    `json:"total"`
}

type FamilyScore struct {
	AddressFamily model.AddressFamily `json:"address_family"`
	Score         float64             `json:"score"`
	Reachable     bool                `json:"reachable"`
	RTTMS         *float64            `json:"rtt_ms,omitempty"`
	JitterMS      *float64            `json:"jitter_ms,omitempty"`
	LossRatio     *float64            `json:"loss_ratio,omitempty"`
	TCPConnectMS  *float64            `json:"tcp_connect_ms,omitempty"`
	TLSMS         *float64            `json:"tls_ms,omitempty"`
	TTFBMS        *float64            `json:"ttfb_ms,omitempty"`
	BaselineRTTMS *float64            `json:"baseline_rtt_ms,omitempty"`
	Penalties     []Penalty           `json:"penalties,omitempty"`
}

type TargetScore struct {
	Target    string        `json:"target"`
	Hostname  string        `json:"hostname"`
	Score     float64       `json:"score"`
	Status    string        `json:"status"`
	HasData   bool          `json:"has_data"`
	Families  []FamilyScore `json:"families"`
	UpdatedAt *time.Time    `json:"updated_at,omitempty"`
}

type RegionScore struct {
	ID           string        `json:"id"`
	DisplayName  string        `json:"display_name"`
	Weight       float64       `json:"weight"`
	Local        bool          `json:"local"`
	Score        float64       `json:"score"`
	Status       string        `json:"status"`
	HasData      bool          `json:"has_data"`
	RTTMS        *float64      `json:"rtt_ms,omitempty"`
	JitterMS     *float64      `json:"jitter_ms,omitempty"`
	LossRatio    *float64      `json:"loss_ratio,omitempty"`
	TCPConnectMS *float64      `json:"tcp_connect_ms,omitempty"`
	TTFBMS       *float64      `json:"ttfb_ms,omitempty"`
	IPv4         FamilyHealth  `json:"ipv4"`
	IPv6         FamilyHealth  `json:"ipv6"`
	Targets      []TargetScore `json:"targets"`
	UpdatedAt    *time.Time    `json:"updated_at,omitempty"`
}

type ResolverHealth struct {
	Name      string   `json:"name"`
	Resolver  string   `json:"resolver"`
	Success   bool     `json:"success"`
	LatencyMS *float64 `json:"latency_ms,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type Snapshot struct {
	GeneratedAt  time.Time        `json:"generated_at"`
	Site         string           `json:"site"`
	GlobalScore  float64          `json:"global_score"`
	GlobalStatus string           `json:"global_status"`
	LocalScore   *float64         `json:"local_score,omitempty"`
	LocalStatus  string           `json:"local_status,omitempty"`
	Regions      []RegionScore    `json:"regions"`
	IPv4         FamilyHealth     `json:"ipv4"`
	IPv6         FamilyHealth     `json:"ipv6"`
	Resolvers    []ResolverHealth `json:"resolvers"`
	PacketLoss   float64          `json:"packet_loss_ratio"`
	RouteChanges int              `json:"route_changes_today"`
	StaleRegions int              `json:"stale_regions"`
}

type Input struct {
	Site         string
	Regions      []model.Region
	Latest       []model.Measurement
	Baselines    map[store.BaselineKey]model.Baseline
	RouteChanges map[string]int
	// MaxAge marks measurements older than this as stale, so a stopped
	// scheduler shows as "unknown" instead of a frozen green dashboard.
	MaxAge            time.Duration
	IPv4Enabled       bool
	IPv6Enabled       bool
	RouteChangesToday int
}

// Compute builds the full snapshot of scores.
func Compute(in Input) Snapshot {
	now := time.Now().UTC()
	snapshot := Snapshot{
		GeneratedAt:  now,
		Site:         in.Site,
		RouteChanges: in.RouteChangesToday,
		IPv4:         FamilyHealth{Enabled: in.IPv4Enabled},
		IPv6:         FamilyHealth{Enabled: in.IPv6Enabled},
	}

	byTarget := map[string][]model.Measurement{}
	for _, m := range in.Latest {
		if in.MaxAge > 0 && now.Sub(m.Timestamp) > in.MaxAge {
			continue
		}
		byTarget[m.Target] = append(byTarget[m.Target], m)
	}

	var lossSum float64
	var lossCount int

	for _, region := range in.Regions {
		rs := RegionScore{
			ID:          region.ID,
			DisplayName: region.DisplayName,
			Weight:      region.Weight,
			Local:       region.Local,
			Status:      model.StatusUnknown,
			IPv4:        FamilyHealth{Enabled: in.IPv4Enabled},
			IPv6:        FamilyHealth{Enabled: in.IPv6Enabled},
		}

		var targetScores []float64
		for _, target := range region.Targets {
			ts := scoreTarget(target, byTarget[target.ID], region, in)
			rs.Targets = append(rs.Targets, ts)
			if !ts.HasData {
				continue
			}
			targetScores = append(targetScores, ts.Score)
			if ts.UpdatedAt != nil && (rs.UpdatedAt == nil || ts.UpdatedAt.After(*rs.UpdatedAt)) {
				rs.UpdatedAt = ts.UpdatedAt
			}
			for _, fam := range ts.Families {
				health := &rs.IPv4
				if fam.AddressFamily == model.IPv6 {
					health = &rs.IPv6
				}
				health.Total++
				if fam.Reachable {
					health.Success++
				}
			}
		}

		if len(targetScores) > 0 {
			rs.HasData = true
			rs.Score = combineTargets(targetScores)
			rs.Status = model.StatusForScore(rs.Score)
			best := bestFamily(rs.Targets)
			if best != nil {
				rs.RTTMS = best.RTTMS
				rs.JitterMS = best.JitterMS
				rs.LossRatio = best.LossRatio
				rs.TCPConnectMS = best.TCPConnectMS
				rs.TTFBMS = best.TTFBMS
				if best.LossRatio != nil {
					lossSum += *best.LossRatio
					lossCount++
				}
			}
		} else {
			snapshot.StaleRegions++
		}

		rs.IPv4.Status = familyStatus(rs.IPv4)
		rs.IPv6.Status = familyStatus(rs.IPv6)
		snapshot.IPv4.Total += rs.IPv4.Total
		snapshot.IPv4.Success += rs.IPv4.Success
		snapshot.IPv6.Total += rs.IPv6.Total
		snapshot.IPv6.Success += rs.IPv6.Success

		snapshot.Regions = append(snapshot.Regions, rs)
	}

	snapshot.IPv4.Status = familyStatus(snapshot.IPv4)
	snapshot.IPv6.Status = familyStatus(snapshot.IPv6)

	if lossCount > 0 {
		snapshot.PacketLoss = lossSum / float64(lossCount)
	}

	var weightSum, weighted float64
	var localScores []float64
	for _, rs := range snapshot.Regions {
		if !rs.HasData {
			continue
		}
		weight := rs.Weight
		if weight <= 0 {
			weight = 1
		}
		weighted += rs.Score * weight
		weightSum += weight
		if rs.Local {
			localScores = append(localScores, rs.Score)
		}
	}
	if weightSum > 0 {
		snapshot.GlobalScore = round1(weighted / weightSum)
	}
	snapshot.GlobalStatus = model.StatusForScore(snapshot.GlobalScore)
	if weightSum == 0 {
		snapshot.GlobalStatus = model.StatusUnknown
	}

	snapshot.Resolvers = resolverHealth(in.Latest, in.MaxAge, now)

	if len(localScores) > 0 {
		local := mean(localScores)
		// The local score is about the user's own connection, so a failing
		// resolver counts against it directly.
		if failing := failingResolvers(snapshot.Resolvers); failing > 0 {
			local -= math.Min(20, float64(failing)*10)
		}
		local = clamp(local)
		snapshot.LocalScore = &local
		snapshot.LocalStatus = model.StatusForScore(local)
	}

	return snapshot
}

func scoreTarget(target model.Target, measurements []model.Measurement, region model.Region, in Input) TargetScore {
	ts := TargetScore{Target: target.ID, Hostname: target.Hostname, Status: model.StatusUnknown}
	if len(measurements) == 0 {
		return ts
	}

	byFamily := map[model.AddressFamily][]model.Measurement{}
	for _, m := range measurements {
		byFamily[m.AddressFamily] = append(byFamily[m.AddressFamily], m)
		if ts.UpdatedAt == nil || m.Timestamp.After(*ts.UpdatedAt) {
			t := m.Timestamp
			ts.UpdatedAt = &t
		}
	}

	families := []model.AddressFamily{model.IPv4, model.IPv6}
	var best *float64
	for _, af := range families {
		group := byFamily[af]
		if len(group) == 0 {
			continue
		}
		fs := scoreFamily(af, group, region, target, in)
		ts.Families = append(ts.Families, fs)
		if best == nil || fs.Score > *best {
			v := fs.Score
			best = &v
		}
	}

	if best != nil {
		ts.HasData = true
		// A target is as healthy as its best working address family. A broken
		// IPv6 deployment is reported separately rather than halving the score.
		ts.Score = round1(*best)
		ts.Status = model.StatusForScore(ts.Score)
	}
	return ts
}

func scoreFamily(af model.AddressFamily, measurements []model.Measurement, region model.Region, target model.Target, in Input) FamilyScore {
	fs := FamilyScore{AddressFamily: af}

	var tcpTotal, tcpOK, httpTotal, httpOK int
	for _, m := range measurements {
		if m.Success {
			fs.Reachable = true
		}
		switch m.Protocol {
		case model.ProtoICMP:
			if m.Success {
				fs.RTTMS = m.AvgRTTMS
				fs.JitterMS = m.JitterMS
			}
			if m.LossRatio != nil {
				fs.LossRatio = m.LossRatio
			}
		case model.ProtoTCP:
			tcpTotal++
			if m.Success {
				tcpOK++
				if fs.TCPConnectMS == nil || (m.ConnectMS != nil && *m.ConnectMS < *fs.TCPConnectMS) {
					fs.TCPConnectMS = m.ConnectMS
				}
			}
		case model.ProtoHTTPS:
			httpTotal++
			if m.Success {
				httpOK++
				fs.TTFBMS = m.TTFBMS
				fs.TLSMS = m.TLSMS
			}
		}
	}

	// With ICMP filtered, the TCP handshake is the best latency signal available.
	if fs.RTTMS == nil {
		fs.RTTMS = fs.TCPConnectMS
	}

	if !fs.Reachable {
		fs.Penalties = append(fs.Penalties, Penalty{Reason: "unreachable on every protocol", Points: 100})
		return fs
	}

	score := 100.0
	penalize := func(reason string, points float64) {
		if points <= 0.05 {
			return
		}
		score -= points
		fs.Penalties = append(fs.Penalties, Penalty{Reason: reason, Points: round1(points)})
	}

	if fs.LossRatio != nil && *fs.LossRatio > 0 {
		penalize("packet loss", math.Min(45, *fs.LossRatio*100*9))
	}

	baselineRTT := baselineFor(in.Baselines, target.ID, model.ProtoICMP, af)
	if baselineRTT == nil {
		baselineRTT = baselineFor(in.Baselines, target.ID, model.ProtoTCP, af)
	}
	reference := baselineRTT
	if reference == nil && region.ExpectedRTTMS > 0 {
		// Without history, fall back to the physical expectation for the region.
		reference = &region.ExpectedRTTMS
	}
	fs.BaselineRTTMS = reference

	if reference != nil && *reference > 0 && fs.RTTMS != nil {
		ratio := *fs.RTTMS / *reference
		// Distance itself is never a fault; only being slower than this path's
		// own normal is.
		if ratio > 1.15 && *fs.RTTMS-*reference > 5 {
			penalize("latency above baseline", math.Min(25, (ratio-1.15)/0.85*25))
		}
	}

	if fs.JitterMS != nil && *fs.JitterMS > 5 {
		penalize("jitter", math.Min(15, (*fs.JitterMS-5)/45*15))
	}

	if tcpTotal > 0 {
		penalize("tcp connect failures", (1-float64(tcpOK)/float64(tcpTotal))*20)
	}
	if httpTotal > 0 {
		penalize("http failures", (1-float64(httpOK)/float64(httpTotal))*10)
	}
	if changes := in.RouteChanges[target.ID]; changes > 0 {
		penalize("route instability", math.Min(5, float64(changes)*2))
	}

	fs.Score = round1(clamp(score))
	return fs
}

// combineTargets keeps one broken endpoint from condemning a whole region: with
// three or more targets the median rules, otherwise the healthiest one does.
func combineTargets(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	sorted := append([]float64(nil), scores...)
	sort.Float64s(sorted)
	if len(sorted) >= 3 {
		mid := len(sorted) / 2
		if len(sorted)%2 == 1 {
			return round1(sorted[mid])
		}
		return round1((sorted[mid-1] + sorted[mid]) / 2)
	}
	return round1(sorted[len(sorted)-1])
}

func bestFamily(targets []TargetScore) *FamilyScore {
	var best *FamilyScore
	for i := range targets {
		for j := range targets[i].Families {
			fam := &targets[i].Families[j]
			if !fam.Reachable {
				continue
			}
			if best == nil || fam.Score > best.Score {
				best = fam
			}
		}
	}
	return best
}

func familyStatus(h FamilyHealth) string {
	if !h.Enabled {
		return "disabled"
	}
	if h.Total == 0 {
		return model.StatusUnknown
	}
	switch {
	case h.Success == h.Total:
		return "healthy"
	case h.Success == 0:
		return "down"
	default:
		return "degraded"
	}
}

func resolverHealth(measurements []model.Measurement, maxAge time.Duration, now time.Time) []ResolverHealth {
	seen := map[string]ResolverHealth{}
	var order []string
	for _, m := range measurements {
		if m.Protocol != model.ProtoDNS {
			continue
		}
		if maxAge > 0 && now.Sub(m.Timestamp) > maxAge {
			continue
		}
		existing, ok := seen[m.Target]
		if !ok {
			order = append(order, m.Target)
			existing = ResolverHealth{Name: m.Target, Resolver: m.Resolver, Success: m.Success, LatencyMS: m.LatencyMS, Error: m.Error}
		} else if !m.Success {
			existing.Success = false
			existing.Error = m.Error
		}
		seen[m.Target] = existing
	}

	out := make([]ResolverHealth, 0, len(order))
	for _, name := range order {
		out = append(out, seen[name])
	}
	return out
}

func failingResolvers(resolvers []ResolverHealth) int {
	var n int
	for _, r := range resolvers {
		if !r.Success {
			n++
		}
	}
	return n
}

func baselineFor(baselines map[store.BaselineKey]model.Baseline, target string, proto model.Protocol, af model.AddressFamily) *float64 {
	b, ok := baselines[store.BaselineKey{Target: target, Protocol: proto, AddressFamily: af}]
	if !ok || b.MedianRTTMS <= 0 {
		return nil
	}
	value := b.MedianRTTMS
	return &value
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func clamp(v float64) float64 {
	return math.Max(0, math.Min(100, v))
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
