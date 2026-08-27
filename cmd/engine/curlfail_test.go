package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/fetch"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/logbuf"
	"github.com/egerkuzma/kuztds/internal/server"
)

// captureIns collects the batches logbuf flushes, so a test can assert on what
// the event log actually recorded.
type captureIns struct{ events []logbuf.Event }

func (c *captureIns) InsertEvents(_ context.Context, batch []logbuf.Event) error {
	c.events = append(c.events, batch...)
	return nil
}

// curlEnv builds an engine with a live event buffer and the given trash mode.
func curlEnv(t *testing.T, groups *config.Groups, trashMode string) (http.Handler, *captureIns, func()) {
	t.Helper()
	dir := t.TempDir()
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	ins := &captureIns{}
	buf := logbuf.New(ins, 100, 1, time.Millisecond, log)
	ctx, cancel := context.WithCancel(context.Background())
	go buf.Run(ctx)
	d := &engineDeps{
		log: log, lists: lists, sigs: detect.NewSignatures(dir, log), geores: geo.Nop{},
		groups: groups, logs: buf, fetcher: fetch.New(""),
		dataDir: dir, keysDir: dir, trashMode: trashMode,
	}
	realIP, err := server.NewRealIP([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.root)
	stop := func() {
		cancel()
		select {
		case <-buf.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("logbuf did not drain")
		}
	}
	return realIP.Middleware(mux), ins, stop
}

func curlGroup(url string) *config.Groups {
	return config.NewGroups(&config.Group{
		ID: "g", Status: true, Redirect: "stop",
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "curl", Out: url}}},
	})
}

// TestCurlUpstreamErrorNotServedAs200 pins the hole this branch closes: fetch.Get
// hands back the response body together with the status error, so an upstream
// 502 used to be rendered into our own 200 and logged as an ordinary serve.
func TestCurlUpstreamErrorNotServedAs200(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><title>502 Bad Gateway</title></html>"))
	}))
	defer up.Close()

	// trash mode 3 => 404, so the failure is visible to any monitoring.
	h, ins, stop := curlEnv(t, curlGroup(up.URL), "3")
	rec := do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	stop()

	if rec.Code == http.StatusOK {
		t.Errorf("upstream 502 served as 200, body=%q", rec.Body.String())
	}
	if b := rec.Body.String(); strings.Contains(b, "Bad Gateway") {
		t.Errorf("upstream error page leaked into the response: %q", b)
	}
	if len(ins.events) != 1 {
		t.Fatalf("want 1 logged event, got %d", len(ins.events))
	}
	if got := ins.events[0].Redirect; got != "curl_error" {
		t.Errorf("event redirect = %q, want %q — a failed fetch must not be logged as a normal serve", got, "curl_error")
	}
}

// TestCurlSuccessStillLogsCurl guards the other direction: a healthy upstream
// must keep its body, its 200 and its plain "curl" in the log.
func TestCurlSuccessStillLogsCurl(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<b>landing</b>"))
	}))
	defer up.Close()

	h, ins, stop := curlEnv(t, curlGroup(up.URL), "3")
	rec := do(t, h, "/g", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	stop()

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "landing") {
		t.Errorf("body = %q, want the upstream page", rec.Body.String())
	}
	if len(ins.events) != 1 || ins.events[0].Redirect != "curl" {
		t.Errorf("events = %+v, want one event with redirect=curl", ins.events)
	}
}
