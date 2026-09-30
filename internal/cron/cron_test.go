package cron

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
)

// env is a runner over a temp directory with Telegram and VirusTotal pointed
// at a local server.
type env struct {
	t    *testing.T
	r    *Runner
	dir  string
	srv  *httptest.Server
	mu   sync.Mutex
	sent []string // Telegram messages, in order
	mux  *http.ServeMux
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, dir: t.TempDir(), mux: http.NewServeMux()}
	e.mux.HandleFunc("/botTOKEN/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		e.mu.Lock()
		e.sent = append(e.sent, r.Form.Get("text"))
		e.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	e.srv = httptest.NewServer(e.mux)
	t.Cleanup(e.srv.Close)
	for _, d := range []string{"data", "keys"} {
		if err := os.Mkdir(filepath.Join(e.dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.r = New(Paths{
		Config:  filepath.Join(e.dir, "cron.json"),
		DataDir: filepath.Join(e.dir, "data"),
		KeysDir: filepath.Join(e.dir, "keys"),
		Groups:  filepath.Join(e.dir, "groups.json"),
		CityDB:  filepath.Join(e.dir, "city.mmdb"),
		ASNDB:   filepath.Join(e.dir, "asn.mmdb"),
	}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.r.telegramBase, e.r.vtBase, e.r.vtPause = e.srv.URL, e.srv.URL, 0
	return e
}

func (e *env) messages() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sent...)
}

func (e *env) serve(path, body string) string {
	e.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
	return e.srv.URL + path
}

func (e *env) list(name string) string {
	b, err := os.ReadFile(filepath.Join(e.dir, "data", name+".dat"))
	if err != nil {
		return ""
	}
	return string(b)
}

var tg = Telegram{Token: "TOKEN", ChatID: "1"}

// ---------- config ----------

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range Jobs {
		if c.Enabled(j) {
			t.Errorf("%s is on by default; a fresh install must do nothing until told to", j)
		}
	}
	if err := c.Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestIntervalHasAFloor(t *testing.T) {
	c := Default()
	c.VirusTotal.EveryMinutes = 0
	c.GeoDB.EveryMinutes = -5
	if got := c.Interval(JobVirusTotal); got != 30*time.Minute {
		t.Errorf("virustotal interval = %v; want the 30 min floor", got)
	}
	if got := c.Interval(JobGeoDB); got != time.Hour {
		t.Errorf("geo interval = %v; want the 1 h floor", got)
	}
}

func TestValidate(t *testing.T) {
	bad := []func(*Config){
		func(c *Config) { c.IPLists.Sources = []IPSource{{URL: "file:///etc/passwd", Target: "ip_x"}} },
		func(c *Config) { c.IPLists.Sources = []IPSource{{URL: "https://x.example/a", Target: "../etc/x"}} },
		func(c *Config) { c.IPLists.Sources = []IPSource{{URL: "https://x.example/a", Target: "ip_x", Mode: "wipe"}} },
		func(c *Config) { c.GeoDB.Sources = []GeoSource{{Kind: "weather", URL: "https://x.example/a"}} },
		func(c *Config) { c.GeoDB.Sources = []GeoSource{{Kind: "city", URL: "ftp://x.example/a"}} },
		func(c *Config) { c.VirusTotal.Domains = []string{"https://x.example/path"} },
		func(c *Config) { c.Disk.MinFreePercent = 100 },
	}
	for i, f := range bad {
		c := Default()
		f(&c)
		if c.Validate() == nil {
			t.Errorf("case %d must be rejected", i)
		}
	}
}

// The admin API never returns a stored secret, and sending the mask back
// keeps what is stored — matched by source, not by row number.
func TestSecretsAreMaskedAndKept(t *testing.T) {
	stored := Default()
	stored.Telegram.Token = "bot-secret"
	stored.VirusTotal.APIKey = "vt-secret"
	stored.GeoDB.Sources = []GeoSource{
		{Kind: "city", URL: "https://a.example/city", User: "1", Password: "city-key"},
		{Kind: "asn", URL: "https://a.example/asn", User: "1", Password: "asn-key"},
	}
	red := stored.Redacted()
	b, _ := json.Marshal(red)
	for _, secret := range []string{"bot-secret", "vt-secret", "city-key", "asn-key"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("redacted config leaks %q", secret)
		}
	}
	if stored.GeoDB.Sources[0].Password != "city-key" {
		t.Fatal("Redacted modified the original")
	}

	// The client removed the first source and added a new one in its place.
	edited := red
	edited.GeoDB.Sources = []GeoSource{
		{Kind: "asn", URL: "https://a.example/asn", User: "1", Password: SecretMask},
		{Kind: "city", URL: "https://evil.example/city", Password: SecretMask},
	}
	edited.Telegram.Token = "new-token"
	got := edited.KeepSecrets(stored)
	if got.GeoDB.Sources[0].Password != "asn-key" {
		t.Errorf("asn source must keep its key, got %q", got.GeoDB.Sources[0].Password)
	}
	if got.GeoDB.Sources[1].Password != "" {
		t.Errorf("a different URL must not inherit a stored key, got %q", got.GeoDB.Sources[1].Password)
	}
	if got.Telegram.Token != "new-token" || got.VirusTotal.APIKey != "vt-secret" {
		t.Errorf("token %q, vt key %q", got.Telegram.Token, got.VirusTotal.APIKey)
	}
}

func TestSaveIsOwnerOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cron.json")
	if err := Save(p, Default()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v; the file holds tokens", fi.Mode().Perm())
	}
}

// ---------- ip lists ----------

func TestIPListFromCrawlerJSON(t *testing.T) {
	e := newEnv(t)
	u := e.serve("/googlebot.json", `{"creationTime":"x","prefixes":[{"ipv4Prefix":"66.249.64.0/27"},{"ipv6Prefix":"2001:4860:4801:10::/64"}]}`)
	msg, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{{URL: u, Target: "ip_google"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.list("ip_google"); got != "66.249.64.0/27\n2001:4860:4801:10::/64\n" {
		t.Errorf("ip_google.dat = %q", got)
	}
	if msg != "ip_google: 2" {
		t.Errorf("message = %q", msg)
	}
}

func TestIPListSections(t *testing.T) {
	e := newEnv(t)
	u := e.serve("/all.txt", "junk before any section\n1.1.1.1\n# google\n66.249.64.0/27\nnot an ip\n# Yandex\n5.45.192.0/18\n5.255.192.0-5.255.255.255\n# ../evil\n9.9.9.9\n")
	if _, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{{URL: u}}}); err != nil {
		t.Fatal(err)
	}
	if got := e.list("ip_google"); got != "66.249.64.0/27\n" {
		t.Errorf("ip_google = %q", got)
	}
	if got := e.list("ip_yandex"); got != "5.45.192.0/18\n5.255.192.0-5.255.255.255\n" {
		t.Errorf("ip_yandex = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(e.dir, "data"))
	if len(entries) != 2 {
		t.Errorf("only the two named sections may be written, got %d files", len(entries))
	}
}

// A target list keeps its "# label" lines: that is how wap.dat names operators.
func TestIPListTargetKeepsLabels(t *testing.T) {
	e := newEnv(t)
	u := e.serve("/wap.txt", "# beeline\n217.118.64.0/18\n<html>oops</html>\n# mts\n213.87.0.0/16\n")
	if _, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{{URL: u, Target: "wap"}}}); err != nil {
		t.Fatal(err)
	}
	if got := e.list("wap"); got != "# beeline\n217.118.64.0/18\n# mts\n213.87.0.0/16\n" {
		t.Errorf("wap.dat = %q", got)
	}
}

func TestIPListMergeKeepsWhatIsThere(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "data", "ip_others.dat")
	if err := os.WriteFile(p, []byte("10.0.0.1\n10.0.0.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := e.serve("/o.txt", "10.0.0.2\n10.0.0.3\n")
	if _, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{{URL: u, Target: "ip_others", Mode: "merge"}}}); err != nil {
		t.Fatal(err)
	}
	if got := e.list("ip_others"); got != "10.0.0.1\n10.0.0.2\n10.0.0.3\n" {
		t.Errorf("merged list = %q", got)
	}
}

// A source that answers 200 with nothing usable must leave the list alone,
// and one bad source must not stop the others.
func TestIPListRefusesToEmptyAList(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(e.dir, "data", "ip_google.dat")
	if err := os.WriteFile(p, []byte("66.249.64.0/27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := e.serve("/empty", "<html>maintenance</html>")
	good := e.serve("/bing", `{"prefixes":[{"ipv4Prefix":"157.55.39.0/24"}]}`)
	e.mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusNotFound) })
	msg, err := e.r.runIPLists(context.Background(), IPLists{Sources: []IPSource{
		{URL: empty, Target: "ip_google"},
		{URL: e.srv.URL + "/gone", Target: "ip_yandex"},
		{URL: good, Target: "ip_bing"},
	}})
	if err == nil || !strings.Contains(err.Error(), "no addresses") || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("err = %v; want both failures reported", err)
	}
	if got := e.list("ip_google"); got != "66.249.64.0/27\n" {
		t.Errorf("the working list was overwritten: %q", got)
	}
	if msg != "ip_bing: 1" || e.list("ip_bing") != "157.55.39.0/24\n" {
		t.Errorf("the good source must still be applied: msg %q", msg)
	}
}

// ---------- geo databases ----------

func testDB(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "geo", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func tarGz(files map[string][]byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, b := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(b)
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func TestGeoDBFormats(t *testing.T) {
	city := testDB(t, "GeoLite2-City-Test.mmdb")
	for name, payload := range map[string][]byte{
		"raw":    city,
		"gzip":   gz(city),
		"tar.gz": tarGz(map[string][]byte{"GeoLite2-City_20260901/LICENSE.txt": []byte("x"), "GeoLite2-City_20260901/GeoLite2-City.mmdb": city}),
	} {
		e := newEnv(t)
		u := e.serve("/db", string(payload))
		msg, err := e.r.runGeoDB(context.Background(), GeoDB{Sources: []GeoSource{{Kind: "city", URL: u}}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := os.ReadFile(e.r.paths.CityDB)
		if !bytes.Equal(got, city) {
			t.Errorf("%s: installed file differs from the database", name)
		}
		if msg != "city: GeoLite2-City updated" {
			t.Errorf("%s: message %q", name, msg)
		}
		// Same build again: nothing to do, the file is not touched.
		before, _ := os.Stat(e.r.paths.CityDB)
		msg, err = e.r.runGeoDB(context.Background(), GeoDB{Sources: []GeoSource{{Kind: "city", URL: u}}})
		after, _ := os.Stat(e.r.paths.CityDB)
		if err != nil || msg != "city: GeoLite2-City is up to date" || !after.ModTime().Equal(before.ModTime()) {
			t.Errorf("%s: second run: %q, %v", name, msg, err)
		}
		left, _ := filepath.Glob(filepath.Join(e.dir, ".city.mmdb.dl*"))
		if len(left) != 0 {
			t.Errorf("%s: temp files left behind: %v", name, left)
		}
	}
}

// What is installed is checked first: a wrong or broken download must leave
// the database the engine is using where it is.
func TestGeoDBRejectsWhatItCannotUse(t *testing.T) {
	e := newEnv(t)
	good := testDB(t, "GeoLite2-City-Test.mmdb")
	if err := os.WriteFile(e.r.paths.CityDB, good, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"an ASN database for the city slot": string(testDB(t, "GeoLite2-ASN-Test.mmdb")),
		"an HTML error page":                "<html>Invalid license key</html>",
		"an archive without a database":     string(tarGz(map[string][]byte{"README": []byte("hi")})),
		"a truncated database":              string(good[:len(good)/2]),
	}
	i := 0
	for name, payload := range cases {
		i++
		u := e.serve(fmt.Sprintf("/bad%d", i), payload)
		if _, err := e.r.runGeoDB(context.Background(), GeoDB{Sources: []GeoSource{{Kind: "city", URL: u}}}); err == nil {
			t.Errorf("%s must be rejected", name)
		}
		if got, _ := os.ReadFile(e.r.paths.CityDB); !bytes.Equal(got, good) {
			t.Fatalf("%s replaced the working database", name)
		}
	}
}

func TestGeoDBSendsBasicAuthAndNeedsATarget(t *testing.T) {
	e := newEnv(t)
	asn := testDB(t, "GeoLite2-ASN-Test.mmdb")
	e.mux.HandleFunc("/asn", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "12345" || p != "license" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(asn)
	})
	src := GeoSource{Kind: "asn", URL: e.srv.URL + "/asn", User: "12345", Password: "license"}
	if msg, err := e.r.runGeoDB(context.Background(), GeoDB{Sources: []GeoSource{src}}); err != nil || msg != "asn: GeoLite2-ASN updated" {
		t.Errorf("%q, %v", msg, err)
	}
	e.r.paths.ASNDB = ""
	if _, err := e.r.runGeoDB(context.Background(), GeoDB{Sources: []GeoSource{src}}); err == nil || !strings.Contains(err.Error(), "KUZTDS_ASN_DB") {
		t.Errorf("no target path: err = %v; want it to name the variable", err)
	}
}

// ---------- virustotal ----------

func vtBody(malicious, suspicious int) string {
	return fmt.Sprintf(`{"data":{"attributes":{"last_analysis_stats":{"malicious":%d,"suspicious":%d,"harmless":60}}}}`, malicious, suspicious)
}

func (e *env) writeGroups(groups []config.Group) {
	b, _ := json.Marshal(groups)
	if err := os.WriteFile(e.r.paths.Groups, b, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) readGroups() []config.Group {
	var g []config.Group
	b, _ := os.ReadFile(e.r.paths.Groups)
	_ = json.Unmarshal(b, &g)
	return g
}

func TestVirusTotal(t *testing.T) {
	e := newEnv(t)
	verdict := map[string]string{"bad.example": vtBody(3, 1), "good.example": vtBody(0, 0), "extra.example": vtBody(0, 0)}
	var keys []string
	e.mux.HandleFunc("/api/v3/domains/", func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("x-apikey"))
		body, ok := verdict[strings.TrimPrefix(r.URL.Path, "/api/v3/domains/")]
		if !ok {
			http.Error(w, "quota", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, body)
	})
	e.writeGroups([]config.Group{{ID: "promo", Status: true, Out: "https://good.example/fallback", Streams: []config.Stream{
		{Name: "a", Status: true, Out: config.Output{Redirect: "http_redirect", Out: "https://BAD.example/?k=[KEY]|||https://good.example/x"}},
		{Name: "b", Status: true, Out: config.Output{Redirect: "http_redirect", Out: "https://good.example/?c=[COUNTRY]"}},
		{Name: "macro", Status: true, Out: config.Output{Redirect: "http_redirect", Out: "https://[PAR-1].example/"}},
		{Name: "text", Status: true, Out: config.Output{Redirect: "show_text", Out: "no url here"}},
	}}})

	cfg := Default()
	cfg.Telegram = tg
	cfg.VirusTotal = VirusTotal{APIKey: "VTKEY", FromGroups: true, Domains: []string{"extra.example"}, Threshold: 2, DisableStreams: true}

	msg, err := e.r.runVirusTotal(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if want := "3 domain(s) checked, 1 flagged: bad.example (4); switched off: promo/a"; msg != want {
		t.Errorf("message = %q; want %q", msg, want)
	}
	for _, k := range keys {
		if k != "VTKEY" {
			t.Errorf("api key header = %q", k)
		}
	}
	g := e.readGroups()[0]
	if g.Streams[0].Status || !g.Streams[1].Status || !g.Streams[2].Status || !g.Streams[3].Status {
		t.Errorf("only the stream sending to the flagged domain may be switched off: %+v", g.Streams)
	}
	if m := e.messages(); len(m) != 1 || !strings.Contains(m[0], "bad.example (4)") || !strings.Contains(m[0], "promo/a") {
		t.Errorf("telegram = %q", m)
	}

	// Second run, same verdicts: the operator has been told already.
	if _, err := e.r.runVirusTotal(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if m := e.messages(); len(m) != 1 {
		t.Errorf("a domain that stays flagged must not alert again: %q", m)
	}

	// The API fails for the flagged domain: it keeps its state, so its
	// recovery next time does not look like a fresh detection.
	delete(verdict, "bad.example")
	if _, err := e.r.runVirusTotal(context.Background(), cfg); err == nil {
		t.Error("an API failure must be reported")
	}
	verdict["bad.example"] = vtBody(3, 1)
	if _, err := e.r.runVirusTotal(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if m := e.messages(); len(m) != 1 {
		t.Errorf("an API hiccup must not cause a repeat alert: %q", m)
	}
}

func TestVirusTotalBelowThresholdAndNoKey(t *testing.T) {
	e := newEnv(t)
	e.mux.HandleFunc("/api/v3/domains/", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, vtBody(1, 0)) })
	cfg := Default()
	cfg.Telegram = tg
	cfg.VirusTotal = VirusTotal{APIKey: "k", Domains: []string{"one.example"}, Threshold: 2}
	msg, err := e.r.runVirusTotal(context.Background(), cfg)
	if err != nil || msg != "1 domain(s) checked, 0 flagged" || len(e.messages()) != 0 {
		t.Errorf("%q, %v, messages %q", msg, err, e.messages())
	}
	cfg.VirusTotal.APIKey = ""
	if _, err := e.r.runVirusTotal(context.Background(), cfg); err == nil {
		t.Error("no API key must be an error, not a silent no-op")
	}
}

// ---------- disk ----------

func TestDiskAlertsOnceEachWay(t *testing.T) {
	e := newEnv(t)
	free := uint64(50)
	e.r.disk = func(string) (uint64, uint64, error) { return free << 30, 100 << 30, nil }
	cfg := Default()
	cfg.Telegram = tg
	cfg.Disk = Disk{MinFreePercent: 10, Paths: []string{"/data"}}
	run := func() string {
		msg, err := e.r.runDisk(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	if msg := run(); msg != "/data: 50% free (50.0 GiB of 100.0 GiB)" || len(e.messages()) != 0 {
		t.Errorf("healthy: %q, messages %q", msg, e.messages())
	}
	free = 5
	if msg := run(); !strings.HasSuffix(msg, "— LOW") {
		t.Errorf("low: %q", msg)
	}
	run()
	run()
	if m := e.messages(); len(m) != 1 || !strings.Contains(m[0], "low disk space on /data — 5% free") {
		t.Fatalf("three low runs must alert once: %q", m)
	}
	free = 40
	run()
	run()
	if m := e.messages(); len(m) != 2 || !strings.Contains(m[1], "back to 40% free") {
		t.Errorf("recovery must be announced once: %q", m)
	}
}

func TestDiskErrorAndDefaultPath(t *testing.T) {
	e := newEnv(t)
	var asked string
	e.r.disk = func(p string) (uint64, uint64, error) { asked = p; return 0, 0, errors.New("no such file") }
	if _, err := e.r.runDisk(context.Background(), Default()); err == nil {
		t.Error("an unreadable path must be an error")
	}
	if asked != e.r.paths.DataDir {
		t.Errorf("default path = %q; want the data directory", asked)
	}
	// The real probe works on this platform for a directory that exists.
	if free, total, err := diskFree(e.dir); err != nil || total == 0 || free > total {
		t.Errorf("diskFree = %d, %d, %v", free, total, err)
	}
}

// ---------- cleanup ----------

func TestCleanupRemovesOnlyOldKeywordFiles(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dir, "keys", "promo")
	_ = os.MkdirAll(dir, 0o755)
	old := time.Now().Add(-40 * 24 * time.Hour)
	mk := func(name string, mod time.Time) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("kw\n"), 0o644)
		_ = os.Chtimes(p, mod, mod)
		return p
	}
	stale, fresh, other := mk("2026-08-01.dat", old), mk("2026-09-29.dat", time.Now()), mk("notes.txt", old)
	msg, err := e.r.runCleanup(Cleanup{KeysDays: 30})
	if err != nil || !strings.HasPrefix(msg, "1 keyword file(s)") {
		t.Errorf("%q, %v", msg, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the old .dat must be gone")
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must stay", filepath.Base(p))
		}
	}
	if msg, _ := e.r.runCleanup(Cleanup{KeysDays: 0}); !strings.HasPrefix(msg, "nothing to clean") {
		t.Errorf("no retention: %q", msg)
	}
}

// ---------- conversions ----------

type fakePB struct {
	rows     []store.PostbackRow
	from, to time.Time
	err      error
}

func (f *fakePB) Postbacks(_ context.Context, from, to time.Time, _ string, _ int) ([]store.PostbackRow, float64, error) {
	f.from, f.to = from, to
	var out []store.PostbackRow
	for _, r := range f.rows {
		if !r.TS.Before(from) && r.TS.Before(to) {
			out = append(out, r)
		}
	}
	return out, 0, f.err
}

func TestConversions(t *testing.T) {
	e := newEnv(t)
	pb := &fakePB{}
	e.r.pb = pb
	clock := time.Date(2026, 9, 30, 12, 0, 0, 500e6, time.UTC)
	e.r.now = func() time.Time { return clock }
	cfg := Default()
	cfg.Telegram = tg
	cfg.Conversions.Template = "[PROFIT] [GROUP]/[STREAM] [COUNTRY] [DEVICE] [CID]"

	// History from before the first run is not announced.
	pb.rows = []store.PostbackRow{{TS: clock.Add(-time.Hour), Profit: 9}}
	if msg, err := e.r.runConversions(context.Background(), cfg); err != nil || msg != "watching for new conversions" || len(e.messages()) != 0 {
		t.Fatalf("first run: %q, %v, %q", msg, err, e.messages())
	}

	// Two new ones, returned newest first by the store, announced oldest first.
	t1, t2 := clock.Add(10*time.Second).Truncate(time.Second), clock.Add(20*time.Second).Truncate(time.Second)
	pb.rows = []store.PostbackRow{
		{TS: t2, Group: "Promo", Stream: "us_all", Country: "us", Device: "phone", Profit: 2.5, CID: "c2"},
		{TS: t1, Group: "Promo", Stream: "ru_mobile", Country: "ru", Device: "phone", Profit: 1, CID: "c1"},
	}
	clock = clock.Add(time.Minute)
	if msg, err := e.r.runConversions(context.Background(), cfg); err != nil || msg != "2 new conversion(s)" {
		t.Fatalf("%q, %v", msg, err)
	}
	want := []string{"1.00 Promo/ru_mobile RU phone c1", "2.50 Promo/us_all US phone c2"}
	if m := e.messages(); len(m) != 2 || m[0] != want[0] || m[1] != want[1] {
		t.Errorf("messages = %q; want %q", m, want)
	}
	if pb.to.Nanosecond() != 0 {
		t.Errorf("window end %v must be a whole second", pb.to)
	}

	// Nothing new: the window starts where the previous one ended.
	prevTo := pb.to
	clock = clock.Add(time.Minute)
	if msg, _ := e.r.runConversions(context.Background(), cfg); msg != "0 new conversion(s)" || !pb.from.Equal(prevTo) {
		t.Errorf("%q; from %v, want %v", msg, pb.from, prevTo)
	}
}

func TestConversionsBacklogAndFailure(t *testing.T) {
	e := newEnv(t)
	pb := &fakePB{}
	e.r.pb = pb
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return clock }
	cfg := Default()
	cfg.Telegram = tg
	_, _ = e.r.runConversions(context.Background(), cfg)

	for i := 0; i < 25; i++ { // newest first
		pb.rows = append(pb.rows, store.PostbackRow{TS: clock.Add(time.Duration(30-i) * time.Second), Profit: 1})
	}
	clock = clock.Add(time.Minute)
	if _, err := e.r.runConversions(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	m := e.messages()
	if len(m) != maxAnnounce+1 || !strings.Contains(m[maxAnnounce], "5 more conversions, 5.00 in total") {
		t.Errorf("%d messages, last %q", len(m), m[len(m)-1])
	}

	// A store error leaves the window where it was, so nothing is skipped.
	var since time.Time
	e.r.state(func(st *Status) { since = st.ConvSince })
	pb.err = errors.New("clickhouse down")
	clock = clock.Add(time.Minute)
	if _, err := e.r.runConversions(context.Background(), cfg); err == nil {
		t.Fatal("store error must surface")
	}
	e.r.state(func(st *Status) {
		if !st.ConvSince.Equal(since) {
			t.Error("the window advanced past conversions that were never read")
		}
	})

	e.r.pb = nil
	if _, err := e.r.runConversions(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "KUZTDS_CLICKHOUSE_ADDR") {
		t.Errorf("no store: %v", err)
	}
}

// ---------- telegram ----------

func TestTelegramErrorsDoNotCarryTheToken(t *testing.T) {
	e := newEnv(t)
	secret := Telegram{Token: "123456:SECRET-TOKEN", ChatID: "1"}
	// Wrong token → the server answers 404.
	err := e.r.notify(context.Background(), secret, "hi")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("HTTP error: %v", err)
	}
	// Unreachable server → a transport error, which net/http words with the URL.
	e.srv.Close()
	err = e.r.notify(context.Background(), secret, "hi")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("transport error leaks the token: %v", err)
	}
	if err := e.r.notify(context.Background(), Telegram{}, "hi"); err == nil {
		t.Error("an unconfigured bot must be an error")
	}
}

// ---------- runner ----------

func (e *env) saveConfig(c Config) {
	if err := Save(e.r.paths.Config, c); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) status() Status {
	st, err := ReadStatus(StatusPath(e.r.paths.Config))
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func TestRunnerSchedulesAndPersists(t *testing.T) {
	e := newEnv(t)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e.r.now = func() time.Time { return clock }
	e.r.disk = func(string) (uint64, uint64, error) { return 50, 100, nil }
	cfg := Default()
	cfg.Disk.Enabled = true
	cfg.Disk.EveryMinutes = 10
	e.saveConfig(cfg)

	e.r.Tick(context.Background())
	e.r.Wait()
	st := e.status()
	d := st.Jobs[JobDisk]
	if !d.OK || d.Running || !d.LastRun.Equal(clock) || !d.NextRun.Equal(clock.Add(10*time.Minute)) {
		t.Fatalf("disk status = %+v", d)
	}
	if len(st.Jobs) != 1 {
		t.Errorf("disabled jobs must not run: %v", st.Jobs)
	}
	if !st.Heartbeat.Equal(clock) {
		t.Errorf("heartbeat = %v", st.Heartbeat)
	}

	// Not due yet.
	clock = clock.Add(9 * time.Minute)
	e.r.Tick(context.Background())
	e.r.Wait()
	if got := e.status().Jobs[JobDisk].LastRun; !got.Equal(clock.Add(-9 * time.Minute)) {
		t.Errorf("ran before its interval: last run %v", got)
	}
	// Due.
	clock = clock.Add(time.Minute)
	e.r.Tick(context.Background())
	e.r.Wait()
	if got := e.status().Jobs[JobDisk].LastRun; !got.Equal(clock) {
		t.Errorf("did not run when due: last run %v", got)
	}

	// A restarted service reads the schedule back instead of running everything at once.
	r2 := New(e.r.paths, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r2.now = func() time.Time { return clock.Add(time.Minute) }
	ran := false
	r2.disk = func(string) (uint64, uint64, error) { ran = true; return 50, 100, nil }
	r2.Tick(context.Background())
	r2.Wait()
	if ran {
		t.Error("a restart must not re-run a job that is not due")
	}
}

func TestRunnerRunNowAndFailure(t *testing.T) {
	e := newEnv(t)
	e.saveConfig(Default()) // everything disabled

	if err := RequestRun(e.r.paths.Config, "rm -rf"); err == nil {
		t.Error("an unknown job name must be refused")
	}
	if err := RequestRun(e.r.paths.Config, JobVirusTotal); err != nil {
		t.Fatal(err)
	}
	if err := RequestRun(e.r.paths.Config, JobTelegramTest); err != nil {
		t.Fatal(err)
	}
	e.r.Tick(context.Background())
	e.r.Wait()
	st := e.status()
	if vt := st.Jobs[JobVirusTotal]; vt.OK || !strings.Contains(vt.Message, "API key") {
		t.Errorf("a requested job runs even when disabled and reports its failure: %+v", vt)
	}
	if tt := st.Jobs[JobTelegramTest]; tt.OK || !tt.NextRun.IsZero() {
		t.Errorf("telegram test without a bot: %+v", tt)
	}
	if _, err := os.Stat(TriggerPath(e.r.paths.Config)); !os.IsNotExist(err) {
		t.Error("the request file must be consumed")
	}

	cfg := Default()
	cfg.Telegram = tg
	e.saveConfig(cfg)
	_ = RequestRun(e.r.paths.Config, JobTelegramTest)
	e.r.Tick(context.Background())
	e.r.Wait()
	if m := e.messages(); len(m) != 1 || !e.status().Jobs[JobTelegramTest].OK {
		t.Errorf("test message: %q, %+v", m, e.status().Jobs[JobTelegramTest])
	}
}

// A config that does not parse or validate stops the schedule instead of
// running on half-understood settings; the heartbeat keeps going so the panel
// can tell "broken config" from "service down".
func TestRunnerBadConfig(t *testing.T) {
	e := newEnv(t)
	ran := false
	e.r.disk = func(string) (uint64, uint64, error) { ran = true; return 1, 1, nil }
	if err := os.WriteFile(e.r.paths.Config, []byte(`{"disk":{"enabled":true,"min_free_percent":250}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.r.Tick(context.Background())
	e.r.Wait()
	if ran {
		t.Error("an invalid config must not run jobs")
	}
	if e.status().Heartbeat.IsZero() {
		t.Error("no heartbeat")
	}
}

func TestRunStopsWithContext(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.r.Run(ctx, time.Millisecond); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
