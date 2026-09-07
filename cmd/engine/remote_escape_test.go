package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/fetch"
)

// render.Expand URL-encodes [KEY] because it is the visitor's ?q=. remoteValue
// builds the partner URL with its own substitution and used to paste the key
// in raw, so ?q=a&admin=1 became an extra parameter on the partner request.
func TestRemoteURLSubstitutionsAreEscaped(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	rm := config.Remote{Enabled: true, URL: srv.URL + "/?k=[KEY]&c=[COUNTRY]&city=[CITY]&l=[LANG]&ip=[IP]"}
	got := remoteValue(fetch.New("ua", nil), context.Background(), rm, "1.2.3.4", "ru", "new york", "ru", "a&admin=1")
	if got != "ok" {
		t.Fatalf("remote fetch failed: %q", got)
	}
	if strings.Contains(seen, "admin=1") || !strings.Contains(seen, "k=a%26admin%3D1") {
		t.Errorf("[KEY] reached the partner unescaped: %q", seen)
	}
	if !strings.Contains(seen, "city=new+york") {
		t.Errorf("[CITY] with a space must be escaped: %q", seen)
	}
}

// The find/replace rules of a CURL stream are operator config with a small,
// stable set of values. Compiling a regexp for each rule on every request
// cost more than the rest of the pipeline; the compiled form is cached now,
// and this pins that the cache returns the right pattern per rule.
func TestReplaceCICachedPatternsStayDistinct(t *testing.T) {
	s := "Price 100, price 200, cost 5"
	if got := replaceCI(s, "price", "P"); got != "P 100, P 200, cost 5" {
		t.Errorf("first rule: %q", got)
	}
	if got := replaceCI(s, "cost", "C"); got != "Price 100, price 200, C 5" {
		t.Errorf("second rule must not reuse the first pattern: %q", got)
	}
	if got := replaceCI(s, "price", "P"); got != "P 100, P 200, cost 5" {
		t.Errorf("first rule again (cache hit): %q", got)
	}
	// A metacharacter in find is literal, cached or not.
	if got := replaceCI("a.b axb", "a.b", "X"); got != "X axb" {
		t.Errorf("find must be literal: %q", got)
	}
}

// The ASCII fast path and the regexp path must agree, including on non-ASCII
// bodies with ASCII finds (offsets must not drift across multi-byte runes) and
// on non-ASCII finds, which stay on the regexp.
func TestReplaceCIFastPathMatchesRegexp(t *testing.T) {
	cases := []struct{ s, find, repl string }{
		{"Price 100, PRICE 200, price 300", "price", "X"},
		{"Цена: Price 100 — PRICE", "price", "X"},     // ASCII find, non-ASCII body
		{"Цена 100, цена 200, ЦЕНА 300", "цена", "X"}, // non-ASCII find → regexp
		{"aaa", "a", "bb"},            // overlapping/adjacent
		{"no match here", "zzz", "X"}, // no match
		{"x.y x?y xzy", "x.y", "R"},   // metacharacters literal
		{"", "a", "b"},                // empty body
	}
	for _, c := range cases {
		want := ciPattern(c.find).ReplaceAllString(c.s, c.repl)
		if got := replaceCI(c.s, c.find, c.repl); got != want {
			t.Errorf("replaceCI(%q,%q,%q) = %q, regexp gives %q", c.s, c.find, c.repl, got, want)
		}
	}
}
