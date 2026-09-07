package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/fetch"
)

// remoteStream builds a group whose out is bare [REMOTE], fetched from srv.
func remoteStream(url, dist, reserved string) *config.Groups {
	return group(config.Stream{Name: "s", Status: true,
		Out:    config.Output{Redirect: "show_text", Out: "[REMOTE]", Distribution: dist},
		Remote: config.Remote{Enabled: true, URL: url, Reserved: reserved},
	})
}

// "|||" is the distribution separator, and pickOut applies it to the template.
// A remote value carrying it used to be split too, which let the partner — or
// anyone who can steer the partner, since ?q= reaches that URL raw — choose
// which of our variants a visitor gets. It is a bad response now, not a menu.
func TestRemoteBodyCannotPickVariants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("a|||b"))
	}))
	defer srv.Close()

	h := testEnv(t, remoteStream(srv.URL, "random", "RESERVED"), func(d *engineDeps) {
		d.fetcher = fetch.New("test")
	})
	for i := 0; i < 20; i++ {
		if body := do(t, h, "/g1", "8.8.8.8", nil).Body.String(); body != "RESERVED" {
			t.Fatalf("body = %q, want RESERVED — the partner must not pick a variant", body)
		}
	}
}

// render.Expand's rule is that substituted values are never rescanned, and it is
// the whole defence against visitor-controlled macros. The remote value used to
// walk around it: the handler spliced the partner's body into outRaw before
// Expand ran, so as far as Expand was concerned it was our own template.
func TestRemoteBodyIsNotMacroExpanded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.dat"), []byte("LEAKED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[RANDLINE-(secret.dat)-1]"))
	}))
	defer srv.Close()

	h := testEnv(t, remoteStream(srv.URL, "", ""), func(d *engineDeps) {
		d.fetcher = fetch.New("test")
		d.dataDir = dir
	})
	if body := do(t, h, "/g1", "8.8.8.8", nil).Body.String(); body != "[RANDLINE-(secret.dat)-1]" {
		t.Errorf("partner response was macro-expanded against dataDir: body = %q", body)
	}
}
