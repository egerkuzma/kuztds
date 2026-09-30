// Package router selects a stream for a visitor by the stream's rules.
//
// Rules are applied as a set of predicates in a loop: the first stream
// that passes all filters wins.
package router

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/geo"
)

// Empty — the "no data" value.
const Empty = "-"

// Visitor — visitor attributes by which streams are filtered.
type Visitor struct {
	Lang       string
	Country    string
	City       string
	Region     string
	UA         string
	Referer    string
	Domain     string
	Key        string
	Device     string // computer/phone/tablet
	Operator   string // beeline/megafon/... or Empty
	OS         string // Android, iOS, Windows, ...
	OSVersion  string
	Browser    string // Chrome, Safari, ...
	BrowserVer string
	Brand      string // Apple, Samsung, ...
	Unique     bool
	IP         netip.Addr

	ASN      uint32     // autonomous system number, 0 = unknown
	Org      string     // its organization, or Empty
	Timezone string     // IANA name ("Europe/Moscow"), or Empty
	TZOffset string     // current UTC offset ("+3", "+5:30"), or Empty
	Query    url.Values // query-string variables of the request
}

// IPLister checks whether an IP belongs to a named list (implemented by ipindex.Set).
type IPLister interface {
	Lookup(name string, ip netip.Addr) (string, bool)
}

// Limiter checks whether the stream's serve limit is exhausted (implemented in phase 4).
type Limiter interface {
	Allowed(stream string, rule config.LimitRule) bool
}

// Deps — external dependencies of the router (may be nil — then the corresponding
// checks are skipped).
type Deps struct {
	IP      IPLister
	Limiter Limiter
	Now     func() time.Time // for scheduling; nil → time.Now
}

// Select returns the first active stream of the group that passed all filters.
func Select(g *config.Group, v Visitor, deps Deps) (*config.Stream, bool) {
	if g == nil {
		return nil, false
	}
	for i := range g.Streams {
		s := &g.Streams[i]
		if !s.Status {
			continue
		}
		if matches(s, v, deps) {
			return s, true
		}
	}
	return nil, false
}

// matches returns true if the visitor passes all of the stream's filters.
func matches(s *config.Stream, v Visitor, deps Deps) bool { return Why(s, v, deps) == "" }

// Why names the first filter of the stream that rejects the visitor, or
// returns "" when the stream matches. The names are the rule keys of the
// config ("country", "ua_text", "device", …), so the admin panel can point at
// the condition that stopped a visitor.
//
// The limit is checked last and only through deps.Limiter, which consumes the
// counter: pass a nil Limiter to ask "would it match" without spending it.
func Why(s *config.Stream, v Visitor, deps Deps) string {
	r := &s.Rules

	// lang/country — contains semantics).
	// raw, or the joined values (the config may set only values).
	langRaw := orJoin(r.Lang.Raw, r.Lang.Values)
	if rejectList(r.Lang.Flag, langRaw != "", containsCI(langRaw, v.Lang)) {
		return "lang"
	}
	countryRaw := orJoin(r.Country.Raw, r.Country.Values)
	if rejectList(r.Country.Flag, countryRaw != "", containsCI(countryRaw, v.Country)) {
		return "country"
	}
	// city/region — element-wise exact match.
	if rejectList(r.City.Flag, len(r.City.Values) > 0, exactAny(r.City.Values, v.City)) {
		return "city"
	}
	if rejectList(r.Region.Flag, len(r.Region.Values) > 0, exactAny(r.Region.Values, v.Region)) {
		return "region"
	}
	// ua/referer/key — /regex/ or substring; domain — /regex/ or exact.
	if rejectList(r.UAText.Flag, cfgd(r.UAText), textOrRegex(r.UAText, v.UA)) {
		return "ua_text"
	}
	if rejectList(r.Referer.Flag, cfgd(r.Referer), textOrRegex(r.Referer, v.Referer)) {
		return "referer_text"
	}
	if rejectList(r.Key.Flag, cfgd(r.Key), textOrRegex(r.Key, v.Key)) {
		return "key_text"
	}
	if rejectList(r.Domain.Flag, cfgd(r.Domain), exactOrRegex(r.Domain, v.Domain)) {
		return "domain_text"
	}

	// OS/browser — substring over "name version"; brand — exact match.
	if rejectList(r.OS.Flag, len(r.OS.Values) > 0, substrAny(r.OS.Values, joinVer(v.OS, v.OSVersion))) {
		return "os"
	}
	if rejectList(r.Browser.Flag, len(r.Browser.Values) > 0, substrAny(r.Browser.Values, joinVer(v.Browser, v.BrowserVer))) {
		return "browser"
	}
	if rejectList(r.Brand.Flag, len(r.Brand.Values) > 0, exactAny(r.Brand.Values, v.Brand)) {
		return "brand"
	}

	// Schedule by days of the week.
	if r.Schedule.Enabled {
		now := time.Now()
		if deps.Now != nil {
			now = deps.Now()
		}
		if !r.Schedule.Days[int(now.Weekday())] {
			return "schedule"
		}
	}

	// Devices: FlagA(0) blocks the given type.
	if r.Computer == config.FlagA && v.Device == "computer" {
		return "device"
	}
	if r.Phone == config.FlagA && v.Device == "phone" {
		return "device"
	}
	if r.Tablet == config.FlagA && v.Device == "tablet" {
		return "device"
	}

	// Operators: FlagA(0) blocks a specific operator; FlagB(1) — whitelist:
	// if at least one FlagB operator is set, the visitor must belong to one
	// of them (an empty operator is not in the list).
	requireSet := map[string]bool{}
	for op, fl := range r.Operators {
		switch fl {
		case config.FlagA:
			if v.Operator == op {
				return "operators"
			}
		case config.FlagB:
			requireSet[op] = true
		}
	}
	if len(requireSet) > 0 && (emptyish(v.Operator) || !requireSet[v.Operator]) {
		return "operators"
	}

	// Uniqueness.
	switch r.Unique {
	case config.FlagA: // unique only
		if !v.Unique {
			return "unique"
		}
	case config.FlagB: // non-unique only
		if v.Unique {
			return "unique"
		}
	}

	// Yandex.Browser.
	switch r.YaBrowser {
	case config.FlagA: // exclude Yandex.Browser
		if matchYaBrowser(v.UA) {
			return "yabrowser"
		}
	case config.FlagB: // Yandex.Browser only
		if !matchYaBrowser(v.UA) {
			return "yabrowser"
		}
	}

	// Presence of a referer.
	switch r.HasReferer {
	case config.FlagA: // require a referer
		if emptyish(v.Referer) {
			return "has_referer"
		}
	case config.FlagB: // require absence of a referer
		if !emptyish(v.Referer) {
			return "has_referer"
		}
	}

	// IP list (via ipindex). File name without the .dat extension.
	if r.IPList.Flag != config.FlagOff && r.IPList.File != "" && deps.IP != nil && v.IP.IsValid() {
		_, in := deps.IP.Lookup(strings.TrimSuffix(r.IPList.File, ".dat"), v.IP)
		if rejectList(r.IPList.Flag, true, in) {
			return "ip_list"
		}
	}

	// Network: ASN by number ("15169" or "AS15169"), organization by /regex/
	// or substring.
	if rejectList(r.ASN.Flag, len(r.ASN.Values) > 0, asnAny(r.ASN.Values, v.ASN)) {
		return "asn"
	}
	if rejectList(r.Org.Flag, cfgd(r.Org), !emptyish(v.Org) && textOrRegex(r.Org, v.Org)) {
		return "org"
	}
	// Time zone: a UTC offset ("+3", "+5:30") or an IANA name ("Europe/Moscow").
	if rejectList(r.Timezone.Flag, len(r.Timezone.Values) > 0, tzAny(r.Timezone.Values, v.Timezone, v.TZOffset)) {
		return "timezone"
	}
	// Query-string variable: "name" (present and non-empty) or "name=value".
	if rejectList(r.Query.Flag, len(r.Query.Values) > 0, queryAny(r.Query.Values, v.Query)) {
		return "get"
	}

	// Limits (Redis in phase 4).
	if r.Limit.Enabled && deps.Limiter != nil {
		if !deps.Limiter.Allowed(s.Name, r.Limit) {
			return "limit"
		}
	}

	return ""
}

// rejectList implements the common semantics of list filters:
//
//	FlagA(0): exclude on match (blacklist)
//	FlagB(1): exclude on the ABSENCE of a match (whitelist)
//	FlagOff(2) or empty config: the filter is not applied
func rejectList(flag config.Flag, configured, matched bool) bool {
	if !configured {
		return false
	}
	switch flag {
	case config.FlagA:
		return matched
	case config.FlagB:
		return !matched
	}
	return false
}

func emptyish(s string) bool { return s == "" || s == Empty }

// cfgd: the filter is configured if raw or values is set.
func cfgd(f config.ListFilter) bool { return f.Raw != "" || len(f.Values) > 0 }

// orJoin returns raw, or the values joined by commas.
func orJoin(raw string, values []string) string {
	if raw != "" {
		return raw
	}
	return strings.Join(values, ",")
}

// containsCI: value as a substring of the config string (case-insensitive).
func containsCI(raw, value string) bool {
	if raw == "" || emptyish(value) {
		return false
	}
	return strings.Contains(strings.ToLower(raw), strings.ToLower(value))
}

// substrAny: any of the values is a substring of subject (case-insensitive).
func substrAny(values []string, subject string) bool {
	if subject == "" {
		return false
	}
	ls := strings.ToLower(subject)
	for _, e := range values {
		if e != "" && strings.Contains(ls, strings.ToLower(e)) {
			return true
		}
	}
	return false
}

// joinVer joins name and version ("Android"+"13" → "android 13").
func joinVer(name, ver string) string {
	return strings.TrimSpace(name + " " + ver)
}

// exactAny: exact (case-insensitive) match of value with one of the elements.
func exactAny(values []string, value string) bool {
	if emptyish(value) {
		return false
	}
	for _, e := range values {
		if strings.EqualFold(strings.TrimSpace(e), value) {
			return true
		}
	}
	return false
}

// asnAny: the visitor's AS number is one of the values ("15169" or "AS15169").
func asnAny(values []string, asn uint32) bool {
	if asn == 0 {
		return false
	}
	for _, e := range values {
		e = strings.TrimSpace(e)
		if len(e) > 2 && (e[0] == 'A' || e[0] == 'a') && (e[1] == 'S' || e[1] == 's') {
			e = e[2:]
		}
		if n, err := strconv.ParseUint(e, 10, 32); err == nil && uint32(n) == asn {
			return true
		}
	}
	return false
}

// tzAny: a value containing "/" is an IANA zone name and is compared with the
// visitor's zone; anything else is a UTC offset and is compared with the
// visitor's current offset after normalization ("UTC+3", "+03:00" → "+3").
func tzAny(values []string, zone, offset string) bool {
	for _, e := range values {
		e = strings.TrimSpace(e)
		if strings.Contains(e, "/") {
			if !emptyish(zone) && strings.EqualFold(e, zone) {
				return true
			}
			continue
		}
		if emptyish(offset) {
			continue
		}
		if n, ok := geo.NormOffset(e); ok && n == offset {
			return true
		}
	}
	return false
}

// queryAny: "name" matches a present, non-empty variable; "name=value"
// matches its value exactly (case-insensitive).
func queryAny(values []string, q url.Values) bool {
	if len(q) == 0 {
		return false
	}
	for _, e := range values {
		name, want, hasValue := strings.Cut(strings.TrimSpace(e), "=")
		if name == "" {
			continue
		}
		got := q.Get(name)
		if got == "" {
			continue
		}
		if !hasValue || strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}

// textOrRegex: if Raw starts with '/', it is a regexp; otherwise — a substring over any of Values.
func textOrRegex(f config.ListFilter, subject string) bool {
	if strings.HasPrefix(f.Raw, "/") {
		if re := compile(f.Raw); re != nil {
			return re.MatchString(subject)
		}
		return false
	}
	ls := strings.ToLower(subject)
	for _, e := range f.Values {
		if e != "" && strings.Contains(ls, strings.ToLower(e)) {
			return true
		}
	}
	return false
}

// exactOrRegex: /regex/ or an exact match over any of Values (for domain).
func exactOrRegex(f config.ListFilter, subject string) bool {
	if strings.HasPrefix(f.Raw, "/") {
		if re := compile(f.Raw); re != nil {
			return re.MatchString(subject)
		}
		return false
	}
	return exactAny(f.Values, subject)
}

// matchYaBrowser detects Yandex.Browser.
// Correct selection logic for Yandex.Browser.
func matchYaBrowser(ua string) bool {
	l := strings.ToLower(ua)
	return strings.Contains(l, "yabrowser") || strings.Contains(l, "yandexsearchbrowser")
}

// --- cache of compiled regexps ---

var reCache sync.Map // string -> *regexp.Regexp (nil on compile error)

// compile translates /pattern/flags into a Go regexp (RE2).
// The i flag (case-insensitive) is supported. PCRE specifics are not translated —
// such patterns won't compile and the filter won't match them (logged on load).
func compile(raw string) *regexp.Regexp {
	if v, ok := reCache.Load(raw); ok {
		if v == nil {
			return nil
		}
		return v.(*regexp.Regexp)
	}
	re := buildRegexp(raw)
	if re == nil {
		reCache.Store(raw, nil)
		return nil
	}
	reCache.Store(raw, re)
	return re
}

func buildRegexp(raw string) *regexp.Regexp {
	if len(raw) < 2 || raw[0] != '/' {
		return nil
	}
	end := strings.LastIndexByte(raw, '/')
	if end <= 0 {
		return nil
	}
	pattern := raw[1:end]
	flags := raw[end+1:]
	if strings.ContainsRune(flags, 'i') {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return re
}
