package probe

import (
	"math"
	"net/netip"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	rtts := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
	}
	stats := Summarize(rtts)

	if stats.Samples != 4 {
		t.Fatalf("samples = %d, want 4", stats.Samples)
	}
	if stats.MinMS != 10 || stats.MaxMS != 40 {
		t.Errorf("min/max = %v/%v, want 10/40", stats.MinMS, stats.MaxMS)
	}
	if stats.AvgMS != 25 {
		t.Errorf("avg = %v, want 25", stats.AvgMS)
	}
	if stats.MedMS != 25 {
		t.Errorf("median = %v, want 25", stats.MedMS)
	}
	// Each consecutive pair differs by exactly 10 ms.
	if stats.JitterMS != 10 {
		t.Errorf("jitter = %v, want 10", stats.JitterMS)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	if got := Summarize(nil); got.Samples != 0 || got.AvgMS != 0 {
		t.Errorf("empty summary = %+v, want zero value", got)
	}
}

func TestSummarizeSingleSampleHasNoJitter(t *testing.T) {
	stats := Summarize([]time.Duration{15 * time.Millisecond})
	if stats.JitterMS != 0 {
		t.Errorf("jitter = %v, want 0 with a single sample", stats.JitterMS)
	}
	if stats.MedMS != 15 {
		t.Errorf("median = %v, want 15", stats.MedMS)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5}
	cases := []struct {
		p    float64
		want float64
	}{
		{0, 1},
		{0.5, 3},
		{1, 5},
	}
	for _, tc := range cases {
		if got := Percentile(sorted, tc.p); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("Percentile(%v) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestFingerprintIgnoresSilentHops(t *testing.T) {
	withSilent := TracerouteResult{Hops: []Hop{
		{TTL: 1, Addr: netip.MustParseAddr("10.0.0.1")},
		{TTL: 2}, // no reply
		{TTL: 3, Addr: netip.MustParseAddr("10.0.0.3")},
	}}
	withoutSilent := TracerouteResult{Hops: []Hop{
		{TTL: 1, Addr: netip.MustParseAddr("10.0.0.1")},
		{TTL: 3, Addr: netip.MustParseAddr("10.0.0.3")},
	}}

	// A router that intermittently declines to answer must not look like a
	// route change.
	if withSilent.Fingerprint() != withoutSilent.Fingerprint() {
		t.Error("a silent hop changed the fingerprint")
	}
}

func TestFingerprintChangesWithPath(t *testing.T) {
	a := TracerouteResult{Hops: []Hop{{TTL: 1, Addr: netip.MustParseAddr("10.0.0.1")}}}
	b := TracerouteResult{Hops: []Hop{{TTL: 1, Addr: netip.MustParseAddr("10.0.0.2")}}}
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("different paths produced the same fingerprint")
	}
	if a.Fingerprint() == "" {
		t.Error("fingerprint is empty for a responding path")
	}
}

func TestFingerprintEmptyWhenNothingResponded(t *testing.T) {
	result := TracerouteResult{Hops: []Hop{{TTL: 1}, {TTL: 2}}}
	if result.Fingerprint() != "" {
		t.Error("expected an empty fingerprint when no hop responded")
	}
}

func TestEchoFromQuotedPacket(t *testing.T) {
	// Minimal IPv4 header (20 bytes) followed by the first 8 bytes of an echo
	// request carrying id 0x1234 and sequence 0x0005.
	quoted := make([]byte, 28)
	quoted[0] = 0x45
	quoted[20] = 8
	quoted[24], quoted[25] = 0x12, 0x34
	quoted[26], quoted[27] = 0x00, 0x05

	id, seq, ok := echoFromQuotedPacket(quoted, false)
	if !ok {
		t.Fatal("failed to parse the quoted packet")
	}
	if id != 0x1234 || seq != 5 {
		t.Errorf("id/seq = %#x/%d, want 0x1234/5", id, seq)
	}
}

func TestEchoFromQuotedPacketTooShort(t *testing.T) {
	if _, _, ok := echoFromQuotedPacket([]byte{0x45, 0, 0}, false); ok {
		t.Error("expected failure on a truncated packet")
	}
}
