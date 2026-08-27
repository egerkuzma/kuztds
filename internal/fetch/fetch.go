// Package fetch provides HTTP loading of external content with an in-memory TTL cache.
// Used for CURL redirects (curl()) and loading [REMOTE] values (remote_pars()).
// The cache lives in the process's memory.
package fetch

import (
	"context"
	"io"
	"net"
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

	// backstop is the ceiling applied to a shared load whose caller brought no
	// deadline of its own. It matches hc.Timeout, so it introduces no new limit
	// — it only makes the existing one explicit and, crucially, unconditional.
	backstop time.Duration
}

type entry struct {
	val string
	exp time.Time
}

// Limits caps the outbound side of the client.
type Limits struct {
	// MaxConnsPerHost bounds simultaneous connections to one host. This is the
	// bulkhead: past it, requests wait on the transport instead of each opening
	// its own socket. Zero means the package default.
	MaxConnsPerHost int
	// MaxIdleConnsPerHost is how many warm connections are kept for reuse.
	// Zero means the package default.
	MaxIdleConnsPerHost int
	// Backstop is the ceiling on a single request when the caller's context
	// carries no deadline of its own. Callers on the hot path are expected to
	// set a tighter one; this only keeps a forgotten call from hanging forever.
	Backstop time.Duration
}

// Defaults for Limits. The shape is deliberate; the numbers are a starting
// point, not a measurement.
//
// http.DefaultTransport keeps MaxIdleConnsPerHost at 2, which is fine for a
// program that talks to many hosts occasionally and wrong for this one: every
// visitor whose stream fetches from the same partner competes for those two
// slots, and the rest open a fresh connection — a TCP and TLS handshake per
// request, plus the sockets they leave in TIME_WAIT.
//
// MaxConnsPerHost has no default in net/http at all: unlimited. That is the
// part that takes the engine down rather than merely slowing it. A partner that
// stalls does not fail requests, it holds them, and without a ceiling every
// held request is another connection and another goroutine parked in the
// handler until its deadline.
const (
	defaultMaxConnsPerHost     = 128
	defaultMaxIdleConnsPerHost = 64
	defaultBackstop            = 10 * time.Second
)

// New creates a client with the given User-Agent and the default limits.
func New(ua string) *Client { return NewWithLimits(ua, Limits{}) }

// NewWithLimits creates a client with explicit outbound limits.
func NewWithLimits(ua string, l Limits) *Client {
	if ua == "" {
		ua = "Mozilla/5.0"
	}
	if l.MaxConnsPerHost <= 0 {
		l.MaxConnsPerHost = defaultMaxConnsPerHost
	}
	if l.MaxIdleConnsPerHost <= 0 {
		l.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}
	if l.MaxIdleConnsPerHost > l.MaxConnsPerHost {
		l.MaxIdleConnsPerHost = l.MaxConnsPerHost
	}
	if l.Backstop <= 0 {
		l.Backstop = defaultBackstop
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxConnsPerHost:       l.MaxConnsPerHost,
		MaxIdleConnsPerHost:   l.MaxIdleConnsPerHost,
		MaxIdleConns:          l.MaxIdleConnsPerHost * 8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		hc:       &http.Client{Transport: tr, Timeout: l.Backstop},
		ua:       ua,
		now:      time.Now,
		c:        make(map[string]entry),
		backstop: l.Backstop,
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

// maxEntries caps the cache. Beyond it the map is dropped whole rather than
// evicted entry by entry.
//
// The crude version is the right one here. An LRU is a second data structure to
// keep in step with the map under the same lock, and it would be buying accuracy
// for a cache whose real fix is upstream: callers are expected to pass ttl <= 0
// for templates that expand per visitor (see render.Cacheable), so what remains
// is a small, slow-growing set of low-cardinality keys. The cap exists so a
// caller that forgets cannot run the process out of memory, not to make eviction
// clever.
//
// The cost is honest: a reset throws away hot entries too, and the requests that
// follow all miss at once. That is why the number is large — this should happen
// once in a very long while, or never.
const maxEntries = 50000

// GetCached returns the value from the cache by key or loads it via load and
// caches it for ttl. On a load error the cache is not updated.
//
// A ttl of zero or less means "do not cache" in both directions: nothing is
// read, nothing is stored, and nothing is shared. That is how the caller
// declines to cache a template whose expansion is unique per visitor — see
// render.Cacheable. Sharing such a key would be pure overhead: no two requests
// ask for the same one.
//
// For a cacheable key, concurrent misses collapse into a single load. Once
// MaxConnsPerHost is in place, connections to a partner are a finite resource,
// and fifty copies of one request occupy slots that fifty different requests
// need. Deduplication stopped being a courtesy to the upstream and became a way
// of not spending our own budget on the same answer fifty times.
func (c *Client) GetCached(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (string, error)) (string, error) {
	if ttl <= 0 {
		return load(ctx)
	}
	c.mu.Lock()
	if e, ok := c.c[key]; ok {
		if c.now().Before(e.exp) {
			c.mu.Unlock()
			return e.val, nil
		}
		// Expired. Drop it now: an entry that is never asked for again would
		// otherwise sit in the map for the life of the process, which is how a
		// TTL cache turns into a log of everything it has ever seen.
		delete(c.c, key)
	}
	c.mu.Unlock()

	// Detach the work, not the wait. The two need opposite treatment, and doing
	// only one of them is wrong in a different way each time.
	//
	// The work must not run on a request context. singleflight hands the leader's
	// result to every waiter, cancellation included, so one visitor closing the
	// tab would kill the fetch fifty others are waiting on — a fallback for a
	// reason that is neither theirs nor the upstream's. The load therefore runs on
	// a context with the deadline preserved and the cancellation dropped.
	//
	// The wait must stay on the caller's own context. Do blocks until the fetch
	// finishes, so a detached load would pin every waiter's goroutine for the full
	// deadline even after its visitor has gone — eight seconds each on the curl
	// path. MaxConnsPerHost does not help there: it counts sockets, not goroutines.
	// DoChan lets each waiter leave on its own ctx.Done() while the shared load
	// carries on for whoever is still there.
	//
	// The cancel func belongs to the load, not to this stack frame. Deferring it
	// here would mean the first caller to return tears down the context the shared
	// fetch is running on — the original bug, reintroduced through the back door.
	//
	// The deadline is unconditional on purpose. An earlier version only wrapped
	// when the caller brought a deadline of its own, which left two paths: with
	// one, cancel was real and the bug above was reachable; without one, cancel
	// was a no-op and it was not. A test written on a bare context.Background took
	// the harmless path and certified the broken version. A branch that decides
	// whether a bug is reachable is worse than the bug — so there is no branch.
	// hc.Timeout bounds the fetch either way; this only says so out loud.
	//
	// Real time, not c.now(): the injectable clock exists to age cache entries in
	// tests, and a context deadline is not something it should be able to move.
	dl, ok := ctx.Deadline()
	if !ok {
		dl = time.Now().Add(c.backstop)
	}
	ch := c.sf.DoChan(key, func() (any, error) {
		lctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), dl)
		defer cancel()
		val, err := load(lctx)
		if err != nil {
			return val, err
		}
		c.mu.Lock()
		if len(c.c) >= maxEntries {
			c.c = make(map[string]entry, maxEntries/8)
		}
		c.c[key] = entry{val: val, exp: c.now().Add(ttl)}
		c.mu.Unlock()
		return val, nil
	})

	select {
	case r := <-ch:
		val, _ := r.Val.(string)
		return val, r.Err
	case <-ctx.Done():
		// This visitor is gone. The load keeps running for the others; the
		// channel is buffered, so nothing leaks by not reading it.
		return "", ctx.Err()
	}
}

// Len reports the number of cached entries, expired ones included. Exposed so
// the cap can be asserted in tests rather than trusted.
func (c *Client) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.c)
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "fetch: http status " + http.StatusText(e.code) }
