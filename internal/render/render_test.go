package render

import (
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestExpandKeyPath(t *testing.T) {
	got := Expand("https://x/?k=[KEY]&p=[PATH]", MacroDeps{Key: "купить телефон", Path: "host.com/f"})
	if !strings.Contains(got, "k=") || strings.Contains(got, "[KEY]") || strings.Contains(got, "[PATH]") {
		t.Fatalf("macros not expanded: %s", got)
	}
	// the key must be url-encoded (space → +/%20, Cyrillic → %xx)
	if strings.Contains(got, "купить телефон") {
		t.Errorf("[KEY] must be url-encoded: %s", got)
	}
}

func TestExpandCountryCity(t *testing.T) {
	got := Expand("https://x/?c=[()COUNTRY()]&t=[()CITY()]", MacroDeps{Country: "ru", City: "moscow"})
	if got != "https://x/?c=ru&t=moscow" {
		t.Errorf("COUNTRY/CITY macros not expanded: %s", got)
	}
}

func TestExpandFullMacroSet(t *testing.T) {
	d := MacroDeps{IP: "1.2.3.4", Country: "ru", City: "moscow", Region: "ru-mow",
		Lang: "ru", Device: "phone", Operator: "beeline", Domain: "ya.ru",
		UserAgent: "UA", CID: "abc123", Pars: []string{"p1", "p2"}}
	got := Expand("ip=[IP];c=[COUNTRY];city=[CITY];reg=[REGION];l=[LANG];d=[DEVICE];op=[OPERATOR];dom=[DOMAIN];ua=[USERAGENT];cid=[CID];a=[PAR-1];b=[PAR-2];e=[PAR-3]", d)
	want := "ip=1.2.3.4;c=ru;city=moscow;reg=ru-mow;l=ru;d=phone;op=beeline;dom=ya.ru;ua=UA;cid=abc123;a=p1;b=p2;e="
	if got != want {
		t.Errorf("macros:\n got=%s\nwant=%s", got, want)
	}
}

func TestRandDfl(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pool"), 0o755)
	os.WriteFile(filepath.Join(dir, "pool", "a.dat"), []byte("x1\nx2\n"), 0o644)
	got := Expand("[RANDDFL-(pool)-1]", MacroDeps{DataDir: dir})
	if got != "x1" && got != "x2" {
		t.Errorf("RANDDFL must return a line from the pool file, got %q", got)
	}
}

func TestRandNum(t *testing.T) {
	// deterministic range min==max
	if got := Expand("[RANDNUM-7-7]", MacroDeps{}); got != "7" {
		t.Errorf("RANDNUM-7-7 = %q; want 7", got)
	}
	rng := rand.New(rand.NewSource(1))
	out := Expand("[RANDNUM-1-6]", MacroDeps{Rng: rng})
	if !regexp.MustCompile(`^[1-6]$`).MatchString(out) {
		t.Errorf("RANDNUM-1-6 out of range: %q", out)
	}
}

func TestRandStr(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	out := Expand("[RANDSTR-(abc)-5]", MacroDeps{Rng: rng})
	if !regexp.MustCompile(`^[abc]{5}$`).MatchString(out) {
		t.Errorf("RANDSTR invalid: %q", out)
	}
}

func TestRandLine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "domains.dat"), []byte("a.com\nb.com\nc.com\n"), 0o644)
	rng := rand.New(rand.NewSource(2))
	// 2 unique lines separated by ';'
	out := Expand("[RANDLINE-(domains.dat)-2/u]", MacroDeps{DataDir: dir, Rng: rng})
	parts := strings.Split(out, ";")
	if len(parts) != 2 || parts[0] == parts[1] {
		t.Errorf("RANDLINE /u must yield 2 distinct lines: %q", out)
	}
	// a nonexistent file → empty
	if got := Expand("[RANDLINE-(nope.dat)-2]", MacroDeps{DataDir: dir}); got != "" {
		t.Errorf("a nonexistent file must yield empty, got %q", got)
	}
}

func TestDoHTTPRedirect(t *testing.T) {
	res := Do("http_redirect", "https://example.com/x", Options{})
	if res.Status != 302 || res.Location != "https://example.com/x" {
		t.Fatalf("http_redirect invalid: %+v", res)
	}
	w := httptest.NewRecorder()
	res.Write(w)
	if w.Code != 302 || w.Header().Get("Location") != "https://example.com/x" {
		t.Errorf("Write redirect: code=%d loc=%q", w.Code, w.Header().Get("Location"))
	}
}

func TestDoShowTextAndErrors(t *testing.T) {
	if r := Do("show_text", "hello", Options{}); string(r.Body) != "hello" || r.Status != 200 {
		t.Errorf("show_text: %+v", r)
	}
	if r := Do("404_not_found", "", Options{}); r.Status != 404 {
		t.Errorf("404: status=%d", r.Status)
	}
	if r := Do("stop", "", Options{}); r.Status != 200 || len(r.Body) != 0 {
		t.Errorf("stop must be an empty 200: %+v", r)
	}
}

func TestDoXSSEscaping(t *testing.T) {
	// a breakout via </script> must be escaped in js_redirect
	res := Do("js_redirect", `</script><img src=x onerror=alert(1)>`, Options{})
	body := string(res.Body)
	if strings.Contains(body, "</script><img") {
		t.Errorf("dangerous injection not escaped: %s", body)
	}
}
