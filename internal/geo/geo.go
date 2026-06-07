// Package geo determines country/city/region by IP.
//
// The resolver is abstracted by the Resolver interface: the production
// implementation is MMDB (MaxMind .mmdb), and Nop is used when the geo database
// is not configured. Values are normalized: lower case, missing = "-".
package geo

import (
	"net/netip"
	"strings"
)

// Empty — the "no data" value.
const Empty = "-"

// Geo — the geolocation result.
type Geo struct {
	Country string // country ISO code, lower-case, or "-"
	City    string // English city name, lower-case, or "-"
	Region  string // region ISO code, lower-case, or "-"
}

// Resolver determines geo data by IP. The implementation must be thread-safe.
type Resolver interface {
	Resolve(ip netip.Addr) Geo
}

// Nop — a stub resolver: always returns empty values.
// Used when .mmdb is not set (geo filtering simply won't trigger).
type Nop struct{}

func (Nop) Resolve(netip.Addr) Geo { return Geo{Country: Empty, City: Empty, Region: Empty} }

// norm lower-cases the value; empty → Empty.
func norm(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return Empty
	}
	return strings.ToLower(s)
}

// record — the subset of the GeoLite2-City schema needed by the engine.
type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"subdivisions"`
}

// fromRecord converts an mmdb record into Geo (pure function, tested without a DB).
func fromRecord(r record) Geo {
	region := ""
	if n := len(r.Subdivisions); n > 0 {
		region = r.Subdivisions[n-1].ISOCode // last subdivision = most specific
	}
	return Geo{
		Country: norm(r.Country.ISOCode),
		City:    norm(r.City.Names["en"]),
		Region:  norm(region),
	}
}
