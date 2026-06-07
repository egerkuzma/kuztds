package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/fetch"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/server"
	"github.com/egerkuzma/kuztds/internal/store"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// GLOBAL END-TO-END ENGINE TESTING
//
// We build a set of groups/streams covering ALL configuration fields, and
// run varied traffic through the full pipeline (engineDeps.root),
// checking routing, filters, bots, rendering of all redirect types, macros,
// uniqueness, limits, firewall, distribution, separation, scheduling, geo,
// operators, and api mode. Some tests use miniredis and a test mmdb.
// ============================================================================

// Known-good UAs (expectations cross-checked with internal/detect/parse_test.go).
const (
	uaIPhone    = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
	uaSamsung   = "Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36"
	uaXiaomi    = "Mozilla/5.0 (Linux; Android 12; Redmi Note 10) AppleWebKit/537.36 Chrome/119.0 Mobile Safari/537.36"
	uaPixel     = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/121.0 Mobile Safari/537.36"
	uaWinFF     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0"
	uaWinChrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	uaIPad      = "Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
	uaYandex    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 YaBrowser/23.9.0 Safari/537.36"
	uaGoogleBot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
)

// engHarness holds the engine sandbox settings.
type engHarness struct {
	files     map[string]string // name.dat -> contents (in dataDir)
	redis     bool              // attach miniredis (counters)
	geoMMDB   bool              // attach the test mmdb
	trashMode string
	trashURL  string
}

func newEng(t *testing.T, groups *config.Groups, h engHarness) http.Handler {
	t.Helper()
	dir := t.TempDir()
	for name, content := range h.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	lists.Load(ipListFiles(groups)...) // custom ip_list files from the groups
	sigs := detect.NewSignatures(dir, log)
	d := &engineDeps{
		log: log, lists: lists, sigs: sigs, geores: geo.Nop{}, groups: groups,
		dataDir: dir, keysDir: dir, postbackKey: "pb", apiKey: "k",
		fetcher: fetch.New(""), trashMode: h.trashMode, trashURL: h.trashURL,
	}
	if d.trashMode == "" {
		d.trashMode = "0"
	}
	if h.geoMMDB {
		if m, err := geo.OpenMMDB("../../internal/geo/testdata/GeoLite2-City-Test.mmdb"); err == nil {
			d.geores = m
		} else {
			t.Logf("mmdb not opened (%v) — mmdb geo checks skipped", err)
		}
	}
	if h.redis {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mr.Close)
		d.counters = store.NewCounters(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	}
	realIP, err := server.NewRealIP([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.root)
	return realIP.Middleware(mux)
}

// ---- assertions ----

func wantStream(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	if got := rec.Header().Get("X-Kuztds-Stream"); got != name {
		t.Errorf("stream = %q, expected %q (bot=%s, country=%s)", got, name, rec.Header().Get("X-Kuztds-Bot"), rec.Header().Get("X-Kuztds-Country"))
	}
}
func wantCode(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Errorf("code = %d, expected %d", rec.Code, code)
	}
}
func wantBody(t *testing.T, rec *httptest.ResponseRecorder, sub string) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), sub) {
		t.Errorf("body does not contain %q; body=%q", sub, rec.Body.String())
	}
}
func wantHdr(t *testing.T, rec *httptest.ResponseRecorder, h, v string) {
	t.Helper()
	if got := rec.Header().Get(h); got != v {
		t.Errorf("header %s = %q, expected %q", h, got, v)
	}
}

// ============================================================================
// 1. Device-based routing
// ============================================================================

func TestE2E_DeviceRouting(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "mobile", Status: true, Rules: config.Rules{Computer: config.FlagA, Tablet: config.FlagA},
			Out: config.Output{Redirect: "show_text", Out: "M"}},
		{Name: "tablet", Status: true, Rules: config.Rules{Computer: config.FlagA, Phone: config.FlagA},
			Out: config.Output{Redirect: "show_text", Out: "T"}},
		{Name: "desktop", Status: true, Rules: config.Rules{Phone: config.FlagA, Tablet: config.FlagA},
			Out: config.Output{Redirect: "show_text", Out: "D"}},
		{Name: "any", Status: true, Out: config.Output{Redirect: "show_text", Out: "A"}},
	}})
	h := newEng(t, gg, engHarness{})
	cases := []struct{ ua, stream, body string }{
		{uaIPhone, "mobile", "M"}, {uaSamsung, "mobile", "M"},
		{uaIPad, "tablet", "T"}, {uaWinChrome, "desktop", "D"}, {uaWinFF, "desktop", "D"},
	}
	for _, c := range cases {
		rec := do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": c.ua})
		wantStream(t, rec, c.stream)
		wantBody(t, rec, c.body)
	}
}

// ============================================================================
// 2. Geo: country whitelist/blacklist (via CF-IPCountry) + mmdb (country/city)
// ============================================================================

func TestE2E_GeoRouting(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "ru_only", Status: true, Rules: config.Rules{Country: config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}}},
			Out: config.Output{Redirect: "show_text", Out: "RU"}},
		{Name: "no_us", Status: true, Rules: config.Rules{Country: config.ListFilter{Flag: config.FlagA, Values: []string{"us"}}},
			Out: config.Output{Redirect: "show_text", Out: "NOTUS"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
	}})
	h := newEng(t, gg, engHarness{})
	// RU → ru_only
	wantBody(t, do(t, h, "/g", "8.8.8.8", map[string]string{"CF-IPCountry": "RU", "User-Agent": uaWinChrome}), "RU")
	// DE → not ru_only (RU whitelist), not blocked (no_us lets it through) → no_us
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"CF-IPCountry": "DE", "User-Agent": uaWinChrome}), "no_us")
	// US → not ru_only, no_us blocks us → rest
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"CF-IPCountry": "US", "User-Agent": uaWinChrome}), "rest")
}

func TestE2E_GeoMMDBCity(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Geo: "sypex", Streams: []config.Stream{
		{Name: "london", Status: true, Rules: config.Rules{
			Country: config.ListFilter{Flag: config.FlagB, Values: []string{"gb"}},
			City:    config.ListFilter{Flag: config.FlagB, Values: []string{"london"}}},
			Out: config.Output{Redirect: "show_text", Out: "LON"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
	}})
	h := newEng(t, gg, engHarness{geoMMDB: true})
	// 81.2.69.142 → gb/london in the test mmdb
	rec := do(t, h, "/g", "81.2.69.142", map[string]string{"User-Agent": uaWinChrome})
	if s := rec.Header().Get("X-Kuztds-Stream"); s != "london" && s != "rest" {
		t.Fatalf("unexpected stream %q", s)
	}
	if rec.Header().Get("X-Kuztds-Stream") == "rest" {
		t.Skip("mmdb unavailable — city geo skipped")
	}
	wantBody(t, rec, "LON")
	wantHdr(t, rec, "X-Kuztds-Country", "gb")
}

// ============================================================================
// 3. OS / Browser / Brand / YaBrowser filters
// ============================================================================

func TestE2E_UAOSBrandFilters(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "android_chrome", Status: true, Rules: config.Rules{
			OS:      config.ListFilter{Flag: config.FlagB, Values: []string{"Android"}},
			Browser: config.ListFilter{Flag: config.FlagB, Values: []string{"Chrome"}}},
			Out: config.Output{Redirect: "show_text", Out: "AC"}},
		{Name: "apple_brand", Status: true, Rules: config.Rules{
			Brand: config.ListFilter{Flag: config.FlagB, Values: []string{"Apple"}}},
			Out: config.Output{Redirect: "show_text", Out: "AP"}},
		{Name: "no_yandex", Status: true, Rules: config.Rules{YaBrowser: config.FlagA},
			Out: config.Output{Redirect: "show_text", Out: "NOYA"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
	}})
	h := newEng(t, gg, engHarness{})
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaSamsung}), "android_chrome")
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaIPhone}), "apple_brand")
	// YaBrowser: not android_chrome (Chrome+Yabrowser on Windows) —
	// OS Windows != Android → not android_chrome; brand "" != Apple → no; no_yandex excludes → rest
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaYandex}), "rest")
	// plain Windows Chrome → no_yandex
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "no_yandex")
}

// ============================================================================
// 4. List filters: lang / referer / domain / key(regex) / ip_list
// ============================================================================

func TestE2E_ListFilters(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "ru_lang", Status: true, Rules: config.Rules{Lang: config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}}},
			Out: config.Output{Redirect: "show_text", Out: "L"}},
		{Name: "need_ref", Status: true, Rules: config.Rules{HasReferer: config.FlagA},
			Out: config.Output{Redirect: "show_text", Out: "R"}},
		{Name: "key_re", Status: true, Rules: config.Rules{Key: config.ListFilter{Flag: config.FlagB, Raw: `/^buy[0-9]+$/`}},
			Out: config.Output{Redirect: "show_text", Out: "K"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
	}})
	h := newEng(t, gg, engHarness{})
	// ru language → ru_lang
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "ru-RU"}), "ru_lang")
	// en + has referer → need_ref
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "en", "Referer": "https://x.example/"}), "need_ref")
	// en, no referer, key buy123 → key_re
	wantStream(t, do(t, h, "/g?q=buy123", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "en"}), "key_re")
	// en, no referer, key does not match → rest
	wantStream(t, do(t, h, "/g?q=hello", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "en"}), "rest")
}

func TestE2E_IPListFilter(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "vip", Status: true, Rules: config.Rules{IPList: config.IPListFilter{Flag: config.FlagB, File: "vip.dat"}},
			Out: config.Output{Redirect: "show_text", Out: "VIP"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
	}})
	h := newEng(t, gg, engHarness{files: map[string]string{"vip.dat": "10.0.0.0/8\n"}})
	wantStream(t, do(t, h, "/g", "10.1.2.3", map[string]string{"User-Agent": uaWinChrome}), "vip")
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "rest")
}

// ============================================================================
// 5. Operators (wap.dat labels)
// ============================================================================

func TestE2E_Operators(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "beeline_only", Status: true, Rules: config.Rules{Operators: map[string]config.Flag{"beeline": config.FlagB}},
			Out: config.Output{Redirect: "show_text", Out: "BEE"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
	}})
	h := newEng(t, gg, engHarness{files: map[string]string{"wap.dat": "# beeline\n5.5.5.5\n# megafon\n6.6.6.6\n"}})
	r1 := do(t, h, "/g", "5.5.5.5", map[string]string{"User-Agent": uaSamsung})
	wantStream(t, r1, "beeline_only")
	wantHdr(t, r1, "X-Kuztds-Stream", "beeline_only")
	wantStream(t, do(t, h, "/g", "6.6.6.6", map[string]string{"User-Agent": uaSamsung}), "rest")
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaSamsung}), "rest")
}

// ============================================================================
// 6. ALL redirect types
// ============================================================================

func TestE2E_AllRedirectTypes(t *testing.T) {
	type tc struct {
		redirect string
		out      string
		code     int
		ctype    string // expected Content-Type substring ("" — do not check)
		loc      string // expected Location ("" — none)
		body     string // expected body substring ("" — do not check)
	}
	cases := []tc{
		{"http_redirect", "https://t.example/x", 302, "", "https://t.example/x", ""},
		{"show_text", "PLAIN", 200, "text/plain", "", "PLAIN"},
		{"javascript", "alert(1)", 200, "javascript", "", "alert(1)"},
		{"js_selection", "https://j.example", 200, "javascript", "", "j.example"},
		{"meta_refresh", "https://m.example", 200, "", "", "m.example"},
		{"js_redirect", "https://jr.example", 200, "", "", "jr.example"},
		{"iframe_redirect", "https://if.example", 200, "", "", "if.example"},
		{"iframe_selection", "https://is.example", 200, "", "", "is.example"},
		{"show_page_html", "<b>hi</b>", 200, "", "", "<b>hi</b>"},
		{"under_construction", "", 200, "", "", "404.png"},
		{"stop", "", 200, "", "", ""},
		{"403_forbidden", "", 403, "", "", "403"},
		{"404_not_found", "", 404, "", "", "404"},
		{"500_server_error", "", 500, "", "", "500"},
		{"api", "https://api.example?k=[KEY]", 200, "json", "", "api.example"},
		{"show_out", "https://so.example", 200, "json", "", "so.example"},
	}
	var groups []*config.Group
	for i, c := range cases {
		groups = append(groups, &config.Group{
			ID: c.redirect, Status: true, Redirect: "stop",
			Streams: []config.Stream{{Name: "s", Status: true, Out: config.Output{Redirect: c.redirect, Out: c.out}}},
		})
		_ = i
	}
	h := newEng(t, config.NewGroups(groups...), engHarness{})
	for _, c := range cases {
		rec := do(t, h, "/"+c.redirect+"?q=KW", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
		if rec.Code != c.code {
			t.Errorf("%s: code=%d, expected %d", c.redirect, rec.Code, c.code)
		}
		if c.loc != "" {
			wantHdr(t, rec, "Location", c.loc)
		}
		if c.ctype != "" && !strings.Contains(rec.Header().Get("Content-Type"), c.ctype) {
			t.Errorf("%s: Content-Type=%q, expected ~%q", c.redirect, rec.Header().Get("Content-Type"), c.ctype)
		}
		if c.body != "" {
			wantBody(t, rec, c.body)
		}
		if c.redirect == "stop" && rec.Body.Len() != 0 {
			t.Errorf("stop: body should be empty, got %q", rec.Body.String())
		}
	}
}

// ============================================================================
// 7. ALL macros
// ============================================================================

func TestE2E_AllMacros(t *testing.T) {
	out := "k=[KEY]|ip=[IP]|c=[COUNTRY]|cc=[()COUNTRY()]|lang=[LANG]|dev=[DEVICE]|" +
		"dom=[DOMAIN]|path=[PATH]|p1=[PAR-1]|p2=[PAR-2]|ua=[USERAGENT]|cid=[CID]|rn=[RANDNUM-7-7]|rs=[RANDSTR-(ab)-4]"
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: out}},
	}})
	h := newEng(t, gg, engHarness{})
	rec := do(t, h, "/g?q=hello%20world&p1=AAA&p2=BBB", "9.9.9.9", map[string]string{
		"User-Agent": uaIPhone, "Accept-Language": "ru-RU", "CF-IPCountry": "RU", "Referer": "https://ads.example.com/land",
	})
	b := rec.Body.String()
	checks := map[string]string{
		"k=hello+world": "[KEY] url-encoded", "ip=9.9.9.9": "[IP]", "c=ru": "[COUNTRY]",
		"cc=ru": "[()COUNTRY()]", "lang=ru": "[LANG]", "dev=phone": "[DEVICE]",
		"dom=ads.example.com": "[DOMAIN]", "path=example.com": "[PATH]",
		"p1=AAA": "[PAR-1]", "p2=BBB": "[PAR-2]", "rn=7": "[RANDNUM-7-7]",
	}
	for sub, what := range checks {
		if !strings.Contains(b, sub) {
			t.Errorf("macro %s not expanded (no %q); body=%q", what, sub, b)
		}
	}
	if !regexp.MustCompile(`cid=[0-9a-f]{10}\b`).MatchString(b) {
		t.Errorf("[CID] does not look like 10 hex; body=%q", b)
	}
	if !regexp.MustCompile(`rs=[ab]{4}\b`).MatchString(b) {
		t.Errorf("[RANDSTR] not from set (ab) of length 4; body=%q", b)
	}
	if !strings.Contains(b, "ua=Mozilla") {
		t.Errorf("[USERAGENT] not expanded; body=%q", b)
	}
}

// ============================================================================
// 8. Bots: all signals + bot_redirect / skip / save_ip
// ============================================================================

func TestE2E_Bots(t *testing.T) {
	files := map[string]string{
		"signature_ua.dat":  "evilbot\n",
		"signature_ref.dat": "spamref\n",
		"ua_blacklist.dat":  "BadUA/1.0\n",
		"ip_google.dat":     "66.249.66.0/24\n",
	}
	mk := func(name string, b config.Bots) *config.Group {
		return &config.Group{ID: name, Status: true, Redirect: "stop", Streams: []config.Stream{
			{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: "HUMAN"}, Bots: b},
		}}
	}
	gg := config.NewGroups(
		mk("empty_ua", config.Bots{EmptyUA: true, Redirect: "404_not_found"}),
		mk("empty_ref", config.Bots{EmptyRef: true, Redirect: "403_forbidden"}),
		mk("empty_lang", config.Bots{EmptyLang: true, Redirect: "404_not_found"}),
		mk("ipv6", config.Bots{IPv6: true, Redirect: "404_not_found"}),
		mk("sign_ua", config.Bots{CheckUA: true, Redirect: "404_not_found"}),
		mk("sign_ref", config.Bots{CheckUA: true, Redirect: "404_not_found"}),
		mk("se_ip", config.Bots{IPGoogle: true, Redirect: "404_not_found"}),
		mk("ua_black", config.Bots{ListUA: true, Redirect: "404_not_found"}),
		mk("skip_bot", config.Bots{EmptyUA: true, Redirect: "skip"}),
	)
	h := newEng(t, gg, engHarness{files: files})

	// empty UA → bot → 404, header bot=empty_ua
	r := do(t, h, "/empty_ua", "8.8.8.8", map[string]string{"User-Agent": ""})
	wantCode(t, r, 404)
	wantHdr(t, r, "X-Kuztds-Bot", "empty_ua")
	// empty referer
	r = do(t, h, "/empty_ref", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	wantCode(t, r, 403)
	wantHdr(t, r, "X-Kuztds-Bot", "empty_ref")
	// empty language (no Accept-Language)
	r = do(t, h, "/empty_lang", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	wantHdr(t, r, "X-Kuztds-Bot", "empty_lang")
	// IPv6
	r = do(t, h, "/ipv6", "2001:4860:4860::8888", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "en"})
	wantHdr(t, r, "X-Kuztds-Bot", "ipv6")
	// UA signature
	r = do(t, h, "/sign_ua", "8.8.8.8", map[string]string{"User-Agent": "Mozilla evilbot crawler", "Accept-Language": "en"})
	wantHdr(t, r, "X-Kuztds-Bot", "sign_ua")
	// referer signature
	r = do(t, h, "/sign_ref", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "en", "Referer": "https://spamref.io/x"})
	wantHdr(t, r, "X-Kuztds-Bot", "sign_ref")
	// search-engine IP
	r = do(t, h, "/se_ip", "66.249.66.10", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "en"})
	wantHdr(t, r, "X-Kuztds-Bot", "google")
	// ua_blacklist (exact match)
	r = do(t, h, "/ua_black", "8.8.8.8", map[string]string{"User-Agent": "BadUA/1.0", "Accept-Language": "en"})
	wantHdr(t, r, "X-Kuztds-Bot", "ua_blacklist")
	// skip → bot is logged, but the normal stream is served
	r = do(t, h, "/skip_bot", "8.8.8.8", map[string]string{"User-Agent": ""})
	wantBody(t, r, "HUMAN")
	wantHdr(t, r, "X-Kuztds-Bot", "empty_ua")
	// a human on any of the groups — HUMAN is served
	r = do(t, h, "/empty_ua", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "Accept-Language": "en"})
	wantBody(t, r, "HUMAN")
	wantHdr(t, r, "X-Kuztds-Bot", "-")
}

func TestE2E_SaveBotIP(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ip_google.dat"), []byte("1.1.1.1\n"), 0o644)
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, log)
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: "X"},
			Bots: config.Bots{CheckUA: true, SaveIP: true, Redirect: "skip"}},
	}})
	d := &engineDeps{log: log, lists: lists, sigs: sigs, geores: geo.Nop{}, groups: gg,
		dataDir: dir, keysDir: dir, fetcher: fetch.New(""), trashMode: "0"}
	realIP, _ := server.NewRealIP([]string{"127.0.0.1/32"})
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.root)
	h := realIP.Middleware(mux)
	// googlebot by UA with a new IP → save_ip appends the IP to ip_google.dat
	do(t, h, "/g", "77.88.99.100", map[string]string{"User-Agent": uaGoogleBot, "Accept-Language": "en"})
	b, _ := os.ReadFile(filepath.Join(dir, "ip_google.dat"))
	if !strings.Contains(string(b), "77.88.99.100") {
		t.Errorf("save_ip did not append the bot IP; file=%q", string(b))
	}
}

// ============================================================================
// 9. out distribution (|||): rotator (cookie), evenly (redis), random
// ============================================================================

func TestE2E_DistributionEvenly(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: "A|||B|||C", Distribution: "evenly"}},
	}})
	h := newEng(t, gg, engHarness{redis: true})
	got := []string{}
	for i := 0; i < 6; i++ {
		got = append(got, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}).Body.String())
	}
	// evenly via the Redis counter cycles A,B,C,A,B,C
	want := []string{"A", "B", "C", "A", "B", "C"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("evenly[%d]=%q, expected %q (whole series %v)", i, got[i], want[i], got)
		}
	}
}

func TestE2E_DistributionRandomStaysInSet(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: "X|||Y|||Z"}},
	}})
	h := newEng(t, gg, engHarness{})
	for i := 0; i < 30; i++ {
		v := do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}).Body.String()
		if v != "X" && v != "Y" && v != "Z" {
			t.Fatalf("random outside the set: %q", v)
		}
	}
}

// ============================================================================
// 10. Uniqueness (cookie + IP/Redis) and firewall + stream limit (Redis)
// ============================================================================

func TestE2E_UniqueIP(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", UniqMethod: "ip", UniqSeconds: 3600,
		Streams: []config.Stream{{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: "X"}}}})
	h := newEng(t, gg, engHarness{redis: true})
	wantHdr(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "X-Kuztds-Uniq", "yes")
	wantHdr(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "X-Kuztds-Uniq", "no")
	wantHdr(t, do(t, h, "/g", "1.2.3.4", map[string]string{"User-Agent": uaWinChrome}), "X-Kuztds-Uniq", "yes")
}

func TestE2E_Firewall(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop",
		Firewall: config.FirewallRule{Enabled: true, Queries: 2, Seconds: 60},
		Streams:  []config.Stream{{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: "X"}}}})
	h := newEng(t, gg, engHarness{redis: true})
	wantCode(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), 200)
	wantCode(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), 200)
	wantCode(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), 403) // 3rd request within the window
	wantCode(t, do(t, h, "/g", "9.9.9.9", map[string]string{"User-Agent": uaWinChrome}), 200) // a different IP is unrestricted
}

func TestE2E_StreamLimit(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "limited", Status: true, Rules: config.Rules{Limit: config.LimitRule{Enabled: true, Type: 1, Count: 2}},
			Out: config.Output{Redirect: "show_text", Out: "LIM"}},
		{Name: "spill", Status: true, Out: config.Output{Redirect: "show_text", Out: "SPILL"}},
	}})
	h := newEng(t, gg, engHarness{redis: true})
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "limited")
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "limited")
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "spill") // limit exhausted
}

// ============================================================================
// 11. Separation, scheduling, chance, aliases, trash, group defaults, api mode
// ============================================================================

func TestE2E_Separation(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "s", Status: true, Out: config.Output{Redirect: "http_redirect", Out: "https://default.example"},
			Separation: config.Separation{Enabled: true, File: "sep.dat"}},
	}})
	h := newEng(t, gg, engHarness{files: map[string]string{"sep.dat": "buy;https://shop.example\nsale;https://sale.example\n"}})
	wantHdr(t, do(t, h, "/g?q=iwanttobuynow", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "Location", "https://shop.example")
	wantHdr(t, do(t, h, "/g?q=nomatch", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "Location", "https://default.example")
}

func TestE2E_Schedule(t *testing.T) {
	allDays := [7]bool{true, true, true, true, true, true, true}
	noDays := [7]bool{}
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "scheduled", Status: true, Rules: config.Rules{Schedule: config.Schedule{Enabled: true, Days: noDays}},
			Out: config.Output{Redirect: "show_text", Out: "SCHED"}},
		{Name: "always", Status: true, Rules: config.Rules{Schedule: config.Schedule{Enabled: true, Days: allDays}},
			Out: config.Output{Redirect: "show_text", Out: "ALWAYS"}},
	}})
	h := newEng(t, gg, engHarness{})
	// the first stream is off today (noDays) → always (all days) is chosen
	wantStream(t, do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "always")
}

func TestE2E_Chance(t *testing.T) {
	shows := func(chance, n int) int {
		gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
			{Name: "s", Status: true, Out: config.Output{Redirect: "javascript", Out: "show()", Chance: chance}},
		}})
		h := newEng(t, gg, engHarness{})
		c := 0
		for i := 0; i < n; i++ {
			if do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}).Body.String() != "" {
				c++
			}
		}
		return c
	}
	// chance=100 and chance=0 (0 = no gate) → always shown.
	if got := shows(100, 50); got != 50 {
		t.Errorf("chance=100: shows %d/50, expected 50", got)
	}
	if got := shows(0, 50); got != 50 {
		t.Errorf("chance=0 (no gate): shows %d/50, expected 50", got)
	}
	// a low chance actually cuts shows (statistically: ~1% of 300).
	if got := shows(1, 300); got > 60 {
		t.Errorf("chance=1: shows %d/300 — gate does not cut", got)
	}
}

func TestE2E_AliasesAndTrash(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "main", Aliases: []string{"promo", "ru"}, Status: true, Redirect: "stop",
		Streams: []config.Stream{{Name: "s", Status: true, Out: config.Output{Redirect: "show_text", Out: "OK"}}}})
	// trash redirect
	h := newEng(t, gg, engHarness{trashMode: "1", trashURL: "https://trash.example"})
	wantBody(t, do(t, h, "/main", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "OK")
	wantBody(t, do(t, h, "/promo", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "OK")
	wantBody(t, do(t, h, "/ru", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "OK")
	wantHdr(t, do(t, h, "/unknown", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), "Location", "https://trash.example")

	// trash 404 / 403
	wantCode(t, do(t, newEng(t, gg, engHarness{trashMode: "3"}), "/none", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), 404)
	wantCode(t, do(t, newEng(t, gg, engHarness{trashMode: "2"}), "/none", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}), 403)
}

func TestE2E_GroupDefaultsAndHeader(t *testing.T) {
	// a stream without its own Out → the group defaults are used (redirect/out/header).
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "show_text", Out: "GDEFAULT", Header: "text/plain; charset=utf-8",
		Streams: []config.Stream{{Name: "s", Status: true}}})
	h := newEng(t, gg, engHarness{})
	rec := do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	wantBody(t, rec, "GDEFAULT")
	wantHdr(t, rec, "Content-Type", "text/plain; charset=utf-8")
}

func TestE2E_APIMode(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g", Status: true, Redirect: "stop", Streams: []config.Stream{
		{Name: "ru", Status: true, Rules: config.Rules{Country: config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}}},
			Out: config.Output{Redirect: "api", Out: "https://offer.example?k=[KEY]&c=[COUNTRY]"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "api", Out: "https://rest.example"}},
	}})
	h := newEng(t, gg, engHarness{})
	// the api client sends its own data; API key "k"
	req := apiRequest{KeyAPI: "k", ID: "g", IP: "8.8.8.8", UserAgent: uaIPhone, Lang: "ru", CFCountry: "ru", Key: "promo", Uniq: "yes"}
	rec := do(t, h, "/?api="+encodeAPI(t, req), "8.8.8.8", nil)
	wantCode(t, rec, 200)
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("api response is not JSON: %v (%s)", err, rec.Body.String())
	}
	out, _ := resp["out"].(string)
	if !strings.Contains(out, "k=promo") || !strings.Contains(out, "c=ru") {
		t.Errorf("api out not expanded correctly: %q", out)
	}
	// invalid API key → 403
	bad := apiRequest{KeyAPI: "wrong", ID: "g", IP: "8.8.8.8"}
	wantCode(t, do(t, h, "/?api="+encodeAPI(t, bad), "8.8.8.8", nil), 403)
}

// ============================================================================
// 12. Large traffic matrix: a realistic multi-stream group + stream
//     preference order (the first matching one in the config wins).
// ============================================================================

func TestE2E_TrafficMatrix(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "mix", Status: true, Redirect: "show_text", Out: "FALLBACK", UniqMethod: "cookie", UniqSeconds: 3600,
		Streams: []config.Stream{
			// 1) search-engine bots are routed via a separate stream (bots on each stream cannot
			//    select a stream — the bot is detected AFTER; so here we segment humans)
			{Name: "ru_mobile", Status: true, Rules: config.Rules{
				Country:  config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}},
				Computer: config.FlagA, Tablet: config.FlagA},
				Out: config.Output{Redirect: "http_redirect", Out: "https://m.ru/?k=[KEY]"}},
			{Name: "ru_desktop", Status: true, Rules: config.Rules{
				Country: config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}},
				Phone:   config.FlagA, Tablet: config.FlagA},
				Out: config.Output{Redirect: "http_redirect", Out: "https://d.ru/"}},
			{Name: "us_any", Status: true, Rules: config.Rules{
				Country: config.ListFilter{Flag: config.FlagB, Values: []string{"us"}}},
				Out: config.Output{Redirect: "show_text", Out: "US"}},
			{Name: "apple_intl", Status: true, Rules: config.Rules{
				Brand: config.ListFilter{Flag: config.FlagB, Values: []string{"Apple"}}},
				Out: config.Output{Redirect: "show_text", Out: "APPLE"}},
		}})
	h := newEng(t, gg, engHarness{})

	type visit struct {
		name, ua, country, ip, stream string
	}
	matrix := []visit{
		{"ru phone", uaSamsung, "RU", "5.0.0.1", "ru_mobile"},
		{"ru xiaomi", uaXiaomi, "RU", "5.0.0.2", "ru_mobile"},
		{"ru desktop", uaWinChrome, "RU", "5.0.0.3", "ru_desktop"},
		{"ru ipad → desktop? no (tablet blocked in both ru) → apple_intl", uaIPad, "RU", "5.0.0.4", "apple_intl"},
		{"us desktop", uaWinFF, "US", "6.0.0.1", "us_any"},
		{"us iphone", uaIPhone, "US", "6.0.0.2", "us_any"},
		{"de iphone → apple_intl", uaIPhone, "DE", "7.0.0.1", "apple_intl"},
		{"de android → fallback(-)", uaSamsung, "DE", "7.0.0.2", "-"},
	}
	for _, v := range matrix {
		rec := do(t, h, "/mix?q=kw", v.ip, map[string]string{"User-Agent": v.ua, "CF-IPCountry": v.country, "Accept-Language": "en"})
		if got := rec.Header().Get("X-Kuztds-Stream"); got != v.stream {
			t.Errorf("[%s] stream=%q, expected %q (country=%s)", v.name, got, v.stream, rec.Header().Get("X-Kuztds-Country"))
		}
	}
}
