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

// TestSPAStructure — the markup contains the key elements of the interface:
// one top bar with the navigation (and no sidebar), the flow canvas with its
// wires, the drawer editors, the save bar, and both themes.
func TestSPAStructure(t *testing.T) {
	html := string(indexHTML)

	// Shell: a single top bar; the navigation is built from the NAV table.
	must(t, html, `class="top"`, "top bar")
	must(t, html, `id="nav"`, "navigation")
	for _, page := range []string{"dashboard", "flows", "logs", "conversions", "keywords", "lists", "tools", "automation"} {
		must(t, html, `['`+page+`','`, "navigation entry "+page)
	}
	mustNot(t, html, `class="sidebar"`, "a sidebar — the layout has none")
	must(t, html, `id="period"`, "period selector")
	must(t, html, `id="userbtn"`, "user menu")
	must(t, html, `window.addEventListener('hashchange',render)`, "hash routing")

	// Light/dark theme: a toggle in the top bar, tokens for both, the system
	// preference honoured until the user picks one.
	must(t, html, `data-theme-btn`, "theme toggle")
	must(t, html, `:root[data-theme="dark"]`, "dark theme tokens")
	must(t, html, `prefers-color-scheme: dark`, "system theme preference")
	must(t, html, `localStorage.getItem('kuztds-theme')`, "remembered theme")

	// Dashboard: the performance table by flow and stream.
	must(t, html, `/api/stats/performance`, "performance request")
	must(t, html, `class="perft"`, "performance table")

	// Flows: cards, then a canvas per flow — nodes, destinations, wires, drag handle.
	must(t, html, `id="fgrid"`, "flow cards")
	must(t, html, `class="canvas"`, "flow canvas")
	must(t, html, `id="wires"`, "wires layer")
	must(t, html, `data-grip`, "drag handle")
	must(t, html, `data-toggle`, "per-stream switch")
	must(t, html, `id="fbnode"`, "the no-match node")
	must(t, html, `data-goto=`, "link to another flow")
	must(t, html, `/api/simulate`, "visitor test request")

	// Editors live in a drawer; conditions are added from a menu.
	must(t, html, `class="drawer"`, "drawer")
	must(t, html, `data-cond="${c.k}"`, "condition row")
	must(t, html, `data-c="${c.k}"`, "add-condition menu")
	for _, k := range []string{"country", "asn", "org", "timezone", "get", "operators", "ip_list", "schedule", "limit"} {
		must(t, html, `{k:'`+k+`'`, "condition "+k)
	}
	must(t, html, `id="g_savekeys"`, "save-keywords flow setting")

	// One save bar for everything that can be edited.
	must(t, html, `id="savebar"`, "save bar")
	must(t, html, `id="dirty"`, "unsaved-changes label")

	// Logs: dropdown filters with checkboxes (multi-select), loaded from data.
	must(t, html, `class="msel"`, "logs dropdown filter")
	must(t, html, `class="msel-list"`, "filter values list")
	must(t, html, `/api/logs/filters`, "request for available filter values")
	must(t, html, `const flag=`, "country flag helper")
	must(t, html, `class="flag"`, "country flag in the geo column")

	// Tools and automation.
	must(t, html, `/api/lookup`, "IP lookup request")
	must(t, html, `/api/cron`, "cron config request")
	must(t, html, `/api/cron/run`, "run-now request")
}

// TestSPAKeyFunctions — critical UI functions are present (protection against
// accidental removal when refactoring the markup/script).
func TestSPAKeyFunctions(t *testing.T) {
	html := string(indexHTML)
	for _, fn := range []string{
		"function app(", "function render(", "function route(",
		"function flows(", "function flowPage(", "function renderFlow(",
		"function drawWires(", "function bindSort(", "function moveStream(",
		"function openStream(", "function paintStream(", "function collectStream(",
		"function openFlowSettings(", "function collectFlow(",
		"function condChips(", "function condRow(", "function resetCond(", "function configured(",
		"function paintTester(",
		"function saveAll(", "function saveFlows(", "function saveCron(", "function syncSaveBar(", "function markDirty(",
		"function msel(", "function buildLogFilters(", "function logDetail(",
		"function toggleTheme(", "function drawChart(", "function renderPerf(",
		"function tools(", "function automation(", "function paintCronStatus(",
	} {
		must(t, html, fn, fn)
	}
}

// TestEditorsDoNotMoveThePage pins a property the interface was rebuilt for
// twice: opening something must not scroll or shift what is under it. Editors
// are drawers that overlay the page and scroll inside themselves; if
// scrollIntoView reappears, a layout that needs chasing has crept back in.
func TestEditorsDoNotMoveThePage(t *testing.T) {
	html := string(indexHTML)
	mustNot(t, html, ".scrollIntoView(", "page-scrolling hack")
	must(t, html, `.drawer{position:fixed`, "the drawer overlays the page")
	must(t, html, `.db{flex:1;overflow-y:auto`, "the drawer body scrolls inside itself")
	must(t, html, `scrollbar-gutter:stable`, "no width jump when a page gets a scrollbar")
}

func must(t *testing.T, html, needle, what string) {
	t.Helper()
	if !strings.Contains(html, needle) {
		t.Errorf("not found in SPA: %s (looked for %q)", what, needle)
	}
}

func mustNot(t *testing.T, html, needle, what string) {
	t.Helper()
	if strings.Contains(html, needle) {
		t.Errorf("must not be in the SPA: %s (found %q)", what, needle)
	}
}
