package engine

import (
	"net/netip"
	"sort"

	"github.com/ffaerber/global-net-bench/internal/geoip"
	"github.com/ffaerber/global-net-bench/internal/model"
	"github.com/ffaerber/global-net-bench/internal/score"
)

// This file turns addresses the monitor already knows about into positions on
// the dashboard map: the public egress address becomes the point the arcs start
// from, and the addresses a region's targets resolve to become that region's
// dot. Neither needs a single coordinate in the configuration.
//
// It is inference and is labelled as such all the way to the UI. An address is
// placed by where its block is registered, not by where the hardware is, so an
// anycast endpoint or a recently reassigned range can land on the wrong
// continent. A configured position therefore always wins over anything here.

// regionPositions infers a position per region from the addresses its targets
// resolved to during the most recent measurements. Regions with configured
// coordinates are included too; score.Compute ignores them in favour of the
// configuration, and including them keeps this function independent of how that
// preference is decided.
func (e *Engine) regionPositions(latest []model.Measurement) map[string]score.Position {
	if e.geo == nil {
		return nil
	}

	samples := map[string][]score.Position{}
	seen := map[string]bool{}
	for _, m := range latest {
		if m.ResolvedIP == "" || m.Region == "" || m.Region == DNSRegion {
			continue
		}
		// Every protocol against a target repeats the same resolved address, so
		// counting each one would let a region with an HTTPS test outvote one
		// without.
		key := m.Region + "|" + m.ResolvedIP
		if seen[key] {
			continue
		}
		seen[key] = true

		addr, err := netip.ParseAddr(m.ResolvedIP)
		if err != nil {
			continue
		}
		loc, ok := e.geo.Lookup(addr)
		if !ok || !loc.HasPosition() {
			continue
		}
		samples[m.Region] = append(samples[m.Region], positionFrom(loc, addr))
	}

	out := make(map[string]score.Position, len(samples))
	for region, group := range samples {
		if p, ok := consensus(group); ok {
			out[region] = p
		}
	}
	return out
}

// publicOrigin locates the current egress address, which is the closest thing
// the monitor has to "where am I" without being told. It is the ISP's view of
// the connection, so for a home line it is usually the right city and
// occasionally the wrong end of the country.
func (e *Engine) publicOrigin() *score.Origin {
	if e.geo == nil {
		return nil
	}
	for _, public := range e.PublicAddresses() {
		if public.IP == "" {
			continue
		}
		addr, err := netip.ParseAddr(public.IP)
		if err != nil {
			continue
		}
		loc, ok := e.geo.Lookup(addr)
		if !ok || !loc.HasPosition() {
			continue
		}
		return &score.Origin{Position: positionFrom(loc, addr), Label: e.siteLabel()}
	}
	return nil
}

func (e *Engine) siteLabel() string {
	if e.cfg == nil {
		return ""
	}
	if e.cfg.Site.Name != "" {
		return e.cfg.Site.Name
	}
	return e.cfg.Site.ID
}

func positionFrom(loc geoip.Location, addr netip.Addr) score.Position {
	return score.Position{
		Latitude:    *loc.Latitude,
		Longitude:   *loc.Longitude,
		Source:      score.PositionSourceGeoIP,
		City:        loc.City,
		Country:     loc.Country,
		CountryName: loc.CountryName,
		Confidence:  string(loc.Confidence),
		IP:          addr.String(),
	}
}

// consensus picks one position out of several belonging to the same region.
//
// Taking the mean would be wrong: two endpoints in Frankfurt and one record
// pointing at the block owner's head office in Seattle would average out to the
// middle of the Atlantic, which is a place no packet went. Instead the
// positions vote by city, the largest group wins, and ties go to the
// better-graded record. Samples are ordered by address first so the answer does
// not depend on map iteration order.
func consensus(samples []score.Position) (score.Position, bool) {
	if len(samples) == 0 {
		return score.Position{}, false
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].IP < samples[j].IP })

	type group struct {
		best  score.Position
		count int
		rank  int
	}
	groups := map[string]*group{}
	var order []string
	for _, s := range samples {
		key := s.City + "|" + s.Country
		g, ok := groups[key]
		if !ok {
			groups[key] = &group{best: s, count: 1, rank: confidenceRank(s.Confidence)}
			order = append(order, key)
			continue
		}
		g.count++
		if rank := confidenceRank(s.Confidence); rank > g.rank {
			g.best, g.rank = s, rank
		}
	}

	winner := groups[order[0]]
	for _, key := range order[1:] {
		g := groups[key]
		if g.count > winner.count || (g.count == winner.count && g.rank > winner.rank) {
			winner = g
		}
	}
	return winner.best, true
}

func confidenceRank(confidence string) int {
	switch geoip.Confidence(confidence) {
	case geoip.ConfidenceHigh:
		return 3
	case geoip.ConfidenceMedium:
		return 2
	case geoip.ConfidenceLow:
		return 1
	default:
		return 0
	}
}
