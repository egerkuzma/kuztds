// Package geo determines what an IP address tells about a visitor: country,
// city, region, time zone, and the network it belongs to (ASN + organization).
//
// The resolver is abstracted by the Resolver interface: the production
// implementation is DB (MaxMind-format .mmdb files — GeoLite2/GeoIP2 City or
// Country plus, optionally, ASN), and Nop is used when no database is
// configured. Values are normalized: lower case, missing = "-".
package geo

import (
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // zone names must resolve in an image that ships no zoneinfo
)

// Empty — the "no data" value.
const Empty = "-"

// Geo — the result of a lookup.
type Geo struct {
	Country string // country ISO code, lower-case, or "-"
	City    string // English city name, lower-case, or "-"
	Region  string // region ISO code, lower-case, or "-"

	// Timezone is the IANA name as the database spells it ("Europe/Moscow"),
	// or "-". UTCOffset turns it into the "+3" / "+5:30" form filters use.
	Timezone string

	// ASN is the autonomous system number, 0 when unknown. Org is its
	// organization as the database spells it ("Google LLC"), or "-".
	ASN uint32
	Org string
}

// None is the answer for "nothing known about this address".
func None() Geo {
	return Geo{Country: Empty, City: Empty, Region: Empty, Timezone: Empty, Org: Empty}
}

// Resolver determines geo data by IP. The implementation must be thread-safe.
type Resolver interface {
	Resolve(ip netip.Addr) Geo
}

// Nop — a stub resolver: always returns empty values.
// Used when no .mmdb is set (geo filtering simply won't trigger).
type Nop struct{}

func (Nop) Resolve(netip.Addr) Geo { return None() }

// norm lower-cases the value; empty → Empty.
func norm(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return Empty
	}
	return strings.ToLower(s)
}

// keep trims the value and keeps its case; empty → Empty.
func keep(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return Empty
	}
	return s
}

// record — the subset of the GeoLite2/GeoIP2 City schema the engine needs.
// A Country database carries the same "country" field and nothing else, so
// the same struct reads it and the rest stays empty.
type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names names `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		ISOCode string `maxminddb:"iso_code"`
		Names   names  `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	Location struct {
		TimeZone string `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

// names picks the English name out of a localized-names map. Decoding into a
// struct reads that one entry; decoding into a map would allocate the map and
// a string for every language of every lookup.
type names struct {
	En string `maxminddb:"en"`
}

// asnRecord — the GeoLite2-ASN schema (DB-IP's ASN Lite uses the same names).
type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// fromRecord converts an mmdb record into Geo (pure function, tested without a DB).
func fromRecord(r record) Geo {
	region := ""
	if n := len(r.Subdivisions); n > 0 {
		// The last subdivision is the most specific. Databases without ISO
		// codes for subdivisions (DB-IP Lite) still name them.
		if region = r.Subdivisions[n-1].ISOCode; region == "" {
			region = r.Subdivisions[n-1].Names.En
		}
	}
	return Geo{
		Country:  norm(r.Country.ISOCode),
		City:     norm(r.City.Names.En),
		Region:   norm(region),
		Timezone: keep(r.Location.TimeZone),
		Org:      Empty,
	}
}

// locations caches parsed time zones: time.LoadLocation re-reads and re-parses
// the zone file on every call, and the set of zones is small and fixed.
var locations sync.Map // string -> *time.Location (nil when the name is unknown)

func location(name string) *time.Location {
	if v, ok := locations.Load(name); ok {
		loc, _ := v.(*time.Location)
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = nil
	}
	locations.Store(name, loc)
	return loc
}

// UTCOffset returns the visitor's offset from UTC at the given moment in the
// form filters and macros use: "+3", "-8", "+5:30", "+0". Empty when the time
// zone is unknown. The moment matters: the same zone is "+1" in winter and
// "+2" in summer.
func (g Geo) UTCOffset(now time.Time) string {
	if g.Timezone == "" || g.Timezone == Empty {
		return Empty
	}
	loc := location(g.Timezone)
	if loc == nil {
		return Empty
	}
	_, sec := now.In(loc).Zone()
	return formatOffset(sec)
}

func formatOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	h, m := sec/3600, sec%3600/60
	if m == 0 {
		return sign + strconv.Itoa(h)
	}
	return sign + strconv.Itoa(h) + ":" + twoDigits(m)
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// NormOffset brings an operator-written offset to the canonical form of
// UTCOffset: "3", "+3", "+03", "+03:00", "UTC+3", "GMT+3" all become "+3";
// "5:30" and "+0530" become "+5:30". ok is false when s is not an offset.
func NormOffset(s string) (string, bool) {
	s = strings.ReplaceAll(s, "\u2212", "-") // a typographic minus
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "UTC"), "GMT")
	s = strings.TrimSpace(s)
	if s == "" || s == "Z" {
		return "+0", true
	}
	sign := 1
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign, s = -1, s[1:]
	}
	hs, ms, hasColon := strings.Cut(s, ":")
	if !hasColon && len(hs) == 4 { // "0530"
		hs, ms = hs[:2], hs[2:]
	}
	h, err := strconv.Atoi(hs)
	if err != nil || h < 0 || h > 14 {
		return "", false
	}
	m := 0
	if ms != "" {
		if m, err = strconv.Atoi(ms); err != nil || m < 0 || m > 59 {
			return "", false
		}
	}
	return formatOffset(sign * (h*3600 + m*60)), true
}
