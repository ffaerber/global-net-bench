package engine

import (
	"net/netip"
	"testing"

	"github.com/ffaerber/global-net-bench/internal/config"
	"github.com/ffaerber/global-net-bench/internal/geoip"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/score"
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

func testResolver(t *testing.T) *geoip.Resolver {
	t.Helper()
	resolver, err := geoip.Open("../geoip/testdata/GeoLite2-City-Test.mmdb", "../geoip/testdata/GeoLite2-ASN-Test.mmdb")
	if err != nil {
		t.Fatalf("open test databases: %v", err)
	}
	t.Cleanup(func() { resolver.Close() })
	return resolver
}

func resolved(region, target string, protocol model.Protocol, ip string) model.Measurement {
	return model.Measurement{
		Region:        region,
		Target:        target,
		Protocol:      protocol,
		AddressFamily: model.IPv4,
		Success:       true,
		ResolvedIP:    ip,
	}
}

// Averaging the positions of a region's targets would put the dot somewhere no
// packet went, so the endpoints vote and the majority city wins.
func TestRegionPositionsVoteByCity(t *testing.T) {
	e := &Engine{geo: testResolver(t)}

	positions := e.regionPositions([]model.Measurement{
		resolved("uk", "lhr-1", model.ProtoICMP, "81.2.69.142"),
		resolved("uk", "lhr-2", model.ProtoICMP, "81.2.69.144"),
		resolved("uk", "odd", model.ProtoICMP, "216.160.83.56"), // registrant's office, not the region
	})

	p, ok := positions["uk"]
	if !ok {
		t.Fatal("expected the region to be placed")
	}
	if p.City != "London" {
		t.Errorf("city = %q, want London: the outlier outvoted the majority", p.City)
	}
	if p.Source != score.PositionSourceGeoIP {
		t.Errorf("source = %q, want %q", p.Source, score.PositionSourceGeoIP)
	}
	if p.IP != "81.2.69.142" {
		t.Errorf("ip = %q, want the lowest address of the winning group", p.IP)
	}
}

// Each protocol run against a target repeats the same resolved address. Letting
// those count separately would mean a target with an HTTPS test outvoting one
// without, which has nothing to do with where either of them is.
func TestRegionPositionsCountEachAddressOnce(t *testing.T) {
	e := &Engine{geo: testResolver(t)}

	positions := e.regionPositions([]model.Measurement{
		resolved("uk", "odd", model.ProtoICMP, "67.43.156.1"), // country centroid, low confidence
		resolved("uk", "odd", model.ProtoTCP, "67.43.156.1"),
		resolved("uk", "odd", model.ProtoHTTPS, "67.43.156.1"),
		resolved("uk", "lhr", model.ProtoICMP, "81.2.69.142"),
	})

	p, ok := positions["uk"]
	if !ok {
		t.Fatal("expected the region to be placed")
	}
	// One address each way, so the better-graded record decides.
	if p.City != "London" {
		t.Errorf("city = %q, want London: repeated protocols were counted as separate votes", p.City)
	}
}

func TestRegionPositionsSkipUnplaceableAddresses(t *testing.T) {
	e := &Engine{geo: testResolver(t)}

	positions := e.regionPositions([]model.Measurement{
		resolved("local", "gateway", model.ProtoICMP, "192.168.1.1"),
		resolved("cgnat", "gateway", model.ProtoICMP, "100.64.0.1"),
		resolved("broken", "target", model.ProtoICMP, "not-an-address"),
		resolved(DNSRegion, "quad9", model.ProtoDNS, "81.2.69.142"),
	})

	if len(positions) != 0 {
		t.Errorf("positions = %+v, want none", positions)
	}
}

func TestRegionPositionsWithoutDatabase(t *testing.T) {
	e := &Engine{}
	if got := e.regionPositions([]model.Measurement{resolved("uk", "lhr", model.ProtoICMP, "81.2.69.142")}); got != nil {
		t.Errorf("expected no positions when GeoIP is disabled, got %+v", got)
	}
}

func TestPublicOriginLocatesTheEgressAddress(t *testing.T) {
	e := &Engine{
		geo:      testResolver(t),
		cfg:      &config.Config{Site: config.Site{ID: "home", Name: "Home Lab"}},
		publicIP: newPublicIPWatcher(),
	}
	e.publicIP.current[model.IPv4] = PublicAddress{AddressFamily: model.IPv4, IP: "81.2.69.142"}

	origin := e.publicOrigin()
	if origin == nil {
		t.Fatal("expected the public address to be located")
	}
	if origin.City != "London" || origin.IP != "81.2.69.142" {
		t.Errorf("origin = %+v, want London / 81.2.69.142", origin)
	}
	if origin.Label != "Home Lab" {
		t.Errorf("label = %q, want the site name", origin.Label)
	}
	if origin.Region != "" {
		t.Errorf("region = %q, want it empty: this origin is not a region", origin.Region)
	}
}

// A CGNAT or private egress address says nothing about where the site is, and
// an invented origin would be worse than none.
func TestPublicOriginIgnoresUnlocatableAddresses(t *testing.T) {
	e := &Engine{
		geo:      testResolver(t),
		cfg:      &config.Config{Site: config.Site{ID: "home"}},
		publicIP: newPublicIPWatcher(),
	}
	e.publicIP.current[model.IPv4] = PublicAddress{AddressFamily: model.IPv4, IP: "100.64.12.9"}

	if origin := e.publicOrigin(); origin != nil {
		t.Errorf("origin = %+v, want none for a carrier-NAT address", origin)
	}
}

func TestPublicOriginWithoutDatabase(t *testing.T) {
	e := &Engine{publicIP: newPublicIPWatcher()}
	e.publicIP.current[model.IPv4] = PublicAddress{AddressFamily: model.IPv4, IP: "81.2.69.142"}
	if origin := e.publicOrigin(); origin != nil {
		t.Errorf("origin = %+v, want none when GeoIP is disabled", origin)
	}
}
