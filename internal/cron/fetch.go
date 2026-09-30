package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// get performs a GET and returns the response body of a 200, capped at limit
// bytes. The caller closes it.
func (r *Runner) get(ctx context.Context, rawURL string, hdr map[string]string, user, pass string, limit int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bad URL %q", rawURL)
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
		return nil, fmt.Errorf("%s: %w", hostOf(rawURL), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", hostOf(rawURL), resp.StatusCode)
	}
	return &capped{r: io.LimitReader(resp.Body, limit+1), c: resp.Body, left: limit, host: hostOf(rawURL)}, nil
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
