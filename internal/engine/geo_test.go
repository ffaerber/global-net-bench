package engine

import (
	"net/netip"
	"testing"

	"github.com/ffaerber/global-net-bench/internal/geoip"
)

// The engine holds a possibly-nil resolver. Enrichment has to work in both
// states without the traceroute path needing to know which it is.
func TestLookupHopGeoWithoutDatabase(t *testing.T) {
	e := &Engine{}
	if got := e.lookupHopGeo(netip.MustParseAddr("81.2.69.142")); got != nil {
		t.Errorf("expected nil geo when GeoIP is disabled, got %+v", got)
	}
}

func TestLookupHopGeoEnriches(t *testing.T) {
	resolver, err := geoip.Open("../geoip/testdata/GeoLite2-City-Test.mmdb", "../geoip/testdata/GeoLite2-ASN-Test.mmdb")
	if err != nil {
		t.Fatalf("open test databases: %v", err)
	}
	t.Cleanup(func() { resolver.Close() })
	e := &Engine{geo: resolver}

	geo := e.lookupHopGeo(netip.MustParseAddr("89.160.20.112"))
	if geo == nil {
		t.Fatal("expected the hop to be enriched")
	}
	if !geo.HasPosition() {
		t.Fatal("expected a position")
	}
	if geo.City != "Linköping" || geo.Country != "SE" {
		t.Errorf("city/country = %q/%q want \"Linköping\"/\"SE\"", geo.City, geo.Country)
	}
	if geo.ASN != 29518 || geo.Org != "Bredband2 AB" {
		t.Errorf("asn/org = %d/%q want 29518/\"Bredband2 AB\"", geo.ASN, geo.Org)
	}
	if geo.Confidence != string(geoip.ConfidenceHigh) {
		t.Errorf("confidence = %q want %q", geo.Confidence, geoip.ConfidenceHigh)
	}
}

// A hop inside the local network has no public position, and asking for one
// would only invent a misleading answer.
func TestLookupHopGeoSkipsPrivateHops(t *testing.T) {
	resolver, err := geoip.Open("../geoip/testdata/GeoLite2-City-Test.mmdb", "")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { resolver.Close() })
	e := &Engine{geo: resolver}

	for _, addr := range []string{"192.168.1.1", "10.0.0.1", "100.64.0.1"} {
		if got := e.lookupHopGeo(netip.MustParseAddr(addr)); got != nil {
			t.Errorf("%s: expected nil geo for a non-public hop, got %+v", addr, got)
		}
	}
}
