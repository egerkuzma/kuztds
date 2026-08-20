package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// The rotator cookie is written by the engine but comes back from the visitor,
// so its value is attacker-controlled. Every branch of pickOut must stay inside
// the variant set no matter what the cookie says.
func TestRotatorHostileCookie(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "A|||B|||C", Distribution: "rotator"}})
	h := testEnv(t, gg)

	inSet := map[string]bool{"A": true, "B": true, "C": true}
	for _, val := range []string{"-1", "-5", "-999999", "999999", "0", "2", "abc", "", "1e5", "+3"} {
		req := httptest.NewRequest(http.MethodGet, "/g1", nil)
		req.RemoteAddr = "127.0.0.1:1"
		req.Header.Set("X-Forwarded-For", "8.8.8.8")
		req.AddCookie(&http.Cookie{Name: "ztrot_g1_s", Value: val})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req) // must not panic
		if got := rec.Body.String(); !inSet[got] {
			t.Errorf("cookie %q → body %q, want one of A/B/C", val, got)
		}
	}
}

// pickOut must never index outside parts, whatever the cookie or the Redis
// counter say. Checked directly so the invariant is pinned without HTTP.
func TestPickOutStaysInRange(t *testing.T) {
	raw := "A|||B|||C"
	for _, val := range []string{"-1", "-5", "-999999", "999999", "abc", "3", "2"} {
		req := httptest.NewRequest(http.MethodGet, "/g1", nil)
		req.AddCookie(&http.Cookie{Name: "ztrot_g_s", Value: val})
		got := pickOut(httptest.NewRecorder(), req, nil, req.Context(), "g", "s", raw, "rotator")
		if got != "A" && got != "B" && got != "C" {
			t.Errorf("cookie %q → %q, want one of A/B/C", val, got)
		}
	}
	// no separator → the string is returned untouched
	if got := pickOut(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
		nil, t.Context(), "g", "s", "single", "rotator"); got != "single" {
		t.Errorf("without ||| the out is returned as is, got %q", got)
	}
}
