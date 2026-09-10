package geoip

import (
	"math"
	"net/netip"
	"testing"
)

const (
	cityDB = "testdata/GeoLite2-City-Test.mmdb"
	asnDB  = "testdata/GeoLite2-ASN-Test.mmdb"
)

// These exercise decoding against real MaxMind-format records. The struct tags
// in geoip.go are the easiest thing in the package to get silently wrong, and
// a unit test with hand-built records would not catch a wrong tag.
func TestLookupDecodesRealRecords(t *testing.T) {
	r, err := Open(cityDB, asnDB)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })

	cases := []struct {
		addr       string
		lat, lon   float64
		city       string
		country    string
		asn        uint
		org        string
		confidence Confidence
	}{
		{"81.2.69.142", 51.5142, -0.0931, "London", "GB", 0, "", ConfidenceHigh},
		{"2.125.160.216", 51.75, -1.25, "Boxford", "GB", 0, "", ConfidenceHigh},
		{"89.160.20.112", 58.4167, 15.6167, "Linköping", "SE", 29518, "Bredband2 AB", ConfidenceHigh},
	}
	for _, tc := range cases {
		loc, ok := r.Lookup(netip.MustParseAddr(tc.addr))
		if !ok {
			t.Errorf("%s: not found", tc.addr)
			continue
		}
		if !loc.HasPosition() {
			t.Errorf("%s: no position decoded", tc.addr)
			continue
		}
		if math.Abs(*loc.Latitude-tc.lat) > 0.001 || math.Abs(*loc.Longitude-tc.lon) > 0.001 {
			t.Errorf("%s: position = %g,%g want %g,%g", tc.addr, *loc.Latitude, *loc.Longitude, tc.lat, tc.lon)
		}
		if loc.City != tc.city {
			t.Errorf("%s: city = %q want %q", tc.addr, loc.City, tc.city)
		}
		if loc.Country != tc.country {
			t.Errorf("%s: country = %q want %q", tc.addr, loc.Country, tc.country)
		}
		if loc.ASN != tc.asn {
			t.Errorf("%s: asn = %d want %d", tc.addr, loc.ASN, tc.asn)
		}
		if loc.Org != tc.org {
			t.Errorf("%s: org = %q want %q", tc.addr, loc.Org, tc.org)
		}
		if loc.Confidence != tc.confidence {
			t.Errorf("%s: confidence = %s want %s", tc.addr, loc.Confidence, tc.confidence)
		}
	}
}

// A record carrying a country but no city is a country centroid. It must be
// reported as low confidence, because the position says nothing about where
// inside the country the address actually is.
func TestLookupCountryOnlyIsLowConfidence(t *testing.T) {
	r, err := Open(cityDB, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })

	loc, ok := r.Lookup(netip.MustParseAddr("2001:218::1"))
	if !ok || !loc.HasPosition() {
		t.Fatalf("expected a positioned result, got ok=%v position=%v", ok, loc.HasPosition())
	}
	if loc.City != "" {
		t.Errorf("expected no city, got %q", loc.City)
	}
	if loc.Country != "JP" {
		t.Errorf("country = %q want JP", loc.Country)
	}
	if loc.Confidence != ConfidenceLow {
		t.Errorf("confidence = %s want %s", loc.Confidence, ConfidenceLow)
	}
}

// An address the ASN database knows but the city database does not still yields
// useful ownership information, with no position and so no confidence claim.
func TestLookupASNOnly(t *testing.T) {
	r, err := Open(cityDB, asnDB)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })

	loc, ok := r.Lookup(netip.MustParseAddr("1.128.0.1"))
	if !ok {
		t.Fatal("expected a hit from the ASN database")
	}
	if loc.HasPosition() {
		t.Error("expected no position")
	}
	if loc.ASN != 1221 || loc.Org != "Telstra Pty Ltd" {
		t.Errorf("asn/org = %d/%q want 1221/\"Telstra Pty Ltd\"", loc.ASN, loc.Org)
	}
	if loc.Confidence != "" {
		t.Errorf("confidence = %q, want empty when there is no position", loc.Confidence)
	}
}

// Private addresses must never reach the database at all.
func TestLookupSkipsPrivateEvenWithDatabase(t *testing.T) {
	r, err := Open(cityDB, asnDB)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })

	for _, s := range []string{"192.168.1.1", "10.0.0.1", "100.64.0.1", "127.0.0.1"} {
		if _, ok := r.Lookup(netip.MustParseAddr(s)); ok {
			t.Errorf("%s: expected no lookup for a non-public address", s)
		}
	}
}

func TestLocatable(t *testing.T) {
	cases := []struct {
		addr string
		want bool
		why  string
	}{
		{"8.8.8.8", true, "ordinary public v4"},
		{"2001:4860:4860::8888", true, "ordinary public v6"},
		{"192.168.1.1", false, "RFC1918"},
		{"10.0.0.1", false, "RFC1918"},
		{"172.16.0.1", false, "RFC1918"},
		{"127.0.0.1", false, "loopback"},
		{"::1", false, "v6 loopback"},
		{"169.254.1.1", false, "link-local"},
		{"fe80::1", false, "v6 link-local"},
		{"fd00::1", false, "v6 unique local"},
		{"224.0.0.1", false, "multicast"},
		{"0.0.0.0", false, "unspecified"},
		{"100.64.0.1", false, "carrier-grade NAT, low end"},
		{"100.127.255.254", false, "carrier-grade NAT, high end"},
		{"100.63.255.255", true, "just below the CGNAT range"},
		{"100.128.0.1", true, "just above the CGNAT range"},
		{"::ffff:192.168.1.1", false, "v4-mapped private address must unmap first"},
		{"::ffff:8.8.8.8", true, "v4-mapped public address"},
	}
	for _, tc := range cases {
		addr, err := netip.ParseAddr(tc.addr)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.addr, err)
		}
		if got := Locatable(addr); got != tc.want {
			t.Errorf("Locatable(%s) = %v, want %v (%s)", tc.addr, got, tc.want, tc.why)
		}
	}
}

func TestLocatableRejectsInvalid(t *testing.T) {
	if Locatable(netip.Addr{}) {
		t.Error("the zero Addr must not be locatable")
	}
}

// A nil Resolver stands in for "no GeoIP database configured", which is the
// default. It has to stay usable so that callers never branch on it.
func TestNilResolverIsUsable(t *testing.T) {
	var r *Resolver
	loc, ok := r.Lookup(netip.MustParseAddr("8.8.8.8"))
	if ok {
		t.Error("nil resolver reported a hit")
	}
	if loc.HasPosition() {
		t.Error("nil resolver returned a position")
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close on nil resolver: %v", err)
	}
}

func TestOpenWithNoPathsReturnsNilResolver(t *testing.T) {
	r, err := Open("", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if r != nil {
		t.Fatal("expected a nil resolver when no database is configured")
	}
	// The nil resolver Open just handed back must itself be safe to use.
	if _, ok := r.Lookup(netip.MustParseAddr("1.1.1.1")); ok {
		t.Error("nil resolver reported a hit")
	}
}

func TestOpenMissingFileFails(t *testing.T) {
	if _, err := Open("/nonexistent/city.mmdb", ""); err == nil {
		t.Fatal("expected an error for a missing database")
	}
}

func TestConfidenceFor(t *testing.T) {
	cases := []struct {
		city   string
		radius uint16
		want   Confidence
	}{
		{"Frankfurt", 20, ConfidenceHigh},
		{"Frankfurt", 100, ConfidenceHigh},
		{"Frankfurt", 101, ConfidenceMedium},
		{"Frankfurt", 500, ConfidenceMedium},
		{"Frankfurt", 501, ConfidenceLow},
		{"Frankfurt", 0, ConfidenceMedium},
		{"", 10, ConfidenceLow},
		{"", 0, ConfidenceLow},
	}
	for _, tc := range cases {
		if got := confidenceFor(tc.city, tc.radius); got != tc.want {
			t.Errorf("confidenceFor(%q, %d) = %s, want %s", tc.city, tc.radius, got, tc.want)
		}
	}
}

func TestPickNameIsDeterministic(t *testing.T) {
	names := map[string]string{"de": "Frankfurt am Main", "en": "Frankfurt", "fr": "Francfort"}
	if got := pickName(names); got != "Frankfurt" {
		t.Errorf("pickName preferred %q, want the English name", got)
	}

	// Without English the choice must still be stable across calls, since map
	// iteration order in Go is deliberately randomised.
	noEnglish := map[string]string{"de": "München", "fr": "Munich", "es": "Múnich"}
	first := pickName(noEnglish)
	for i := 0; i < 50; i++ {
		if got := pickName(noEnglish); got != first {
			t.Fatalf("pickName returned %q then %q for the same input", first, got)
		}
	}
	if first != "München" {
		t.Errorf("pickName = %q, want the alphabetically first language (de)", first)
	}

	if got := pickName(nil); got != "" {
		t.Errorf("pickName(nil) = %q, want empty", got)
	}
}
