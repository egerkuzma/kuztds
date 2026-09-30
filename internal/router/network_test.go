package router

import (
	"net/url"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

func rs(r config.Rules) *config.Stream {
	return &config.Stream{Name: "s", Status: true, Rules: r}
}

func lf(flag config.Flag, vals ...string) config.ListFilter {
	raw := ""
	if len(vals) == 1 {
		raw = vals[0]
	}
	return config.ListFilter{Flag: flag, Values: vals, Raw: raw}
}

func TestASNFilter(t *testing.T) {
	google := Visitor{ASN: 15169, Org: "Google LLC"}
	other := Visitor{ASN: 1221, Org: "Telstra Pty Ltd"}
	unknown := Visitor{Org: Empty}

	only := rs(config.Rules{ASN: lf(config.FlagB, "AS15169", "as32934")})
	if !matches(only, google, Deps{}) {
		t.Error("whitelist: AS15169 must match a visitor in AS15169")
	}
	if matches(only, other, Deps{}) || matches(only, unknown, Deps{}) {
		t.Error("whitelist: another or an unknown AS must not match")
	}
	if Why(only, other, Deps{}) != "asn" {
		t.Errorf("Why = %q; want asn", Why(only, other, Deps{}))
	}

	except := rs(config.Rules{ASN: lf(config.FlagA, "15169")})
	if matches(except, google, Deps{}) {
		t.Error("blacklist: a bare number must exclude that AS")
	}
	if !matches(except, other, Deps{}) || !matches(except, unknown, Deps{}) {
		t.Error("blacklist: everyone else, including an unknown AS, passes")
	}

	// Garbage in the list is ignored, not treated as AS 0.
	junk := rs(config.Rules{ASN: lf(config.FlagB, "google", "AS")})
	if matches(junk, unknown, Deps{}) {
		t.Error("an unparseable ASN value must never match an unknown AS")
	}
}

func TestOrgFilter(t *testing.T) {
	v := Visitor{ASN: 16509, Org: "Amazon.com, Inc."}
	if !matches(rs(config.Rules{Org: lf(config.FlagB, "amazon", "google")}), v, Deps{}) {
		t.Error("substring, case-insensitive")
	}
	if !matches(rs(config.Rules{Org: lf(config.FlagB, "/^(amazon|google|microsoft)/i")}), v, Deps{}) {
		t.Error("regex")
	}
	dc := rs(config.Rules{Org: lf(config.FlagA, "/hosting|cloud|amazon/i")})
	if matches(dc, v, Deps{}) {
		t.Error("blacklist by regex must exclude the visitor")
	}
	// No ASN database → org unknown → a whitelist cannot match, a blacklist passes.
	none := Visitor{Org: Empty}
	if matches(rs(config.Rules{Org: lf(config.FlagB, "-")}), none, Deps{}) {
		t.Error("the \"-\" placeholder is not an organization name")
	}
	if !matches(dc, none, Deps{}) {
		t.Error("an unknown organization is not on a blacklist")
	}
}

func TestTimezoneFilter(t *testing.T) {
	msk := Visitor{Timezone: "Europe/Moscow", TZOffset: "+3"}
	delhi := Visitor{Timezone: "Asia/Kolkata", TZOffset: "+5:30"}
	none := Visitor{Timezone: Empty, TZOffset: Empty}

	for _, val := range []string{"+3", "3", "UTC+3", "+03:00", "europe/moscow"} {
		s := rs(config.Rules{Timezone: lf(config.FlagB, val)})
		if !matches(s, msk, Deps{}) {
			t.Errorf("%q must match a Moscow visitor", val)
		}
		if matches(s, delhi, Deps{}) {
			t.Errorf("%q must not match a Delhi visitor", val)
		}
		if matches(s, none, Deps{}) {
			t.Errorf("%q must not match an unknown time zone", val)
		}
	}
	if !matches(rs(config.Rules{Timezone: lf(config.FlagB, "+3", "+5:30")}), delhi, Deps{}) {
		t.Error("any of the listed offsets")
	}
	if matches(rs(config.Rules{Timezone: lf(config.FlagA, "+5:30")}), delhi, Deps{}) {
		t.Error("blacklist")
	}
	// An IANA name never matches by offset: Minsk is +3 too, and is not Moscow.
	minsk := Visitor{Timezone: "Europe/Minsk", TZOffset: "+3"}
	if matches(rs(config.Rules{Timezone: lf(config.FlagB, "Europe/Moscow")}), minsk, Deps{}) {
		t.Error("a zone name is compared with the zone, not with its offset")
	}
}

func TestQueryFilter(t *testing.T) {
	v := Visitor{Query: url.Values{"utm_source": {"Facebook"}, "sub": {"42"}, "empty": {""}}}
	cases := []struct {
		val  string
		want bool
	}{
		{"utm_source", true},
		{"utm_source=facebook", true}, // value is case-insensitive
		{"utm_source=google", false},
		{"sub=42", true},
		{"sub=4", false}, // exact, not a prefix
		{"missing", false},
		{"empty", false}, // present but empty is not "set"
		{"=x", false},
	}
	for _, c := range cases {
		got := matches(rs(config.Rules{Query: lf(config.FlagB, c.val)}), v, Deps{})
		if got != c.want {
			t.Errorf("get %q → %v; want %v", c.val, got, c.want)
		}
	}
	if matches(rs(config.Rules{Query: lf(config.FlagB, "utm_source")}), Visitor{}, Deps{}) {
		t.Error("no query string at all must not satisfy a whitelist")
	}
	if !matches(rs(config.Rules{Query: lf(config.FlagA, "debug")}), v, Deps{}) {
		t.Error("blacklist on an absent variable passes")
	}
}

// Why names the rule that stopped the visitor, using the config's own keys,
// and agrees with matches on every outcome.
func TestWhyNamesTheRejectingRule(t *testing.T) {
	v := Visitor{Country: "ru", Device: "phone", Unique: true, UA: "Mozilla", Referer: Empty, Lang: "ru"}
	cases := []struct {
		rules config.Rules
		want  string
	}{
		{config.Rules{}, ""},
		{config.Rules{Country: lf(config.FlagB, "us")}, "country"},
		{config.Rules{Country: lf(config.FlagB, "ru"), Phone: config.FlagA}, "device"},
		{config.Rules{Unique: config.FlagB}, "unique"},
		{config.Rules{HasReferer: config.FlagA}, "has_referer"},
		{config.Rules{UAText: lf(config.FlagA, "mozilla")}, "ua_text"},
		{config.Rules{Operators: map[string]config.Flag{"mts": config.FlagB}}, "operators"},
		{config.Rules{Lang: lf(config.FlagB, "en")}, "lang"},
		// the first failing rule in evaluation order wins
		{config.Rules{Lang: lf(config.FlagB, "en"), Country: lf(config.FlagB, "us")}, "lang"},
	}
	for i, c := range cases {
		s := rs(c.rules)
		got := Why(s, v, Deps{})
		if got != c.want {
			t.Errorf("case %d: Why = %q; want %q", i, got, c.want)
		}
		if matches(s, v, Deps{}) != (got == "") {
			t.Errorf("case %d: matches and Why disagree", i)
		}
	}
}
