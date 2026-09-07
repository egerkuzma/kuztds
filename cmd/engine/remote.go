package main

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/fetch"
)

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
	// Every value is query-escaped, as render.Expand does for [KEY]. Pasted in
	// raw, the visitor's ?q= became extra parameters on the partner request —
	// "a&admin=1" is one key to us and two to them.
	u := rm.URL
	u = strings.ReplaceAll(u, "[IP]", url.QueryEscape(ip))
	u = strings.ReplaceAll(u, "[COUNTRY]", url.QueryEscape(country))
	u = strings.ReplaceAll(u, "[CITY]", url.QueryEscape(city))
	u = strings.ReplaceAll(u, "[LANG]", url.QueryEscape(lang))
	u = strings.ReplaceAll(u, "[KEY]", url.QueryEscape(key))
	ttl := time.Duration(rm.Cache) * time.Second
	val, err := fc.GetCached("remote:"+u, ttl, func() (string, error) {
		body, err := fc.Get(ctx, u)
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
// response body alongside the status error, so the previous check — err != nil
// && body == "" — let an upstream error page through: a partner's 502 was
// rendered into the visitor's response under our own 200, and the event log
// recorded an ordinary serve. A failed fetch has no usable body, whatever its
// length.
func curlBody(fc *fetch.Client, ctx context.Context, url, rules string, curlCacheMin int) (string, bool) {
	ttl := time.Duration(curlCacheMin) * time.Minute
	body, err := fc.GetCached("curl:"+url, ttl, func() (string, error) {
		fctx, cancel := context.WithTimeout(ctx, curlTimeout)
		defer cancel()
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
//
// This runs on every CURL request, cached body or not, once per rule. With a
// regexp per rule, 20 rules on an 8 KB page measured 1.4 ms per request — more
// than the rest of the pipeline — and caching the compiled pattern only took
// the allocations away, not the time: the cost is the regexp scanning the body.
// An ASCII find therefore takes a plain path: fold the body's ASCII case once
// (length-preserving, so indexes line up) and let strings.Index do the scan.
// A find with non-ASCII letters still goes through (?i), where Unicode folding
// is the point.
func replaceCI(s, find, repl string) string {
	if find == "" {
		return s
	}
	if !isASCII(find) {
		return ciPattern(find).ReplaceAllString(s, repl)
	}
	lf := asciiLower(find)
	ls := asciiLower(s)
	i := strings.Index(ls, lf)
	if i < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i >= 0 {
		b.WriteString(s[:i])
		b.WriteString(repl)
		s, ls = s[i+len(find):], ls[i+len(find):]
		i = strings.Index(ls, lf)
	}
	b.WriteString(s)
	return b.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// asciiLower folds A–Z only, so len(out) == len(s) and byte offsets into the
// folded copy address the same characters in the original.
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for ; i < len(b); i++ {
				if b[i] >= 'A' && b[i] <= 'Z' {
					b[i] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

var ciCache sync.Map // find → *regexp.Regexp

func ciPattern(find string) *regexp.Regexp {
	if re, ok := ciCache.Load(find); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(find))
	ciCache.Store(find, re)
	return re
}
