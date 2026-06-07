package router

import (
	"net/netip"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

// fakeIP is a simple IPLister implementation for tests.
type fakeIP map[string]map[string]bool // list -> ip -> in

func (f fakeIP) Lookup(name string, ip netip.Addr) (string, bool) {
	if m, ok := f[name]; ok && m[ip.String()] {
		return "", true
	}
	return "", false
}

func list(flag config.Flag, raw string, values ...string) config.ListFilter {
	return config.ListFilter{Flag: flag, Raw: raw, Values: values}
}

// stream helper with the given rules.
func stream(name string, r config.Rules) config.Stream {
	return config.Stream{Name: name, Status: true, Rules: r}
}

func baseVisitor() Visitor {
	return Visitor{
		Lang: "ru", Country: "ru", City: "moscow", Region: "ru-mow",
		UA: "Mozilla/5.0 Firefox/120.0", Referer: "https://ya.ru/",
		Domain: "ya.ru", Key: "купить телефон", Device: "computer",
		Operator: Empty, Unique: true, IP: netip.MustParseAddr("203.0.113.5"),
	}
}

func TestSelect_FirstMatchWins(t *testing.T) {
	g := &config.Group{Streams: []config.Stream{
		{Name: "disabled", Status: false},                        // disabled — skip
		stream("only_phones", config.Rules{Phone: config.FlagA}), // block phones — does comp pass? yes, this blocks phone, comp ok -> match!
	}}
	// For a computer visitor the "only_phones" stream (block phone) does NOT cut computer → it is selected.
	s, ok := Select(g, baseVisitor(), Deps{})
	if !ok || s.Name != "only_phones" {
		t.Fatalf("expected only_phones, got %v ok=%v", s, ok)
	}
}

func TestSelect_LangWhitelistBlacklist(t *testing.T) {
	v := baseVisitor() // lang=ru
	// whitelist en -> ru is cut
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{Lang: list(config.FlagB, "en")})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("whitelist 'en' must cut ru")
	}
	// whitelist ru -> passes
	g = &config.Group{Streams: []config.Stream{stream("s", config.Rules{Lang: list(config.FlagB, "ru,en")})}}
	if _, ok := Select(g, v, Deps{}); !ok {
		t.Error("whitelist 'ru,en' must let ru through")
	}
	// blacklist ru -> is cut
	g = &config.Group{Streams: []config.Stream{stream("s", config.Rules{Lang: list(config.FlagA, "ru")})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("blacklist 'ru' must cut ru")
	}
}

func TestSelect_CityRegionExact(t *testing.T) {
	v := baseVisitor() // city=moscow
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{City: list(config.FlagB, "", "Moscow", "Kazan")})}}
	if _, ok := Select(g, v, Deps{}); !ok {
		t.Error("city whitelist must let moscow through (case-insensitive)")
	}
	// a partial match must not trigger (exact comparison)
	g = &config.Group{Streams: []config.Stream{stream("s", config.Rules{City: list(config.FlagB, "", "mosc")})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("partial 'mosc' must not match the exact city filter")
	}
}

func TestSelect_UARegexAndSubstring(t *testing.T) {
	v := baseVisitor()
	v.UA = "Mozilla/5.0 (X11; Linux) curl/8.0"
	// substring
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{UAText: list(config.FlagA, "curl", "curl", "wget")})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("blacklist substring 'curl' must cut")
	}
	// regex with the i flag
	g = &config.Group{Streams: []config.Stream{stream("s", config.Rules{UAText: list(config.FlagA, "/CURL\\/[0-9]+/i")})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("blacklist regex '/CURL\\/[0-9]+/i' must cut")
	}
}

func TestSelect_Device(t *testing.T) {
	v := baseVisitor() // computer
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{Computer: config.FlagA})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("block computer must cut the desktop")
	}
	v.Device = "phone"
	if _, ok := Select(g, v, Deps{}); !ok {
		t.Error("block computer must not affect phone")
	}
}

func TestSelect_OperatorRequireAndBlock(t *testing.T) {
	v := baseVisitor()
	v.Operator = Empty
	// an operator is required (FlagB) -> cut a visitor without an operator
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{Operators: map[string]config.Flag{"beeline": config.FlagB}})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("requiring an operator must cut a visitor without an operator")
	}
	// has the beeline operator -> passes
	v.Operator = "beeline"
	if _, ok := Select(g, v, Deps{}); !ok {
		t.Error("a visitor with an operator must pass")
	}
	// block mts (FlagA) -> the mts visitor is cut
	v.Operator = "mts"
	g = &config.Group{Streams: []config.Stream{stream("s", config.Rules{Operators: map[string]config.Flag{"mts": config.FlagA}})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("block mts must cut mts")
	}
}

func TestSelect_UniqueRefererYaBrowser(t *testing.T) {
	v := baseVisitor()
	// non-unique only (FlagB), but the visitor is unique -> cut
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{Unique: config.FlagB})}}, v, Deps{}); ok {
		t.Error("non-unique-only must cut a unique visitor")
	}
	// require no referer (FlagB), but a referer is present -> cut
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{HasReferer: config.FlagB})}}, v, Deps{}); ok {
		t.Error("require-no-referer must cut a visitor with a referer")
	}
	// YaBrowser only (FlagB), UA without YaBrowser -> cut
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{YaBrowser: config.FlagB})}}, v, Deps{}); ok {
		t.Error("only-yabrowser must cut a regular UA")
	}
	v.UA = "Mozilla/5.0 YaBrowser/23.0"
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{YaBrowser: config.FlagB})}}, v, Deps{}); !ok {
		t.Error("only-yabrowser must let YaBrowser through")
	}
}

func TestSelect_IPList(t *testing.T) {
	v := baseVisitor() // IP 203.0.113.5
	ip := fakeIP{"whitelist": {"203.0.113.5": true}}
	// whitelist (FlagB): IP in the list -> passes
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{IPList: config.IPListFilter{Flag: config.FlagB, File: "whitelist.dat"}})}}
	if _, ok := Select(g, v, Deps{IP: ip}); !ok {
		t.Error("IP from the whitelist must pass")
	}
	// blacklist (FlagA): IP in the list -> cut
	g = &config.Group{Streams: []config.Stream{stream("s", config.Rules{IPList: config.IPListFilter{Flag: config.FlagA, File: "whitelist.dat"}})}}
	if _, ok := Select(g, v, Deps{IP: ip}); ok {
		t.Error("IP from the blacklist must be cut")
	}
}

// fakeLimiter always denies.
type denyLimiter struct{}

func (denyLimiter) Allowed(string, config.LimitRule) bool { return false }

func TestSelect_Limit(t *testing.T) {
	v := baseVisitor()
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{Limit: config.LimitRule{Enabled: true, Type: 1, Count: 100}})}}
	if _, ok := Select(g, v, Deps{Limiter: denyLimiter{}}); ok {
		t.Error("an exhausted limit must cut the stream")
	}
	// without a limiter the limit is skipped
	if _, ok := Select(g, v, Deps{}); !ok {
		t.Error("without Limiter the limit must not interfere")
	}
}

func TestSelect_OSBrowserBrand(t *testing.T) {
	v := baseVisitor()
	v.OS, v.OSVersion = "Android", "13"
	v.Browser, v.BrowserVer = "Chrome", "120.0"
	v.Brand = "Samsung"

	// whitelist OS Android → passes; iOS → no
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{OS: list(config.FlagB, "", "Android")})}}, v, Deps{}); !ok {
		t.Error("whitelist OS Android must let through")
	}
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{OS: list(config.FlagB, "", "iOS")})}}, v, Deps{}); ok {
		t.Error("whitelist OS iOS must cut Android")
	}
	// OS version: "Android 13" matches, "Android 12" — does not
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{OS: list(config.FlagB, "", "Android 12")})}}, v, Deps{}); ok {
		t.Error("OS Android 12 must not match Android 13")
	}
	// blacklist browser Chrome → cut
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{Browser: list(config.FlagA, "", "Chrome")})}}, v, Deps{}); ok {
		t.Error("blacklist Chrome must cut")
	}
	// brand whitelist Samsung → passes; Apple → no
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{Brand: list(config.FlagB, "", "Samsung")})}}, v, Deps{}); !ok {
		t.Error("brand whitelist Samsung must let through")
	}
	if _, ok := Select(&config.Group{Streams: []config.Stream{stream("s", config.Rules{Brand: list(config.FlagB, "", "Apple")})}}, v, Deps{}); ok {
		t.Error("brand whitelist Apple must cut Samsung")
	}
}

func TestSelect_Schedule(t *testing.T) {
	v := baseVisitor()
	base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	next := base.AddDate(0, 0, 1) // adjacent day — a different day of week

	// allow only base's day of week
	var days [7]bool
	days[int(base.Weekday())] = true
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{
		Schedule: config.Schedule{Enabled: true, Days: days}})}}

	if _, ok := Select(g, v, Deps{Now: func() time.Time { return base }}); !ok {
		t.Error("on an allowed day the stream must work")
	}
	if _, ok := Select(g, v, Deps{Now: func() time.Time { return next }}); ok {
		t.Error("on a disallowed day the stream must not work")
	}
}

// Regression: a filter specified only via values (without raw) — must work
// (a JSON config may not contain raw).
func TestSelect_ValuesOnlyFilter(t *testing.T) {
	v := baseVisitor() // country=ru
	// country whitelist [us] values-only → ru is cut
	g := &config.Group{Streams: []config.Stream{stream("s", config.Rules{
		Country: config.ListFilter{Flag: config.FlagB, Values: []string{"us"}}})}}
	if _, ok := Select(g, v, Deps{}); ok {
		t.Error("country whitelist [us] (values-only) must cut ru")
	}
	// country whitelist [ru] values-only → passes
	g = &config.Group{Streams: []config.Stream{stream("s", config.Rules{
		Country: config.ListFilter{Flag: config.FlagB, Values: []string{"ru", "by"}}})}}
	if _, ok := Select(g, v, Deps{}); !ok {
		t.Error("country whitelist [ru,by] (values-only) must let ru through")
	}
}

func TestSelect_NoStreamMatches(t *testing.T) {
	v := baseVisitor()
	g := &config.Group{Streams: []config.Stream{
		stream("a", config.Rules{Lang: list(config.FlagB, "en")}),    // needs en
		stream("b", config.Rules{Country: list(config.FlagB, "us")}), // needs us
	}}
	if s, ok := Select(g, v, Deps{}); ok {
		t.Errorf("no stream must match, got %v", s)
	}
}
