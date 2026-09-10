// Package geoip resolves approximate physical locations for IP addresses from
// a local MaxMind-format database.
//
// Accuracy deserves scepticism, especially for the intermediate hops of a
// traceroute. GeoIP infers position from registry and routing data, not from
// any signal the address itself carries, and backbone routers frequently
// resolve to the address block owner's registered office rather than to where
// the hardware actually sits. Every result therefore carries a Confidence, and
// callers are expected to present low-confidence positions differently rather
// than drawing them as fact.
package geoip

import (
	"fmt"
	"net/netip"
	"sort"

	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

// Confidence describes how much weight a position deserves.
type Confidence string

const (
	// ConfidenceHigh is a city-level answer with a tight accuracy radius.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium is a city-level answer with a loose radius, or a
	// city with no radius reported at all.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow is effectively a country centroid: the country is known
	// but the position within it is not.
	ConfidenceLow Confidence = "low"
)

// Location is what is known about an address. Latitude and Longitude are nil
// when the database had no position for it, in which case the remaining fields
// may still be populated.
type Location struct {
	Latitude    *float64   `json:"latitude,omitempty"`
	Longitude   *float64   `json:"longitude,omitempty"`
	City        string     `json:"city,omitempty"`
	Country     string     `json:"country,omitempty"`
	CountryName string     `json:"country_name,omitempty"`
	ASN         uint       `json:"asn,omitempty"`
	Org         string     `json:"org,omitempty"`
	Confidence  Confidence `json:"confidence,omitempty"`
}

// HasPosition reports whether the location can be plotted.
func (l Location) HasPosition() bool { return l.Latitude != nil && l.Longitude != nil }

type cityRecord struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Location struct {
		Latitude       *float64 `maxminddb:"latitude"`
		Longitude      *float64 `maxminddb:"longitude"`
		AccuracyRadius uint16   `maxminddb:"accuracy_radius"`
	} `maxminddb:"location"`
}

type asnRecord struct {
	Number uint   `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// Resolver looks up addresses in the configured databases. A nil *Resolver is
// usable and always reports "not found", so callers do not need to branch on
// whether GeoIP is configured.
type Resolver struct {
	city *maxminddb.Reader
	asn  *maxminddb.Reader
}

// Open loads the databases. Both paths are optional: pass "" to skip one. The
// city database supplies position, the ASN database supplies network ownership;
// MaxMind and DB-IP both ship these separately. Open returns a nil Resolver
// when neither path is set.
func Open(cityPath, asnPath string) (*Resolver, error) {
	if cityPath == "" && asnPath == "" {
		return nil, nil
	}
	r := &Resolver{}
	if cityPath != "" {
		db, err := maxminddb.Open(cityPath)
		if err != nil {
			return nil, fmt.Errorf("open city database %s: %w", cityPath, err)
		}
		r.city = db
	}
	if asnPath != "" {
		db, err := maxminddb.Open(asnPath)
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("open asn database %s: %w", asnPath, err)
		}
		r.asn = db
	}
	return r, nil
}

// Close releases the open databases.
func (r *Resolver) Close() error {
	if r == nil {
		return nil
	}
	var err error
	if r.city != nil {
		err = r.city.Close()
		r.city = nil
	}
	if r.asn != nil {
		if cerr := r.asn.Close(); err == nil {
			err = cerr
		}
		r.asn = nil
	}
	return err
}

// Locatable reports whether an address is worth looking up at all. Addresses
// inside the local network or a carrier's NAT range have no public location,
// and asking about them only produces misleading answers.
func Locatable(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() ||
		addr.IsPrivate() || addr.IsMulticast() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return false
	}
	// 100.64.0.0/10, carrier-grade NAT. Go does not count this as private
	// because it is public address space, but it is still the ISP's internal
	// plumbing and never has a meaningful position.
	if addr.Is4() && addr.As4()[0] == 100 {
		if second := addr.As4()[1]; second >= 64 && second <= 127 {
			return false
		}
	}
	return true
}

// Lookup resolves an address. The second return is false when the address is
// not locatable, no database is loaded, or nothing was found.
func (r *Resolver) Lookup(addr netip.Addr) (Location, bool) {
	if r == nil || !Locatable(addr) {
		return Location{}, false
	}
	// A v4-mapped v6 address (::ffff:1.2.3.4) has to be unmapped first or the
	// lookup misses the IPv4 tree entirely.
	addr = addr.Unmap()

	var loc Location
	var found bool

	if r.city != nil {
		res := r.city.Lookup(addr)
		if res.Found() {
			var rec cityRecord
			if err := res.Decode(&rec); err == nil {
				found = true
				loc.Latitude = rec.Location.Latitude
				loc.Longitude = rec.Location.Longitude
				loc.City = pickName(rec.City.Names)
				loc.Country = rec.Country.ISOCode
				loc.CountryName = pickName(rec.Country.Names)
				loc.Confidence = confidenceFor(loc.City, rec.Location.AccuracyRadius)
			}
		}
	}

	if r.asn != nil {
		res := r.asn.Lookup(addr)
		if res.Found() {
			var rec asnRecord
			if err := res.Decode(&rec); err == nil {
				found = true
				loc.ASN = rec.Number
				loc.Org = rec.Org
			}
		}
	}

	return loc, found
}

// confidenceFor grades a position. accuracyRadius is in kilometres and is 0
// when the database does not report one, which the lite databases often do not.
func confidenceFor(city string, accuracyRadius uint16) Confidence {
	if city == "" {
		// No city means the position is the country's centre of mass, which
		// says nothing about where inside the country the address is.
		return ConfidenceLow
	}
	switch {
	case accuracyRadius == 0:
		return ConfidenceMedium
	case accuracyRadius <= 100:
		return ConfidenceHigh
	case accuracyRadius <= 500:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// pickName prefers the English name, falling back to the alphabetically first
// language so that repeated lookups of the same address agree with each other.
func pickName(names map[string]string) string {
	if n, ok := names["en"]; ok {
		return n
	}
	langs := make([]string, 0, len(names))
	for lang := range names {
		langs = append(langs, lang)
	}
	if len(langs) == 0 {
		return ""
	}
	sort.Strings(langs)
	return names[langs[0]]
}
