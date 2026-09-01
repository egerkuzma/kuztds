// Package fetch provides HTTP loading of external content with an in-memory TTL cache.
// Used for CURL redirects (curl()) and loading [REMOTE] values (remote_pars()).
// The cache lives in the process's memory.
package fetch

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Client is a thread-safe loader with a cache.
type Client struct {
	hc  *http.Client
	ua  string
	mu  sync.Mutex
	now func() time.Time
	c   map[string]entry
	sf  singleflight.Group

	// onStore, when set, is called after an entry has been written and the
	// mutex released. A test seam; nil in production.
	//
	// The ordering carries two things at once. Called while mu is held, a
	// callback that reads the map deadlocks on a non-reentrant mutex — in the
	// very test written to make this reliable. And "after Unlock" is also the
	// honest meaning: the callback says the entry is stored and visible, not
	// that a store is beginning. That is the state a test needs, not the
	// moment. Do not move this call earlier.
	onStore func()
}

type entry struct {
	val string
	exp time.Time
}

// New creates a client with the given User-Agent.
func New(ua string) *Client {
	if ua == "" {
		ua = "Mozilla/5.0"
	}
	return &Client{
		hc:  &http.Client{Timeout: 10 * time.Second},
		ua:  ua,
		now: time.Now,
		c:   make(map[string]entry),
	}
}

// Get loads the body at URL (without caching). Returns an error on a non-2xx status.
func (c *Client) Get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	// A non-2xx body is never content: it is the upstream's error page. Both
	// callers treat it as a failure, and returning it alongside the error only
	// lets a caller with a sloppy condition render someone else's 502 as a
	// normal response. Drop the body — there is nothing to be right about.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &httpError{resp.StatusCode}
	}
	return string(b), nil
}

// GetCached returns the value for key, loading it through load on a miss and
// caching it for ttl. On a load error the cache is not updated.
//
// Concurrent misses on one key are collapsed into a single load. That is the
// point of the singleflight, and it is also why load runs on a context of its
// own rather than on any caller's: singleflight hands the leader's result to
// every waiter, so a leader whose visitor closed the tab would fail the fetch
// for the whole queue behind it. Today one visitor leaving costs exactly one
// request — its own — and that has to stay true. The detached load is bounded
// by http.Client's own timeout.
//
// Each caller still waits under its own ctx and can walk away without
// disturbing the others. The load outlives all of them if it has to, and
// finishes the job: the entry is written from inside the group, so a fetch
// nobody is waiting for any more still lands in the cache and the next arrival
// gets a hit instead of starting over. Storing in the caller instead would make
// an abandoned fetch ten seconds of work thrown away.
func (c *Client) GetCached(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (string, error)) (string, error) {
	if ttl > 0 {
		if v, ok := c.lookup(key); ok {
			return v, nil
		}
	}
	ch := c.sf.DoChan(key, func() (any, error) {
		v, err := load(context.Background())
		if err != nil {
			return "", err
		}
		if ttl > 0 {
			c.store(key, v, ttl)
		}
		return v, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return "", r.Err
		}
		return r.Val.(string), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// lookup returns a cached value that has not expired.
func (c *Client) lookup(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.c[key]
	if !ok || !c.now().Before(e.exp) {
		return "", false
	}
	return e.val, true
}

// store writes the entry and then, outside the lock, signals onStore.
func (c *Client) store(key, val string, ttl time.Duration) {
	c.mu.Lock()
	c.c[key] = entry{val: val, exp: c.now().Add(ttl)}
	cb := c.onStore
	c.mu.Unlock()
	if cb != nil {
		cb()
	}
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "fetch: http status " + http.StatusText(e.code) }
