package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeUIServesSPA — the embedded SPA is served at the root without auth,
// with the correct Content-Type and a non-empty body.
func TestServeUIServesSPA(t *testing.T) {
	rec := httptest.NewRecorder()
	serveUI(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("serveUI → 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, expected text/html", ct)
	}
	if rec.Body.Len() < 5000 {
		t.Errorf("SPA body suspiciously small: %d bytes", rec.Body.Len())
	}
}

// TestSPAServedViaHandler — the GET / route in the shared Handler() serves the SPA.
func TestSPAServedViaHandler(t *testing.T) {
	srv, _, _ := fullServer(t, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / → 200, got %d", resp.StatusCode)
	}
}

// TestSPAStructure — the markup contains the key elements of the new interface:
// sidebar navigation with all tabs, the settings gear and the user block
// in the top right corner, the collapsible group tree and the stream card.
func TestSPAStructure(t *testing.T) {
	html := string(indexHTML)

	// Sidebar + navigation across all sections (buttons are built from navItem('<tab>')).
	must(t, html, `class="sidebar"`, "sidebar")
	must(t, html, "data-t=\"${t}\"", "navigation button template")
	for _, tab := range []string{"dashboard", "logs", "postbacks", "keys", "groups", "lists"} {
		must(t, html, `navItem('`+tab+`')`, "navigation button "+tab)
	}

	// Top right corner: period, settings gear, user chip, logout.
	must(t, html, `id="period"`, "period selector")
	must(t, html, `id="gear"`, "settings gear")
	must(t, html, `class="userchip"`, "user chip")
	must(t, html, `id="out"`, "logout button")

	// Groups: collapsible tree (chevron) and streams.
	must(t, html, `class="glist"`, "group tree")
	must(t, html, `class="chev`, "group collapse chevron")
	must(t, html, `class="snode`, "stream node in the tree")
	must(t, html, `class="streamcard"`, "stream card")

	// Logs: dropdown filters with checkboxes (multi-select), loaded from data.
	must(t, html, `class="msel"`, "logs dropdown filter")
	must(t, html, `class="msel-list"`, "filter values list")
	must(t, html, `/api/logs/filters`, "request for available filter values")

	// Country flag in logs.
	must(t, html, `const flag=`, "country flag helper")
	must(t, html, `class="flag"`, "country flag in the geo column")
}

// TestSPAKeyFunctions — critical UI functions are present (protection against
// accidental removal when refactoring the markup/script).
func TestSPAKeyFunctions(t *testing.T) {
	html := string(indexHTML)
	for _, fn := range []string{
		"function app(", "function openTab(", "function groups(", "function streamForm(",
		"function collectStream(", "function collectGroup(", "function saveAll(",
		"function focusStream(", // focus/scroll to the stream on click
		"scrollIntoView",        // the scroll itself
		"function msel(",        // constructor of the logs dropdown filter
		"function buildLogFilters(",
	} {
		must(t, html, fn, fn)
	}
}

func must(t *testing.T, html, needle, what string) {
	t.Helper()
	if !strings.Contains(html, needle) {
		t.Errorf("not found in SPA: %s (looked for %q)", what, needle)
	}
}
