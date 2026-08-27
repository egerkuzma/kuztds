package fetch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingServer reports how many TCP connections the client actually opened.
func countingServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var opened atomic.Int64
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			opened.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &opened
}

// TestConnectionReuse measures the thing MaxIdleConnsPerHost governs. With
// net/http's default of 2, a burst of sequential requests to one host keeps
// throwing connections away and dialling again; with the transport this package
// now builds, the same burst rides a handful of warm sockets.
func TestConnectionReuse(t *testing.T) {
	const n = 200

	run := func(c *Client, srv *httptest.Server) int64 {
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < n/16; j++ {
					if _, err := c.Get(context.Background(), srv.URL); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		return 0
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })

	srvOld, openedOld := countingServer(t, h)
	old := New("ua")
	old.hc = &http.Client{Timeout: 10 * time.Second} // the client this package used to build
	run(old, srvOld)

	srvNew, openedNew := countingServer(t, h)
	run(New("ua"), srvNew)

	t.Logf("%d requests, 16 concurrent: default transport opened %d connections, tuned opened %d",
		n, openedOld.Load(), openedNew.Load())
	if openedNew.Load() > openedOld.Load() {
		t.Errorf("tuned transport opened more connections (%d) than the default one (%d)",
			openedNew.Load(), openedOld.Load())
	}
}

// TestMaxConnsPerHostIsTheCeiling pins the bulkhead: against an upstream that
// never answers, the number of sockets held is bounded by MaxConnsPerHost no
// matter how many requests pile in. Without a ceiling every stalled request is
// another connection, which is how a slow partner takes the engine down instead
// of merely slowing it.
func TestMaxConnsPerHostIsTheCeiling(t *testing.T) {
	release := make(chan struct{})
	srv, opened := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})

	c := NewWithLimits("ua", Limits{MaxConnsPerHost: 4, Backstop: 2 * time.Second})

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Get(context.Background(), srv.URL) // all of these time out
		}()
	}
	time.Sleep(700 * time.Millisecond) // let everything that can connect, connect
	held := opened.Load()
	close(release)
	wg.Wait()

	t.Logf("64 stalled requests, MaxConnsPerHost=4: %d connections held", held)
	if held > 4 {
		t.Errorf("held %d connections, want at most 4 — the ceiling is not holding", held)
	}
}

// TestBackstopBoundsAForgottenCall: a caller with no deadline of its own must
// still be released. The hot path sets a tighter one, this is only the floor
// under a mistake.
func TestBackstopBoundsAForgottenCall(t *testing.T) {
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hold until the client gives up
	})
	c := NewWithLimits("ua", Limits{Backstop: 300 * time.Millisecond})

	start := time.Now()
	_, err := c.Get(context.Background(), srv.URL)
	el := time.Since(start)

	if err == nil {
		t.Fatal("want a timeout error")
	}
	if el > 2*time.Second {
		t.Errorf("call took %v, the backstop did not fire", el)
	}
}
