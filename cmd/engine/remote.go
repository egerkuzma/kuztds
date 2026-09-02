package main

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/fetch"
)

// remoteValue loads the value for [REMOTE]:
// URL substitutions, caching by Cache seconds, regex parsing, fallback to reserved.
func remoteValue(fc *fetch.Client, ctx context.Context, rm config.Remote, ip, country, city, lang, key string) string {
	u := rm.URL
	u = strings.ReplaceAll(u, "[IP]", ip)
	u = strings.ReplaceAll(u, "[COUNTRY]", country)
	u = strings.ReplaceAll(u, "[CITY]", city)
	u = strings.ReplaceAll(u, "[LANG]", lang)
	u = strings.ReplaceAll(u, "[KEY]", key)
	ttl := time.Duration(rm.Cache) * time.Second
	val, err := fc.GetCached(ctx, "remote:"+u, ttl, func(lctx context.Context) (string, error) {
		body, err := fc.Get(lctx, u)
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
// response body alongside the status error, so the previous condition — an
// error AND an empty body — let an upstream error page through: a partner's 502
// was rendered into the visitor's response under our own 200, and the event log
// recorded an ordinary serve. A failed fetch has no usable body, whatever its
// length.
//
// Spelled out in words on purpose: gofmt reformats doc comments and turns a
// pair of straight single quotes into a typographic one, which silently rewrote
// this sentence into nonsense once already.
func curlBody(fc *fetch.Client, ctx context.Context, url, rules string, curlCacheMin int) (string, bool) {
	ttl := time.Duration(curlCacheMin) * time.Minute
	body, err := fc.GetCached(ctx, "curl:"+url, ttl, func(lctx context.Context) (string, error) {
		return fc.Get(lctx, url)
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
