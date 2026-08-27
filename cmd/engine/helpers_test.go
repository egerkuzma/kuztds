package main

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/ipindex"
)

func TestParseSEKeyword(t *testing.T) {
	cases := map[string]string{
		"https://www.yandex.ru/search/?text=куртка":  "куртка",
		"https://www.google.com/search?q=shoes":      "shoes",
		"https://search.yahoo.com/search?p=laptop":   "laptop",
		"https://nova.rambler.ru/search?query=phone": "phone",
		"https://go.mail.ru/search?q=tv":             "tv",
		"https://www.bing.com/search?q=book":         "book",
		"https://example.com/page?foo=bar":           "",
		"-":                                          "",
		"":                                           "",
		"not a url ::::":                             "",
	}
	for ref, want := range cases {
		if got := parseSEKeyword(ref); got != want {
			t.Errorf("parseSEKeyword(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestAppendKey(t *testing.T) {
	dir := t.TempDir()
	appendKey(dir, "g1", "hello", false)
	appendKey(dir, "g1", "world", false)
	appendKey(dir, "g1", "fromse", true)
	// empty/invalid — ignored
	appendKey(dir, "g1", "", false)
	appendKey("", "g1", "x", false)

	date := time.Now().Format("2006-01-02")
	b, err := os.ReadFile(filepath.Join(dir, "g1", date+".dat"))
	if err != nil {
		t.Fatalf("read keys: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "hello\nworld" {
		t.Errorf("keys file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "g1", date+"-se.dat")); err != nil {
		t.Errorf("se keys file missing: %v", err)
	}
}

func TestParseAPIRequest(t *testing.T) {
	good := `{"key_api":"k","id":"g1","ip":"1.2.3.4"}`
	enc := base64.StdEncoding.EncodeToString([]byte(good))
	if r, ok := parseAPIRequest(enc, "k"); !ok || r.ID != "g1" {
		t.Errorf("valid request must parse, got ok=%v", ok)
	}
	// wrong key
	if _, ok := parseAPIRequest(enc, "other"); ok {
		t.Error("wrong key must fail")
	}
	// empty server key → always rejected
	if _, ok := parseAPIRequest(enc, ""); ok {
		t.Error("empty server key must fail")
	}
	// garbage base64
	if _, ok := parseAPIRequest("!!!notbase64!!!", "k"); ok {
		t.Error("bad base64 must fail")
	}
	// valid base64, but not JSON
	bad := base64.StdEncoding.EncodeToString([]byte("not json"))
	if _, ok := parseAPIRequest(bad, "k"); ok {
		t.Error("non-JSON must fail")
	}
	// URL-safe base64 is also accepted
	encURL := base64.URLEncoding.EncodeToString([]byte(good))
	if _, ok := parseAPIRequest(encURL, "k"); !ok {
		t.Error("url-safe base64 must parse")
	}
}

func TestRegexFirst(t *testing.T) {
	cases := []struct {
		raw, subject, want string
	}{
		{`/code:(\d+)/`, "code:12345 end", "12345"},
		{`/CODE:(\d+)/i`, "code:777", "777"}, // i flag
		{`/(\d+)/`, "no digits here", ""},    // no match
		{`/foo/`, "barfoobar", "foo"},        // no group → whole match
		{`broken`, "x", ""},                  // no surrounding /
		{`/[/`, "x", ""},                     // broken pattern
	}
	for _, c := range cases {
		if got := regexFirst(c.raw, c.subject); got != c.want {
			t.Errorf("regexFirst(%q,%q) = %q, want %q", c.raw, c.subject, got, c.want)
		}
	}
}

func TestReplaceCI(t *testing.T) {
	if got := replaceCI("Hello WORLD", "world", "Go"); got != "Hello Go" {
		t.Errorf("replaceCI case-insensitive = %q", got)
	}
	if got := replaceCI("abc", "", "x"); got != "abc" {
		t.Errorf("empty find returns input, got %q", got)
	}
}

func TestExtraParams(t *testing.T) {
	req := httptest.NewRequest("GET", "/g1?q=key&a=1&b=2&c=3&d=4&e=5&f=6", nil)
	got := extraParams(req)
	// q is dropped, at most 5
	if len(got) != 5 {
		t.Fatalf("want 5 params, got %d (%v)", len(got), got)
	}
	if got[0] != "1" || got[4] != "5" {
		t.Errorf("param order wrong: %v", got)
	}
}

func TestExtraParamsURLDecode(t *testing.T) {
	req := httptest.NewRequest("GET", "/g1?p=hello%20world", nil)
	if got := extraParams(req); len(got) != 1 || got[0] != "hello world" {
		t.Errorf("url-decoded param: %v", got)
	}
}

func TestTrashResult(t *testing.T) {
	if r := trashResult("0", ""); r.Status != 200 {
		t.Errorf("mode 0 → 200, got %d", r.Status)
	}
	if r := trashResult("1", "http://x"); r.Status != 302 || r.Location != "http://x" {
		t.Errorf("mode 1 → 302 redirect, got %d %q", r.Status, r.Location)
	}
	if r := trashResult("1", ""); r.Status != 200 {
		t.Errorf("mode 1 without url → 200, got %d", r.Status)
	}
	if r := trashResult("2", ""); r.Status != 403 {
		t.Errorf("mode 2 → 403, got %d", r.Status)
	}
	if r := trashResult("3", ""); r.Status != 404 {
		t.Errorf("mode 3 → 404, got %d", r.Status)
	}
}

func TestDomainOf(t *testing.T) {
	if got := domainOf("https://ads.example.com/path?x=1"); got != "ads.example.com" {
		t.Errorf("domainOf host: %q", got)
	}
	if got := domainOf(""); got != "unknown" {
		t.Errorf("empty referer → unknown, got %q", got)
	}
}

func TestNewCID(t *testing.T) {
	a, b := newCID(), newCID()
	if len(a) != 10 {
		t.Errorf("cid len = %d, want 10", len(a))
	}
	if a == b {
		t.Errorf("cids should differ: %q == %q", a, b)
	}
	for _, c := range a {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("cid has non-hex char %q", c)
		}
	}
}

func TestDetectBotByIPList(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ip_google.dat"), []byte("66.249.66.1\n"), 0o644)
	lists := ipindex.NewSet(dir, discardLog())
	lists.Load("ip_google", "ip_yandex", "ip_others")
	sigs := detect.NewSignatures(dir, discardLog())

	gbot := netip.MustParseAddr("66.249.66.1")
	human := netip.MustParseAddr("8.8.8.8")
	cfg := config.Bots{IPGoogle: true}

	if got := detectBot(lists, sigs, dir, gbot, "Mozilla", "-", "en", cfg); got != "google" {
		t.Errorf("google IP → google bot, got %q", got)
	}
	if got := detectBot(lists, sigs, dir, human, "Mozilla", "-", "en", cfg); got != detect.BotNone {
		t.Errorf("non-bot IP → none, got %q", got)
	}
}

func TestDetectBotEmptyToggles(t *testing.T) {
	dir := t.TempDir()
	lists := ipindex.NewSet(dir, discardLog())
	sigs := detect.NewSignatures(dir, discardLog())
	ip := netip.MustParseAddr("8.8.8.8")

	if got := detectBot(lists, sigs, dir, ip, "", "-", "en", config.Bots{EmptyUA: true}); got != "empty_ua" {
		t.Errorf("empty UA → empty_ua, got %q", got)
	}
	if got := detectBot(lists, sigs, dir, ip, "Mozilla", "", "en", config.Bots{EmptyRef: true}); got != "empty_ref" {
		t.Errorf("empty ref → empty_ref, got %q", got)
	}
	if got := detectBot(lists, sigs, dir, ip, "Mozilla", "-", "-", config.Bots{EmptyLang: true}); got != "empty_lang" {
		t.Errorf("empty lang → empty_lang, got %q", got)
	}
	ipv6 := netip.MustParseAddr("2001:4860:4860::8888")
	if got := detectBot(lists, sigs, dir, ipv6, "Mozilla", "-", "en", config.Bots{IPv6: true}); got != "ipv6" {
		t.Errorf("ipv6 → ipv6 bot, got %q", got)
	}
}

func TestHelperFns(t *testing.T) {
	if b2yn(true) != "yes" || b2yn(false) != "no" {
		t.Error("b2yn")
	}
	if b2u8(true) != 1 || b2u8(false) != 0 {
		t.Error("b2u8")
	}
	if emptyDash("") != "-" || emptyDash("x") != "x" {
		t.Error("emptyDash")
	}
	if secs(5) != 5*time.Second {
		t.Error("secs")
	}
	if getenv("KUZTDS_NONEXISTENT_VAR_XYZ", "def") != "def" {
		t.Error("getenv default")
	}
	os.Setenv("KUZTDS_TEST_DUR", "2s")
	defer os.Unsetenv("KUZTDS_TEST_DUR")
	if getdur("KUZTDS_TEST_DUR", time.Minute) != 2*time.Second {
		t.Error("getdur parse")
	}
	if getdur("KUZTDS_NONEXISTENT_DUR", time.Minute) != time.Minute {
		t.Error("getdur default")
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
