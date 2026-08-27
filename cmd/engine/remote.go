package main

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/fetch"
	"github.com/egerkuzma/kuztds/internal/render"
)

// cacheMinFor returns the curl cache lifetime for an out template, or 0 when
// the template expands into a different URL for every visitor.
//
// It has to be called on the raw template. By the time curlBody sees the URL,
// render.Expand has already run and [CID] has become a random string: there is
// no macro left to notice, only a key nobody will ever ask for again.
func cacheMinFor(outRaw string, curlCacheMin int) int {
	if !render.Cacheable(outRaw) {
		return 0
	}
	return curlCacheMin
}

// remoteValue loads the value for [REMOTE]:
// URL substitutions, caching by Cache seconds, regex parsing, fallback to reserved.
// Timeouts on the outbound fetches are set here, not inside fetch, because the
// number follows from the cost of giving up — and only the caller knows it.
//
// [REMOTE] has rm.Reserved: failing is instant and free, so waiting longer only
// buys a slightly better value at the price of every visitor's page. curl has no
// value of its own — failing means trashResult, which on the default
// KUZTDS_TRASH_MODE=0 is a burnt click. Expensive to give up means it is worth
// waiting longer.
//
// The coupling to watch: http.Client's timeout covers waiting for a connection,
// so it is also what bounds the queue behind MaxConnsPerHost. A long curl
// timeout is a long queue — under saturation everyone waits the full duration to
// be handed the trash page. The numbers below are a starting point; they are the
// two knobs to turn under real load.
const (
	remoteTimeout = 2 * time.Second
	curlTimeout   = 8 * time.Second
)

func remoteValue(fc *fetch.Client, ctx context.Context, rm config.Remote, ip, country, city, lang, key string) string {
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	u := rm.URL
	u = strings.ReplaceAll(u, "[IP]", ip)
	u = strings.ReplaceAll(u, "[COUNTRY]", country)
	u = strings.ReplaceAll(u, "[CITY]", city)
	u = strings.ReplaceAll(u, "[LANG]", lang)
	u = strings.ReplaceAll(u, "[KEY]", key)
	// Cache only when the URL template expands into a bounded set of strings.
	// rm.URL is checked, not u: by now the substitutions are done and there is
	// nothing left to recognise.
	ttl := time.Duration(rm.Cache) * time.Second
	if !render.Cacheable(rm.URL) {
		ttl = 0
	}
	val, err := fc.GetCached(ctx, "remote:"+u, ttl, func(fctx context.Context) (string, error) {
		body, err := fc.Get(fctx, u)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(rm.Regexp, "/") {
			return regexFirst(rm.Regexp, body), nil
		}
		return strings.TrimSpace(body), nil
	})
	if err != nil || val == "" {
		return rm.Reserved
	}
	return val
}

// curlBody loads the page at URL and applies find/replace from rules
// (lines "find|||replace"). Cached for curlCache minutes. The bool reports
// whether the fetch actually succeeded.
//
// Any error fails the whole call, including a non-2xx status. Get returns the
// response body alongside the status error, so the previous "err != nil &&
// body == ”" let an upstream error page through: a partner's 502 was rendered
// into the visitor's response under our own 200, and the event log recorded an
// ordinary serve. A failed fetch has no usable body, whatever its length.
func curlBody(fc *fetch.Client, ctx context.Context, url, rules string, curlCacheMin int) (string, bool) {
	ttl := time.Duration(curlCacheMin) * time.Minute
	cctx, ccancel := context.WithTimeout(ctx, curlTimeout)
	defer ccancel()
	body, err := fc.GetCached(cctx, "curl:"+url, ttl, func(fctx context.Context) (string, error) {
		return fc.Get(fctx, url)
	})
	if err != nil {
		return "", false
	}
	for _, ln := range strings.Split(rules, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		find, repl, ok := strings.Cut(ln, "|||")
		if !ok {
			continue
		}
		body = replaceCI(body, find, repl)
	}
	return body, true
}

// regexFirst compiles /pattern/flags and returns the first match group.
func regexFirst(raw, subject string) string {
	if len(raw) < 2 {
		return ""
	}
	end := strings.LastIndexByte(raw, '/')
	if end <= 0 {
		return ""
	}
	pattern := raw[1:end]
	if strings.ContainsRune(raw[end+1:], 'i') {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	m := re.FindStringSubmatch(subject)
	if len(m) >= 2 {
		return m[1]
	}
	if len(m) == 1 {
		return m[0]
	}
	return ""
}

// replaceCI is a case-insensitive replacement (equivalent to str_ireplace).
func replaceCI(s, find, repl string) string {
	if find == "" {
		return s
	}
	return regexp.MustCompile("(?i)"+regexp.QuoteMeta(find)).ReplaceAllString(s, repl)
}
