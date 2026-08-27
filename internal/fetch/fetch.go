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
)

// Client is a thread-safe loader with a cache.
type Client struct {
	hc  *http.Client
	ua  string
	mu  sync.Mutex
	now func() time.Time
	c   map[string]entry
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

// GetCached returns the value from the cache by key or loads it via load()
// and caches it for ttl. On a load error the cache is not updated.
func (c *Client) GetCached(key string, ttl time.Duration, load func() (string, error)) (string, error) {
	if ttl > 0 {
		c.mu.Lock()
		if e, ok := c.c[key]; ok && c.now().Before(e.exp) {
			c.mu.Unlock()
			return e.val, nil
		}
		c.mu.Unlock()
	}
	val, err := load()
	if err != nil {
		return val, err
	}
	if ttl > 0 {
		c.mu.Lock()
		c.c[key] = entry{val: val, exp: c.now().Add(ttl)}
		c.mu.Unlock()
	}
	return val, nil
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "fetch: http status " + http.StatusText(e.code) }
