package render

import "strings"

// cacheableMacros — the macros whose value is low-cardinality: a template
// containing only these expands into a small, bounded set of distinct strings.
//
// A whitelist, not a blacklist. Everything else — [IP], [KEY], [CID],
// [USERAGENT], [DOMAIN], [PATH], [PAR-n], every RAND* — takes a value that is
// per-visitor or outright random, so a cache keyed on the expanded string grows
// by one entry per request and never hits. Listing those instead would mean the
// next macro added to scalars() leaks silently by default; this way it simply
// stops being cached until someone decides otherwise.
//
// [()COUNTRY()] and [()CITY()] are separate literals for the same values and
// must be listed too — they are why this function lives next to scalars()
// rather than in the caller.
var cacheableMacros = []string{
	"[()COUNTRY()]",
	"[()CITY()]",
	"[COUNTRY]",
	"[CITY]",
	"[REGION]",
	"[LANG]",
	"[DEVICE]",
	"[OPERATOR]",
}

// Cacheable reports whether the expansion of tmpl is worth caching, i.e.
// whether every macro in it is low-cardinality.
//
// It works by removing the whitelisted literals and asking whether any '[' is
// left. Matching macro tokens with a regexp would be wrong: RAND* arguments are
// taken from the template and contain macros of their own, as in
// [RANDLINE-([COUNTRY].dat)-1], so a `\[[^\]]*\]` pattern stops at the inner
// bracket and reads a macro that is not there. Stripping what is known and
// looking at the remainder needs no nesting rules and errs toward not caching.
//
// A template with a bare '[' that is not a macro at all is reported as not
// cacheable. That is the safe direction: the cost is a cache miss, not a leak.
func Cacheable(tmpl string) bool {
	if !strings.Contains(tmpl, "[") {
		return true
	}
	s := tmpl
	for _, m := range cacheableMacros {
		s = strings.ReplaceAll(s, m, "")
	}
	return !strings.Contains(s, "[")
}
