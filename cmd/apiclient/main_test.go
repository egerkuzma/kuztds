package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egerkuzma/kuztds/internal/server"
)

// fakeTDS spins up a fake engine, decodes ?api= and calls reply to build the
// response. Returns the server and a pointer to the last accepted request.
func fakeTDS(t *testing.T, reply func(w http.ResponseWriter, req apiRequest)) (*httptest.Server, *apiRequest) {
	t.Helper()
	last := &apiRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := base64.StdEncoding.DecodeString(r.URL.Query().Get("api"))
		if err != nil {
			t.Errorf("api param not base64: %v", err)
		}
		if err := json.Unmarshal(raw, last); err != nil {
			t.Errorf("api param not JSON: %v", err)
		}
		reply(w, *last)
	}))
	t.Cleanup(srv.Close)
	return srv, last
}

func newClient(t *testing.T, tds string) http.Handler {
	t.Helper()
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	realIP, err := server.NewRealIP([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	return realIP.Middleware(newClientHandler(clientConfig{
		tdsURL: tds, apiKey: "k", groupID: "g1", cookieName: "ztu", hc: hc,
	}))
}

func req(path string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "8.8.8.8")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestClientForwardsVisitorData(t *testing.T) {
	tds, last := fakeTDS(t, func(w http.ResponseWriter, _ apiRequest) {
		w.WriteHeader(http.StatusOK)
	})
	h := newClient(t, tds.URL)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("/?q=shoes&p=1", map[string]string{
		"User-Agent":      "TestUA",
		"Referer":         "https://www.google.com/search?q=x",
		"Accept-Language": "RU-ru",
	}))

	if last.KeyAPI != "k" || last.ID != "g1" {
		t.Errorf("api key/group not set: %+v", last)
	}
	if last.IP != "8.8.8.8" {
		t.Errorf("client IP = %q, want 8.8.8.8", last.IP)
	}
	if last.Key != "shoes" {
		t.Errorf("key = %q, want shoes", last.Key)
	}
	if last.UserAgent != "TestUA" {
		t.Errorf("ua = %q", last.UserAgent)
	}
	if last.SE != "google" {
		t.Errorf("se = %q, want google", last.SE)
	}
	if last.Lang != "ru" {
		t.Errorf("lang = %q, want ru", last.Lang)
	}
	if last.Uniq != "yes" {
		t.Errorf("first visit uniq=yes, got %q", last.Uniq)
	}
	if len(last.Pars) != 1 || last.Pars[0] != "1" {
		t.Errorf("pars = %v, want [1]", last.Pars)
	}
}

func TestClientProxiesLocationRedirect(t *testing.T) {
	tds, _ := fakeTDS(t, func(w http.ResponseWriter, _ apiRequest) {
		w.Header().Set("Location", "https://offer.example/landing")
		w.WriteHeader(http.StatusFound)
	})
	h := newClient(t, tds.URL)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://offer.example/landing" {
		t.Errorf("Location = %q", loc)
	}
}

func TestClientRedirectsOnAPIJSONOut(t *testing.T) {
	tds, _ := fakeTDS(t, func(w http.ResponseWriter, _ apiRequest) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"out": "https://offer.example/from-json", "type": 0})
	})
	h := newClient(t, tds.URL)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("api JSON out → 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://offer.example/from-json" {
		t.Errorf("Location from JSON out = %q", loc)
	}
}

func TestClientServesContentAsIs(t *testing.T) {
	tds, _ := fakeTDS(t, func(w http.ResponseWriter, _ apiRequest) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<h1>hello</h1>"))
	})
	h := newClient(t, tds.URL)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("content → 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	if rec.Body.String() != "<h1>hello</h1>" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestClientBadGatewayWhenTDSDown(t *testing.T) {
	// nonexistent TDS address → 502
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	realIP, _ := server.NewRealIP([]string{"127.0.0.1/32"})
	h := realIP.Middleware(newClientHandler(clientConfig{
		tdsURL: "http://127.0.0.1:1", apiKey: "k", groupID: "g1", cookieName: "ztu", hc: hc,
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("TDS down → 502, got %d", rec.Code)
	}
}

func TestClientUniqCookieRepeatVisit(t *testing.T) {
	tds, last := fakeTDS(t, func(w http.ResponseWriter, _ apiRequest) {
		w.WriteHeader(http.StatusOK)
	})
	h := newClient(t, tds.URL)

	// first visit → sets cookie, uniq=yes
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("/", nil))
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected uniq cookie")
	}
	// repeat with cookie → uniq=no
	r2 := req("/", nil)
	for _, c := range cookies {
		r2.AddCookie(c)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r2)
	if last.Uniq != "no" {
		t.Errorf("repeat visit uniq=no, got %q", last.Uniq)
	}
}

// --- pure helpers ---

func TestSeByReferer(t *testing.T) {
	cases := map[string]string{
		"https://www.google.com/x":  "google",
		"https://yandex.ru/y":       "yandex",
		"https://go.mail.ru/z":      "mail",
		"https://search.yahoo.com/": "yahoo",
		"https://bing.com/":         "bing",
		"https://example.com/":      "-",
		"":                          "-",
	}
	for ref, want := range cases {
		if got := seByReferer(ref); got != want {
			t.Errorf("seByReferer(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestLangOf(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "EN-us")
	if got := langOf(r); got != "en" {
		t.Errorf("langOf = %q, want en", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	if got := langOf(r2); got != "-" {
		t.Errorf("no header → -, got %q", got)
	}
}

func TestExtraParamsClient(t *testing.T) {
	r := httptest.NewRequest("GET", "/?q=key&a=1&b=hello%20world&c=3", nil)
	got := extraParams(r)
	if len(got) != 3 || got[0] != "1" || got[1] != "hello world" {
		t.Errorf("extraParams = %v", got)
	}
}

func TestGetenvAndProxies(t *testing.T) {
	if getenv("KUZTDS_NONEXISTENT_ABC", "def") != "def" {
		t.Error("getenv default")
	}
	if tp := trustedProxies(); len(tp) == 0 {
		t.Error("default trusted proxies empty")
	}
}
