package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// TestMacroInjection_VisitorInputIsExpandedAsMacros documents second-order macro
// expansion in render.Expand: visitor-controlled values ([PAR-n], [USERAGENT],
// [DOMAIN], [PATH], [LANG]) are substituted into the out string first, and the
// RAND* macros are expanded afterwards over the result. So whatever the visitor
// sends is itself treated as macro source.
//
// Here the group's out is a perfectly ordinary "pass the sub-id through"
// template. The visitor puts [RANDNUM-777-777] in that query parameter and the
// engine evaluates it.
func TestMacroInjection_VisitorInputIsExpandedAsMacros(t *testing.T) {
	h := testEnv(t, group(config.Stream{
		Name: "s1", Status: true,
		Out: config.Output{Redirect: "http_redirect", Out: "https://offer.example/?sub=[PAR-1]"},
	}))

	rec := do(t, h, "/g1?sub=[RANDNUM-777-777]", "203.0.113.9", nil)

	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "sub=[RANDNUM-777-777]") {
		t.Fatalf("Location = %q, want the visitor value passed through verbatim, not evaluated", loc)
	}
}

// TestMacroInjection_UserAgentReachesRandline is the same defect through a header
// every visitor controls, combined with the second half of the problem:
// [RANDLINE-(file)-n] does filepath.Join(DataDir, file) with no containment
// check, so "../" walks out of the data directory.
//
// Together they are a remote arbitrary file read: the contents of the file land
// in the redirect the visitor is sent to.
func TestMacroInjection_UserAgentReachesRandline(t *testing.T) {
	var dataDir string
	h := testEnv(t, group(config.Stream{
		Name: "s1", Status: true,
		Out: config.Output{Redirect: "http_redirect", Out: "https://offer.example/?ua=[USERAGENT]"},
	}), func(d *engineDeps) { dataDir = d.dataDir })

	// A file the engine must never serve, one directory above dataDir.
	secret := filepath.Join(filepath.Dir(dataDir), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-LINE\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	defer os.Remove(secret)

	rec := do(t, h, "/g1", "203.0.113.9", map[string]string{
		"User-Agent": "[RANDLINE-(../secret.txt)-1]",
	})

	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "TOP-SECRET") {
		t.Fatalf("file outside dataDir was read and returned to the visitor: Location = %q", loc)
	}
}

// TestMacroInjection_RandstrLengthIsBounded shows the denial-of-service edge of
// the same defect: the repeat count of [RANDSTR-(set)-n] is taken from the
// expanded string, so a visitor can pick it. One request, one huge allocation.
func TestMacroInjection_RandstrLengthIsBounded(t *testing.T) {
	h := testEnv(t, group(config.Stream{
		Name: "s1", Status: true,
		Out: config.Output{Redirect: "http_redirect", Out: "https://offer.example/?ua=[USERAGENT]"},
	}))

	rec := do(t, h, "/g1", "203.0.113.9", map[string]string{
		"User-Agent": "[RANDSTR-(Z)-5000000]",
	})

	if rec.Code != http.StatusFound && rec.Code != http.StatusMovedPermanently {
		t.Skipf("unexpected status %d, nothing to measure", rec.Code)
	}
	if n := len(rec.Header().Get("Location")); n > 100000 {
		t.Fatalf("visitor forced a %d-byte redirect target out of a 22-byte header", n)
	}
}
