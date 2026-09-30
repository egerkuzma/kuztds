package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// httpStatus is a response other than 200. Callers tell a few of them apart:
// 304 (nothing new), 404 (the API does not know the thing), 401/403/429 (no
// point in asking again during this run).
type httpStatus struct {
	host string
	code int
}

func (e *httpStatus) Error() string { return fmt.Sprintf("%s: HTTP %d", e.host, e.code) }

// statusOf returns the HTTP status an error stands for, or 0.
func statusOf(err error) int {
	var s *httpStatus
	if errors.As(err, &s) {
		return s.code
	}
	return 0
}

// get performs a GET and returns the response body of a 200, capped at limit
// bytes. The caller closes it.
func (r *Runner) get(ctx context.Context, rawURL string, hdr map[string]string, user, pass string, limit int64) (io.ReadCloser, error) {
	body, _, err := r.fetch(ctx, rawURL, hdr, user, pass, limit)
	return body, err
}

// fetch is get that also returns the response headers.
func (r *Runner) fetch(ctx context.Context, rawURL string, hdr map[string]string, user, pass string, limit int64) (io.ReadCloser, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("bad URL %q", rawURL)
	}
	req.Header.Set("User-Agent", "kuztds-cron")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		// Drop the URL from the text: it may carry a key in its query string.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("%s: %w", hostOf(rawURL), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, nil, &httpStatus{host: hostOf(rawURL), code: resp.StatusCode}
	}
	return &capped{r: io.LimitReader(resp.Body, limit+1), c: resp.Body, left: limit, host: hostOf(rawURL)}, resp.Header, nil
}

func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "source"
}

// capped fails the read instead of silently truncating: half an IP list or
// half a database is worse than none.
type capped struct {
	r    io.Reader
	c    io.Closer
	left int64
	host string
}

func (c *capped) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, fmt.Errorf("%s: response is larger than the allowed size", c.host)
	}
	return n, err
}

func (c *capped) Close() error { return c.c.Close() }
