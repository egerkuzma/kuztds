package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/cron"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/security"
)

// toolsServer is an admin with a data directory, the MaxMind test databases
// and a cron config file.
func toolsServer(t *testing.T) (base string, c *http.Client, csrf, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wap.dat"), []byte("# telstra\n1.128.0.0/11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ip_google.dat"), []byte("1.0.0.0/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signature_ua.dat"), []byte("bot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := geo.Open("../geo/testdata/GeoLite2-City-Test.mmdb", "../geo/testdata/GeoLite2-ASN-Test.mmdb", nil)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash, Sessions: security.NewMemoryStore(),
		Limiter: allowAll{}, SessionTTL: time.Hour,
		DataDir: dir, Geo: db, GeoCityPath: "x", CronFile: filepath.Join(dir, "cron.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c = &http.Client{Jar: jar}
	_, csrf = login(t, c, srv.URL, "admin", "p@ss")
	return srv.URL, c, csrf, dir
}

func call(t *testing.T, c *http.Client, method, url, csrf string, body any, out any) int {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decoding: %v", method, url, err)
		}
	}
	return resp.StatusCode
}

func TestLookup(t *testing.T) {
	base, c, csrf, _ := toolsServer(t)
	var out struct {
		Visitor   visitorInfo
		Lists     []struct{ List, Label string }
		Databases []geo.Info
	}
	// 1.128.0.1: Telstra in the ASN database, and in our wap list.
	if code := call(t, c, "GET", base+"/api/lookup?ip=1.128.0.1", csrf, nil, &out); code != 200 {
		t.Fatalf("status %d", code)
	}
	if out.Visitor.ASN != 1221 || out.Visitor.Org != "Telstra Pty Ltd" || out.Visitor.Operator != "telstra" {
		t.Errorf("visitor = %+v", out.Visitor)
	}
	if len(out.Lists) != 1 || out.Lists[0].List != "wap" || out.Lists[0].Label != "telstra" {
		t.Errorf("lists = %+v", out.Lists)
	}
	if len(out.Databases) != 2 || !out.Databases[0].Loaded {
		t.Errorf("databases = %+v", out.Databases)
	}
	// London in the City database, with its time zone turned into an offset.
	call(t, c, "GET", base+"/api/lookup?ip=81.2.69.142", csrf, nil, &out)
	if v := out.Visitor; v.Country != "gb" || v.City != "london" || v.Timezone != "Europe/London" || (v.UTC != "+0" && v.UTC != "+1") {
		t.Errorf("visitor = %+v", v)
	}
	if code := call(t, c, "GET", base+"/api/lookup?ip=not-an-ip", csrf, nil, nil); code != 400 {
		t.Errorf("bad ip → %d; want 400", code)
	}
	// No session → nothing.
	if resp, _ := http.Get(base + "/api/lookup?ip=1.1.1.1"); resp.StatusCode != 401 {
		t.Errorf("anonymous → %d; want 401", resp.StatusCode)
	}
}

func simGroups() []config.Group {
	inc := func(v ...string) config.ListFilter {
		return config.ListFilter{Flag: config.FlagB, Values: v, Raw: v[0]}
	}
	return []config.Group{
		{ID: "promo", Status: true, Redirect: "show_text", Out: "DEFAULT", Geo: "db", Streams: []config.Stream{
			{Name: "off", Status: false, Out: config.Output{Redirect: "show_text", Out: "never"}},
			{Name: "gb_phones", Status: true, Rules: config.Rules{Country: inc("gb"), Computer: config.FlagA, Tablet: config.FlagA},
				Out: config.Output{Redirect: "http_redirect", Out: "https://m.example/"}},
			{Name: "telstra", Status: true, Rules: config.Rules{ASN: inc("AS1221")}, Out: config.Output{Redirect: "group", Out: "au"}},
			{Name: "fb", Status: true, Rules: config.Rules{Query: inc("utm_source=fb")}, Out: config.Output{Redirect: "show_text", Out: "FB"}},
			{Name: "gb_any", Status: true, Rules: config.Rules{Country: inc("gb")}, Out: config.Output{Redirect: "show_text", Out: "GB"}},
		}},
		{ID: "au", Status: true, Redirect: "show_text", Out: "AU DEFAULT", Streams: []config.Stream{
			{Name: "repeat", Status: true, Rules: config.Rules{Unique: config.FlagB}, Out: config.Output{Redirect: "show_text", Out: "AGAIN"}},
		}},
		{ID: "broken", Status: true, Redirect: "group", Out: "nowhere"},
	}
}

const (
	uaPhone   = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
	uaDesktop = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

func TestSimulate(t *testing.T) {
	base, c, csrf, _ := toolsServer(t)
	type result struct {
		Visitor visitorInfo
		Path    []simStep
	}
	run := func(group, ip, ua, query string, repeat bool) result {
		t.Helper()
		var req simRequest
		req.Groups, req.Group = simGroups(), group
		req.Visitor.IP, req.Visitor.UserAgent, req.Visitor.Query, req.Visitor.Repeat = ip, ua, query, repeat
		var out result
		if code := call(t, c, "POST", base+"/api/simulate", csrf, req, &out); code != 200 {
			t.Fatalf("status %d", code)
		}
		return out
	}

	// A London phone: first active stream that matches wins; the disabled one
	// is reported as off; later streams are still evaluated.
	r := run("promo", "81.2.69.142", uaPhone, "", false)
	st := r.Path[0]
	if st.Selected != "gb_phones" || st.Redirect != "http_redirect" || st.Out != "https://m.example/" {
		t.Errorf("phone: %+v", st)
	}
	if s := st.Streams[0]; s.On || s.Match {
		t.Errorf("disabled stream = %+v", s)
	}
	if s := st.Streams[2]; s.Match || s.Why != "asn" {
		t.Errorf("telstra stream for a London IP = %+v; want rejected by asn", s)
	}
	if s := st.Streams[4]; !s.Match {
		t.Errorf("gb_any would match too: %+v", s)
	}
	if r.Visitor.Device != "phone" || r.Visitor.Brand != "Apple" {
		t.Errorf("detected %+v", r.Visitor)
	}

	// The same IP from a desktop: the device rule turns it away, the query
	// string picks the next one.
	r = run("promo", "81.2.69.142", uaDesktop, "?utm_source=fb", false)
	if st := r.Path[0]; st.Streams[1].Why != "device" || st.Selected != "fb" {
		t.Errorf("desktop with utm: %+v", st)
	}

	// A Telstra address follows the link into another group; there a unique
	// visitor falls to the default and a returning one matches.
	r = run("promo", "1.128.0.1", uaDesktop, "", false)
	if len(r.Path) != 2 || r.Path[0].Selected != "telstra" || r.Path[1].Group != "au" || r.Path[1].Selected != "-" || r.Path[1].Out != "AU DEFAULT" {
		t.Errorf("link, unique: %+v", r.Path)
	}
	if s := r.Path[1].Streams[0]; s.Why != "unique" {
		t.Errorf("why = %q; want unique", s.Why)
	}
	r = run("promo", "1.128.0.1", uaDesktop, "", true)
	if r.Path[1].Selected != "repeat" {
		t.Errorf("link, returning visitor: %+v", r.Path[1])
	}

	// A link to nowhere is reported, not followed.
	r = run("broken", "8.8.8.8", uaDesktop, "", false)
	if len(r.Path) != 1 || !strings.Contains(r.Path[0].Error, "does not exist") {
		t.Errorf("broken link: %+v", r.Path)
	}

	var req simRequest
	req.Groups, req.Group, req.Visitor.IP = simGroups(), "missing", "8.8.8.8"
	if code := call(t, c, "POST", base+"/api/simulate", csrf, req, nil); code != 400 {
		t.Errorf("unknown group → %d", code)
	}
	req.Group, req.Visitor.IP = "promo", "nope"
	if code := call(t, c, "POST", base+"/api/simulate", csrf, req, nil); code != 400 {
		t.Errorf("bad ip → %d", code)
	}
	if code := call(t, c, "POST", base+"/api/simulate", "wrong-csrf", req, nil); code == 200 || code == 400 {
		t.Errorf("without a CSRF token → %d; want a refusal", code)
	}
}

func TestCronEndpoints(t *testing.T) {
	base, c, csrf, dir := toolsServer(t)
	cronFile := filepath.Join(dir, "cron.json")

	// Nothing saved yet: defaults, service not running.
	var st cronState
	if code := call(t, c, "GET", base+"/api/cron", csrf, nil, &st); code != 200 {
		t.Fatalf("status %d", code)
	}
	if !st.Available || st.Alive || st.Config.Disk.Enabled || len(st.Jobs) != len(cron.Jobs) || !st.GeoPaths.City || st.GeoPaths.ASN {
		t.Errorf("fresh state = %+v", st)
	}

	// Save with secrets.
	cfg := st.Config
	cfg.Telegram = cron.Telegram{Token: "123:SECRET", ChatID: "42"}
	cfg.VirusTotal.APIKey = "VT-SECRET"
	cfg.Disk.Enabled = true
	if code := call(t, c, "PUT", base+"/api/cron", csrf, cfg, nil); code != 200 {
		t.Fatalf("save → %d", code)
	}
	raw, _ := os.ReadFile(cronFile)
	if !strings.Contains(string(raw), "123:SECRET") {
		t.Fatal("the token must be stored")
	}

	// Read back: masked, and nothing in the body gives the secret away.
	resp, _ := c.Get(base + "/api/cron")
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	if strings.Contains(body.String(), "SECRET") {
		t.Fatalf("GET /api/cron leaks a secret: %s", body.String())
	}
	_ = json.Unmarshal(body.Bytes(), &st)
	if st.Config.Telegram.Token != cron.SecretMask || st.Config.Telegram.ChatID != "42" || !st.Config.Disk.Enabled {
		t.Errorf("read back = %+v", st.Config)
	}

	// Saving the masked form keeps the secrets; changing something else works.
	cfg = st.Config
	cfg.Disk.MinFreePercent = 25
	if code := call(t, c, "PUT", base+"/api/cron", csrf, cfg, nil); code != 200 {
		t.Fatalf("second save → %d", code)
	}
	stored, _ := cron.Load(cronFile)
	if stored.Telegram.Token != "123:SECRET" || stored.VirusTotal.APIKey != "VT-SECRET" || stored.Disk.MinFreePercent != 25 {
		t.Errorf("stored = %+v", stored)
	}

	// Invalid config is refused with the reason and nothing is written.
	cfg.IPLists.Sources = []cron.IPSource{{URL: "https://x.example/l", Target: "../../etc/cron.d/x"}}
	var e struct{ Error string }
	if code := call(t, c, "PUT", base+"/api/cron", csrf, cfg, &e); code != 400 || !strings.Contains(e.Error, "target") {
		t.Errorf("bad target → %d %q", code, e.Error)
	}
	if again, _ := cron.Load(cronFile); len(again.IPLists.Sources) != len(stored.IPLists.Sources) {
		t.Error("a refused save changed the file")
	}

	// Run now: queued for the service; unknown names are refused.
	if code := call(t, c, "POST", base+"/api/cron/run", csrf, map[string]string{"job": cron.JobDisk}, nil); code != 202 {
		t.Errorf("run → %d", code)
	}
	if b, _ := os.ReadFile(cron.TriggerPath(cronFile)); string(b) != "disk\n" {
		t.Errorf("trigger file = %q", b)
	}
	if code := call(t, c, "POST", base+"/api/cron/run", csrf, map[string]string{"job": "../x"}, nil); code != 400 {
		t.Errorf("unknown job → %d", code)
	}

	// A heartbeat makes the service "alive".
	hb, _ := json.Marshal(cron.Status{Heartbeat: time.Now(), Jobs: map[string]cron.JobStatus{"disk": {OK: true, Message: "fine"}}})
	_ = os.WriteFile(cron.StatusPath(cronFile), hb, 0o644)
	call(t, c, "GET", base+"/api/cron", csrf, nil, &st)
	if !st.Alive || st.Status.Jobs["disk"].Message != "fine" {
		t.Errorf("with a heartbeat: alive=%v status=%+v", st.Alive, st.Status.Jobs)
	}
}

// Without KUZTDS_CRON_FILE the page still loads (defaults, marked
// unavailable) and writes are refused rather than dropped.
func TestCronUnavailable(t *testing.T) {
	srv, _, _ := fullServer(t, nil)
	c, csrf := authedClient(t, srv.URL)
	var st cronState
	if code := call(t, c, "GET", srv.URL+"/api/cron", csrf, nil, &st); code != 200 || st.Available {
		t.Errorf("GET → %d, available=%v", code, st.Available)
	}
	if code := call(t, c, "PUT", srv.URL+"/api/cron", csrf, cron.Default(), nil); code != 503 {
		t.Errorf("PUT → %d; want 503", code)
	}
	if code := call(t, c, "POST", srv.URL+"/api/cron/run", csrf, map[string]string{"job": "disk"}, nil); code != 503 {
		t.Errorf("run → %d; want 503", code)
	}
	// The lookup degrades to "nothing known" instead of failing.
	var out struct{ Visitor visitorInfo }
	if code := call(t, c, "GET", srv.URL+"/api/lookup?ip=8.8.8.8", csrf, nil, &out); code != 200 || out.Visitor.Country != "-" {
		t.Errorf("lookup without geo → %d %+v", code, out.Visitor)
	}
}
