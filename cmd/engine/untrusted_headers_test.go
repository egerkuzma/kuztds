package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// CF-IPCountry is read from the raw request, not gated by the trusted-proxy
// list the way X-Forwarded-For is, so any visitor can set it. A country is two
// letters; anything else must be dropped, not passed through into the log, the
// response header and — worst — a file name.
func TestCountryHeaderIsValidated(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "c=[COUNTRY]"}})
	h := testEnv(t, gg)

	cases := map[string]string{
		"RU":               "ru",
		"us":               "us",
		"XX":               "xx", // Cloudflare's "unknown"
		"T1":               "t1", // Cloudflare's Tor marker
		"":                 "-",
		"-":                "-",
		"ru;x":             "-",
		"../../etc/passwd": "-",
		"ip_blacklist":     "-",
		"a&b=1":            "-",
		"RUS":              "-",
	}
	for in, want := range cases {
		hdr := map[string]string{"User-Agent": uaWinChrome}
		if in != "" {
			hdr["CF-IPCountry"] = in
		}
		rec := do(t, h, "/g1", "8.8.8.8", hdr)
		if got := rec.Header().Get("X-Kuztds-Country"); got != want {
			t.Errorf("CF-IPCountry %q → country %q, want %q", in, got, want)
		}
		if got := rec.Body.String(); got != "c="+want {
			t.Errorf("CF-IPCountry %q → [COUNTRY] %q, want %q", in, got, "c="+want)
		}
	}
}

// The concrete harm: [RANDLINE-([COUNTRY].dat)-1] is a legitimate per-country
// template, and with the header unvalidated a visitor picked which .dat in the
// data directory got read into their own response.
func TestCountryHeaderCannotPickAFile(t *testing.T) {
	var dir string
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "[RANDLINE-([COUNTRY].dat)-1]"}})
	h := testEnv(t, gg, func(d *engineDeps) { dir = d.dataDir })
	if err := os.WriteFile(filepath.Join(dir, "secret.dat"), []byte("LEAKED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ru.dat"), []byte("ru-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "CF-IPCountry": "secret"})
	if got := rec.Body.String(); got == "LEAKED" {
		t.Fatalf("a visitor chose the file the engine read: body %q", got)
	}
	// The legitimate case keeps working.
	rec = do(t, h, "/g1", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome, "CF-IPCountry": "RU"})
	if got := rec.Body.String(); got != "ru-line" {
		t.Errorf("valid country must still select its file, got %q", got)
	}
}

// Accept-Language is the same shape of input: two bytes taken verbatim.
func TestLangHeaderIsValidated(t *testing.T) {
	gg := group(config.Stream{Name: "s", Status: true,
		Out: config.Output{Redirect: "show_text", Out: "l=[LANG]"}})
	h := testEnv(t, gg)
	cases := map[string]string{"ru-RU,ru;q=0.9": "ru", "EN": "en", "..": "-", "/e": "-", "a&": "-", "": "-"}
	for in, want := range cases {
		hdr := map[string]string{"User-Agent": uaWinChrome}
		if in != "" {
			hdr["Accept-Language"] = in
		}
		if got := do(t, h, "/g1", "8.8.8.8", hdr).Body.String(); got != "l="+want {
			t.Errorf("Accept-Language %q → %q, want %q", in, got, "l="+want)
		}
	}
}
