package probe

import (
	"math"
	"sort"
	"time"
)

// Stats summarises a set of round-trip times in milliseconds.
type Stats struct {
	Samples int     `json:"samples"`
	MinMS   float64 `json:"min_ms"`
	AvgMS   float64 `json:"avg_ms"`
	MedMS   float64 `json:"median_ms"`
	P95MS   float64 `json:"p95_ms"`
	P99MS   float64 `json:"p99_ms"`
	MaxMS   float64 `json:"max_ms"`
	// JitterMS is the mean absolute difference between consecutive RTTs, which
	// reflects what real-time traffic actually experiences better than stddev.
	JitterMS    float64 `json:"jitter_ms"`
	P95JitterMS float64 `json:"p95_jitter_ms"`
}

// Summarize computes latency statistics. RTTs must be in arrival order so the
// jitter figures reflect consecutive packets.
func Summarize(rtts []time.Duration) Stats {
	if len(rtts) == 0 {
		return Stats{}
	}
	ms := make([]float64, len(rtts))
	for i, d := range rtts {
		ms[i] = float64(d.Nanoseconds()) / 1e6
	}

	var deltas []float64
	for i := 1; i < len(ms); i++ {
		deltas = append(deltas, math.Abs(ms[i]-ms[i-1]))
	}

	var sum float64
	for _, v := range ms {
		sum += v
	}

	sorted := append([]float64(nil), ms...)
	sort.Float64s(sorted)

	s := Stats{
		Samples: len(ms),
		MinMS:   sorted[0],
		MaxMS:   sorted[len(sorted)-1],
		AvgMS:   sum / float64(len(ms)),
		MedMS:   Percentile(sorted, 0.50),
		P95MS:   Percentile(sorted, 0.95),
		P99MS:   Percentile(sorted, 0.99),
	}
	if len(deltas) > 0 {
		var dsum float64
		for _, v := range deltas {
			dsum += v
		}
		s.JitterMS = dsum / float64(len(deltas))
		sortedDeltas := append([]float64(nil), deltas...)
		sort.Float64s(sortedDeltas)
		s.P95JitterMS = Percentile(sortedDeltas, 0.95)
	}
	return s
}

// Percentile interpolates between neighbours in an already sorted slice.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lower := int(pos)
	if lower+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[lower] + (pos-float64(lower))*(sorted[lower+1]-sorted[lower])
}

func msFloat(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}
