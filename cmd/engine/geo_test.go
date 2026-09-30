package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/geo"
)

// fixedGeo answers the same record for every address.
type fixedGeo geo.Geo

func (f fixedGeo) Resolve(netip.Addr) geo.Geo { return geo.Geo(f) }

func withGeo(g geo.Geo) func(*engineDeps) {
	return func(d *engineDeps) { d.geores = fixedGeo(g) }
}

func geoGroup(source string, streams ...config.Stream) *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "g1", Name: "g1", Status: true, Redirect: "show_text", Out: "default c=[COUNTRY]",
		Geo: source, UniqSeconds: 86400, Streams: streams,
	})
}

// Which source wins when the database and the CDN header disagree is the
// group's choice; whichever loses is still the fallback when the winner has
// no answer.
func TestCountrySourcePrecedence(t *testing.T) {
	db := geo.None()
	db.Country = "de"
	hdr := map[string]string{"User-Agent": uaWinChrome, "CF-IPCountry": "FR"}
	noHdr := map[string]string{"User-Agent": uaWinChrome}

	cases := []struct {
		source string
		geo    geo.Geo
		hdr    map[string]string
		want   string
	}{
		{"cf", db, hdr, "fr"},          // header first
		{"cf", db, noHdr, "de"},        // no header → database
		{"db", db, hdr, "de"},          // database first
		{"sypex", db, hdr, "de"},       // legacy name for the same thing
		{"", db, hdr, "de"},            // unset behaves as database-first
		{"db", geo.None(), hdr, "fr"},  // database silent → header
		{"db", geo.None(), noHdr, "-"}, // nobody knows
		{"cf", geo.None(), noHdr, "-"},
	}
	for _, c := range cases {
		h := testEnv(t, geoGroup(c.source), withGeo(c.geo))
		rec := do(t, h, "/g1", "8.8.8.8", c.hdr)
		if got := rec.Header().Get("X-Kuztds-Country"); got != c.want {
			t.Errorf("geo=%q db=%q header=%q → %q; want %q", c.source, c.geo.Country, c.hdr["CF-IPCountry"], got, c.want)
		}
	}
}

// CF-IPCountry from a peer that is not a trusted proxy is text the visitor
// typed. Believing it lets anyone pick the stream their country is not
// supposed to get.
func TestCountryHeaderIgnoredFromUntrustedPeer(t *testing.T) {
	ru := config.Stream{Name: "ru_only", Status: true,
		Rules: config.Rules{Country: config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}, Raw: "ru"}},
		Out:   config.Output{Redirect: "show_text", Out: "RU OFFER"}}
	h := testEnv(t, geoGroup("cf", ru))

	req := httptest.NewRequest(http.MethodGet, "/g1", nil)
	req.RemoteAddr = "203.0.113.7:4711" // a direct visitor, not our proxy
	req.Header.Set("User-Agent", uaWinChrome)
	req.Header.Set("CF-IPCountry", "RU")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Kuztds-Stream"); got == "ru_only" {
		t.Fatalf("a visitor named their own country and got stream %q (body %q)", got, rec.Body.String())
	}
	if got := rec.Header().Get("X-Kuztds-Country"); got != "-" {
		t.Errorf("country = %q; want unknown", got)
	}

	// The same header through the trusted proxy is the CDN speaking.
	rec = do(t, h, "/g1", "203.0.113.7", map[string]string{"User-Agent": uaWinChrome, "CF-IPCountry": "RU"})
	if got := rec.Header().Get("X-Kuztds-Stream"); got != "ru_only" {
		t.Errorf("through the trusted proxy: stream %q; want ru_only", got)
	}
}

// ASN, organization and time zone reach the router and the macros.
func TestNetworkFiltersAndMacros(t *testing.T) {
	g := geo.Geo{Country: "in", City: "delhi", Region: "dl", Timezone: "Asia/Kolkata", ASN: 55836, Org: "Reliance Jio Infocomm Limited"}
	inc := func(vals ...string) config.ListFilter {
		return config.ListFilter{Flag: config.FlagB, Values: vals, Raw: vals[0]}
	}
	streams := []config.Stream{
		{Name: "hosting", Status: true, Rules: config.Rules{Org: inc("/amazon|google|hetzner/i")},
			Out: config.Output{Redirect: "show_text", Out: "DC"}},
		{Name: "jio_india", Status: true, Rules: config.Rules{ASN: inc("AS55836"), Timezone: inc("+5:30")},
			Out: config.Output{Redirect: "show_text", Out: "as=[ASN] org=[ORG] tz=[TIMEZONE] utc=[UTC]"}},
	}
	h := testEnv(t, geoGroup("db", streams...), withGeo(g))
	rec := do(t, h, "/g1", "49.36.0.1", map[string]string{"User-Agent": uaWinChrome})
	if got := rec.Header().Get("X-Kuztds-Stream"); got != "jio_india" {
		t.Fatalf("stream = %q; want jio_india", got)
	}
	want := "as=55836 org=Reliance+Jio+Infocomm+Limited tz=Asia/Kolkata utc=+5:30"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q; want %q", got, want)
	}

	// Without any geo data the macros are empty, not "0" or "-" in a URL.
	h = testEnv(t, geoGroup("db", config.Stream{Name: "any", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "as=[ASN]&org=[ORG]"}}))
	if got := do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}).Body.String(); got != "as=&org=" {
		t.Errorf("no geo: %q; want empty values", got)
	}
}

// The time-zone filter sees the offset as of now, so a zone with daylight
// saving matches the right value in both halves of the year.
func TestOffsetFollowsTheCalendar(t *testing.T) {
	g := geo.None()
	g.Timezone = "America/New_York"
	now := g.UTCOffset(time.Now())
	if now != "-5" && now != "-4" {
		t.Fatalf("New York offset = %q", now)
	}
	s := config.Stream{Name: "ny", Status: true,
		Rules: config.Rules{Timezone: config.ListFilter{Flag: config.FlagB, Values: []string{"-5", "-4"}}},
		Out:   config.Output{Redirect: "show_text", Out: "NY"}}
	h := testEnv(t, geoGroup("db", s), withGeo(g))
	if got := do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}).Body.String(); got != "NY" {
		t.Errorf("body = %q", got)
	}
}

// A query-string variable routes, and a stream can set its own Content-Type.
func TestQueryFilterAndStreamHeader(t *testing.T) {
	s := config.Stream{Name: "fb", Status: true,
		Rules: config.Rules{Query: config.ListFilter{Flag: config.FlagB, Values: []string{"utm_source=fb"}}},
		Out:   config.Output{Redirect: "show_text", Out: "FB", Header: "text/plain"}}
	h := testEnv(t, geoGroup("db", s))
	hd := map[string]string{"User-Agent": uaWinChrome}
	rec := do(t, h, "/g1?utm_source=fb", "8.8.8.8", hd)
	if rec.Body.String() != "FB" {
		t.Errorf("with the variable: %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("stream Content-Type = %q; want text/plain", ct)
	}
	if got := do(t, h, "/g1?utm_source=tt", "8.8.8.8", hd).Header().Get("X-Kuztds-Stream"); got != "-" {
		t.Errorf("another value must fall through, got stream %q", got)
	}
}
