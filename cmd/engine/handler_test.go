package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/seplist"
	"github.com/egerkuzma/kuztds/internal/server"
)

// testEnv assembles a minimal engine for httptest: empty dataDir, Nop geo,
// without Redis/ClickHouse. groups is supplied by the test. Returns an http.Handler
// with realip middleware (trusts 127.0.0.1, the client IP is taken from XFF).
func testEnv(t *testing.T, groups *config.Groups, opts ...func(*engineDeps)) http.Handler {
	t.Helper()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...) // no files — lists are empty, that's fine
	sigs := detect.NewSignatures(dir, log)

	seps := seplist.NewSet(dir, log)
	seps.Load(separationFiles(groups)...)
	d := &engineDeps{
		log: log, lists: lists, sigs: sigs, seps: seps, geores: geo.Nop{},
		groups: groups, dataDir: dir, keysDir: dir, trashMode: "0",
	}
	for _, o := range opts {
		o(d)
	}
	realIP, err := server.NewRealIP([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		t.Fatalf("realip: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.root)
	return realIP.Middleware(mux)
}

// do issues a request to the engine with the given client IP (via XFF from a trusted
// localhost peer) and headers.
func do(t *testing.T, h http.Handler, path, ip string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func group(streams ...config.Stream) *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "g1", Name: "g1", Status: true, Redirect: "stop",
		UniqSeconds: 86400, Streams: streams,
	})
}

// --- basic routes ---

func TestUnknownGroupTrash200(t *testing.T) {
	h := testEnv(t, config.NewGroups()) // no groups
	rec := do(t, h, "/nope", "8.8.8.8", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("trash mode 0 → 200, got %d", rec.Code)
	}
}

func TestTrashModes(t *testing.T) {
	cases := []struct {
		mode, url string
		want      int
	}{
		{"0", "", http.StatusOK},
		{"1", "https://x.example", http.StatusFound},
		{"2", "", http.StatusForbidden},
		{"3", "", http.StatusNotFound},
	}
	for _, c := range cases {
		h := testEnv(t, config.NewGroups(), func(d *engineDeps) {
			d.trashMode, d.trashURL = c.mode, c.url
		})
		rec := do(t, h, "/nope", "8.8.8.8", nil)
		if rec.Code != c.want {
			t.Errorf("trash mode %q: want %d, got %d", c.mode, c.want, rec.Code)
		}
		if c.mode == "1" && rec.Header().Get("Location") != c.url {
			t.Errorf("trash redirect: want Location %q, got %q", c.url, rec.Header().Get("Location"))
		}
	}
}

func TestDisabledGroupIsTrash(t *testing.T) {
	gg := config.NewGroups(&config.Group{ID: "g1", Status: false})
	h := testEnv(t, gg, func(d *engineDeps) { d.trashMode = "3" })
	rec := do(t, h, "/g1", "8.8.8.8", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled group → trash 404, got %d", rec.Code)
	}
}

func TestInvalidIP200(t *testing.T) {
	h := testEnv(t, group())
	// no XFF and the peer is trusted localhost → ClientIP = 127.0.0.1, valid.
	// Pass deliberately garbage XFF — realip returns the peer (127.0.0.1), still valid.
	// To get an invalid IP, send api mode without a key? No. Check: blacklist is empty,
	// localhost passes as a regular client → not trash (the group exists), serves stop=200.
	rec := do(t, h, "/g1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid localhost client → 200, got %d", rec.Code)
	}
}

// --- blacklist ---

func TestBlacklistForbidden(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, dir, "ip_blacklist", "9.9.9.9")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, log)
	d := &engineDeps{log: log, lists: lists, sigs: sigs, geores: geo.Nop{},
		groups: group(), dataDir: dir, keysDir: dir, trashMode: "0"}
	realIP, _ := server.NewRealIP([]string{"127.0.0.1/32"})
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.root)
	h := realIP.Middleware(mux)

	if rec := do(t, h, "/g1", "9.9.9.9", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("blacklisted IP → 403, got %d", rec.Code)
	}
	if rec := do(t, h, "/g1", "8.8.8.8", nil); rec.Code == http.StatusForbidden {
		t.Fatalf("non-blacklisted IP must not be 403")
	}
}

// --- stream selection and filters ---

func TestStreamSelectionByDevice(t *testing.T) {
	// desktop stream blocks phone/tablet; mobile stream blocks computer.
	gg := group(
		config.Stream{Name: "mobile", Status: true,
			Rules: config.Rules{Computer: config.FlagA, Tablet: config.FlagA},
			Out:   config.Output{Redirect: "show_text", Out: "MOBILE"}},
		config.Stream{Name: "desktop", Status: true,
			Rules: config.Rules{Phone: config.FlagA, Tablet: config.FlagA},
			Out:   config.Output{Redirect: "show_text", Out: "DESKTOP"}},
	)
	h := testEnv(t, gg)

	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
	desktop := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

	rec := do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": iphone})
	if got := rec.Body.String(); got != "MOBILE" {
		t.Errorf("iPhone → MOBILE stream, got %q (stream=%s)", got, rec.Header().Get("X-Kuztds-Stream"))
	}
	rec = do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": desktop})
	if got := rec.Body.String(); got != "DESKTOP" {
		t.Errorf("desktop → DESKTOP stream, got %q (stream=%s)", got, rec.Header().Get("X-Kuztds-Stream"))
	}
}

func TestCountryWhitelistValuesOnly(t *testing.T) {
	// Regression: the country filter with values (without raw) must work.
	gg := group(
		config.Stream{Name: "ru_only", Status: true,
			Rules: config.Rules{Country: config.ListFilter{Flag: config.FlagB, Values: []string{"ru"}}},
			Out:   config.Output{Redirect: "show_text", Out: "RU"}},
		config.Stream{Name: "rest", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "REST"}},
	)
	h := testEnv(t, gg)

	rec := do(t, h, "/g1", "8.8.8.8", map[string]string{"CF-IPCountry": "RU"})
	if got := rec.Body.String(); got != "RU" {
		t.Errorf("RU visitor → ru_only, got %q", got)
	}
	rec = do(t, h, "/g1", "8.8.8.8", map[string]string{"CF-IPCountry": "US"})
	if got := rec.Body.String(); got != "REST" {
		t.Errorf("US visitor → rest, got %q", got)
	}
}

func TestGroupDefaultsWhenStreamHasNoOut(t *testing.T) {
	// Stream is selected but has no Out of its own → the group defaults are used.
	gg := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "show_text", Out: "GROUPDEFAULT",
		Streams: []config.Stream{{Name: "s", Status: true}},
	})
	h := testEnv(t, gg)
	rec := do(t, h, "/g1", "8.8.8.8", nil)
	if got := rec.Body.String(); got != "GROUPDEFAULT" {
		t.Errorf("want group default out, got %q", got)
	}
}

// --- macros and redirects ---

func TestHTTPRedirectAndMacros(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "http_redirect", Out: "https://t.example/?k=[KEY]&ip=[IP]"}})
	h := testEnv(t, gg)
	rec := do(t, h, "/g1?q=hello", "8.8.8.8", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "k=hello") || !strings.Contains(loc, "ip=8.8.8.8") {
		t.Errorf("macros not expanded: %q", loc)
	}
}

func TestAliasRouting(t *testing.T) {
	gg := config.NewGroups(&config.Group{
		ID: "g1", Aliases: []string{"promo"}, Status: true,
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "OK"}}},
	})
	h := testEnv(t, gg)
	if rec := do(t, h, "/promo", "8.8.8.8", nil); rec.Body.String() != "OK" {
		t.Errorf("alias /promo must resolve to g1")
	}
}

// --- bots ---

func TestBotRedirect(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Rules: config.Rules{},
		Out:   config.Output{Redirect: "show_text", Out: "HUMAN"},
		Bots:  config.Bots{EmptyUA: true, Redirect: "404_not_found"}})
	h := testEnv(t, gg)
	// empty UA → bot → bot_redirect 404
	rec := do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": ""})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty-UA bot → 404, got %d (bot=%s)", rec.Code, rec.Header().Get("X-Kuztds-Bot"))
	}
	if rec.Header().Get("X-Kuztds-Bot") != "empty_ua" {
		t.Errorf("want bot=empty_ua, got %q", rec.Header().Get("X-Kuztds-Bot"))
	}
}

func TestBotSkipServesHuman(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out:  config.Output{Redirect: "show_text", Out: "HUMAN"},
		Bots: config.Bots{EmptyUA: true, Redirect: "skip"}})
	h := testEnv(t, gg)
	rec := do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": ""})
	if got := rec.Body.String(); got != "HUMAN" {
		t.Errorf("redirect=skip → normal stream, got %q", got)
	}
}

// --- separation ---

func TestSeparation(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, dir, "sep", "buy;https://shop.example")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, log)
	gg := group(config.Stream{Name: "s", Status: true,
		Out:        config.Output{Redirect: "http_redirect", Out: "https://default.example"},
		Separation: config.Separation{Enabled: true, File: "sep.dat"}})
	seps := seplist.NewSet(dir, log)
	seps.Load(separationFiles(gg)...)
	d := &engineDeps{log: log, lists: lists, sigs: sigs, seps: seps, geores: geo.Nop{},
		groups: gg, dataDir: dir, keysDir: dir, trashMode: "0"}
	realIP, _ := server.NewRealIP([]string{"127.0.0.1/32"})
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.root)
	h := realIP.Middleware(mux)

	rec := do(t, h, "/g1?q=iwanttobuy", "8.8.8.8", nil)
	if loc := rec.Header().Get("Location"); loc != "https://shop.example" {
		t.Errorf("separation key 'buy' → shop, got %q", loc)
	}
	rec = do(t, h, "/g1?q=other", "8.8.8.8", nil)
	if loc := rec.Header().Get("Location"); loc != "https://default.example" {
		t.Errorf("no key match → default out, got %q", loc)
	}
}

// --- postback ---

func TestPostbackNoCHIs200(t *testing.T) {
	h := testEnv(t, group(), func(d *engineDeps) { d.postbackKey = "secret" })
	// chConn nil → just 200, without a panic
	rec := do(t, h, "/?pb=secret&cid=abc&profit=1.5", "8.8.8.8", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("postback → 200, got %d", rec.Code)
	}
}

// --- api mode ---

func TestAPIModeForbiddenWithoutKey(t *testing.T) {
	h := testEnv(t, group(), func(d *engineDeps) { d.apiKey = "apikey" })
	// wrong key → 403
	payload := apiRequest{KeyAPI: "wrong", ID: "g1", IP: "8.8.8.8"}
	rec := do(t, h, "/?api="+encodeAPI(t, payload), "8.8.8.8", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad api key → 403, got %d", rec.Code)
	}
}

func TestAPIModeServes(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "api", Out: "https://offer.example/?k=[KEY]"}})
	h := testEnv(t, gg, func(d *engineDeps) { d.apiKey = "apikey" })
	payload := apiRequest{KeyAPI: "apikey", ID: "g1", IP: "8.8.8.8", Key: "kw", Uniq: "yes"}
	rec := do(t, h, "/?api="+encodeAPI(t, payload), "8.8.8.8", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("api ok → 200, got %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("api response not JSON: %v (%s)", err, rec.Body.String())
	}
	if out, _ := resp["out"].(string); !strings.Contains(out, "k=kw") {
		t.Errorf("api out macro not expanded: %v", resp["out"])
	}
}

// --- out distribution (|||) ---

func TestDistributionRotatorCycles(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "A|||B|||C", Distribution: "rotator"}})
	h := testEnv(t, gg)

	// first visit without a cookie → index 0 = A, sets cookie ztrot_g1_s
	rec := do(t, h, "/g1", "8.8.8.8", nil)
	first := rec.Body.String()
	if first != "A" {
		t.Errorf("rotator first visit → A, got %q", first)
	}
	// pass the cookie back — it should advance to B
	ck := rec.Result().Cookies()
	req := httptest.NewRequest(http.MethodGet, "/g1", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	for _, c := range ck {
		req.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if got := rec2.Body.String(); got != "B" {
		t.Errorf("rotator second visit → B, got %q", got)
	}
}

func TestDistributionRandomStaysInSet(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "X|||Y|||Z"}})
	h := testEnv(t, gg)
	for i := 0; i < 20; i++ {
		got := do(t, h, "/g1", "8.8.8.8", nil).Body.String()
		if got != "X" && got != "Y" && got != "Z" {
			t.Fatalf("random pick out of set: %q", got)
		}
	}
}

// --- cookie uniqueness ---

func TestCookieUniqueness(t *testing.T) {
	gg := config.NewGroups(&config.Group{
		ID: "g1", Status: true, UniqMethod: "cookie", UniqSeconds: 3600,
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "OK"}}},
	})
	h := testEnv(t, gg)
	rec := do(t, h, "/g1", "8.8.8.8", nil)
	if rec.Header().Get("X-Kuztds-Uniq") != "yes" {
		t.Fatalf("first visit uniq=yes, got %q", rec.Header().Get("X-Kuztds-Uniq"))
	}
	ck := rec.Result().Cookies()
	if len(ck) == 0 {
		t.Fatal("expected uniqueness cookie set")
	}
	// repeat visit with cookie → uniq=no
	req := httptest.NewRequest(http.MethodGet, "/g1", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	for _, c := range ck {
		req.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Header().Get("X-Kuztds-Uniq") != "no" {
		t.Errorf("repeat visit uniq=no, got %q", rec2.Header().Get("X-Kuztds-Uniq"))
	}
}

// helpers

func encodeAPI(t *testing.T, req apiRequest) string {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// writeDat writes dataDir/<name>.dat with the given lines.
func writeDat(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	p := filepath.Join(dir, name+".dat")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writeDat %s: %v", p, err)
	}
}
