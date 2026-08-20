package main

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// The group is taken from the first path segment, so a link dressed up as a
// real page still routes. Matching the whole path used to drop every one of
// these into trash mode.
func TestGroupResolvedFromFirstPathSegment(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "HIT"}})
	h := testEnv(t, gg)

	hit := []string{
		"/g1",
		"/g1/",
		"/g1/landing",
		"/g1/iphone-15-sale.html",
		"/g1/a/b/c",
		"/g1/landing?q=shoes", // query is not part of the path
		"/g1?q=shoes",
	}
	for _, p := range hit {
		rec := do(t, h, p, "8.8.8.8", nil)
		if got := rec.Body.String(); got != "HIT" {
			t.Errorf("%s → body %q, want HIT (stream %q)", p, got, rec.Header().Get("X-Kuztds-Stream"))
		}
	}

	// Only the first segment names the group: a different one is still unknown.
	miss := []string{"/", "/nope", "/nope/g1", "/g11", "/g1x/landing"}
	for _, p := range miss {
		rec := do(t, h, p, "8.8.8.8", nil)
		if got := rec.Body.String(); got == "HIT" {
			t.Errorf("%s must not resolve to g1, got %q", p, got)
		}
	}
}

// Repeated slashes never reach the handler: net/http's ServeMux cleans the path
// and redirects first. Pinned here so the 308 is not mistaken for engine routing.
func TestRepeatedSlashesRedirectedByMux(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "HIT"}})
	h := testEnv(t, gg)
	rec := do(t, h, "//g1//x", "8.8.8.8", nil)
	if rec.Code < 300 || rec.Code > 399 {
		t.Fatalf("//g1//x → %d, want a mux redirect (3xx)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/g1/x" {
		t.Errorf("redirect Location = %q, want /g1/x", loc)
	}
}

// Aliases resolve from the first segment too.
func TestAliasResolvedFromFirstPathSegment(t *testing.T) {
	gg := config.NewGroups(&config.Group{
		ID: "main", Aliases: []string{"promo"}, Status: true, Redirect: "stop",
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "OK"}}},
	})
	h := testEnv(t, gg)
	for _, p := range []string{"/main/deep/page.html", "/promo/deep/page.html"} {
		if got := do(t, h, p, "8.8.8.8", nil).Body.String(); got != "OK" {
			t.Errorf("%s → %q, want OK", p, got)
		}
	}
}
