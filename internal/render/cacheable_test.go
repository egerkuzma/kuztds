package render

import "testing"

// TestCacheableMacrosExistInScalars catches a typo in the whitelist. A
// misspelled entry never matches, so the template it was meant to allow stops
// being cached — nothing fails, nothing is logged, the upstream just gets more
// traffic than it should. The reverse check is deliberately absent: a macro
// missing from the whitelist is already safe, that is what the whitelist is for.
func TestCacheableMacrosExistInScalars(t *testing.T) {
	known := map[string]bool{}
	for _, m := range (MacroDeps{}).scalars() {
		known[m.name] = true
	}
	for _, name := range cacheableMacros {
		if !known[name] {
			t.Errorf("whitelisted macro %q is not in scalars() — typo or renamed macro", name)
		}
	}
}

func TestCacheable(t *testing.T) {
	cases := []struct {
		tmpl string
		want bool
	}{
		{"https://p.example/offer", true},
		{"https://p.example/o?c=[COUNTRY]&l=[LANG]", true},
		{"https://p.example/o?c=[()COUNTRY()]&r=[REGION]&d=[DEVICE]&op=[OPERATOR]", true},
		{"https://p.example/o?c=[CITY]", true},

		{"https://p.example/o?ip=[IP]", false},
		{"https://p.example/o?q=[KEY]", false},
		{"https://p.example/o?cid=[CID]", false},
		{"https://p.example/o?ua=[USERAGENT]", false},
		{"https://p.example/o?d=[DOMAIN]", false},
		{"https://p.example/o?p=[PATH]", false},
		{"https://p.example/o?s=[PAR-1]", false},
		{"https://p.example/o?s=[PAR-5]", false},

		// RAND* are matched by regexp, not by the scalars table, so they never
		// reach the whitelist — the leftover '[' is what rejects them.
		{"https://p.example/o?n=[RANDNUM-1-1000000]", false},
		{"https://p.example/o?s=[RANDSTR-(abc)-8]", false},
		{"https://p.example/o?l=[RANDLINE-(list.dat)-1]", false},
		{"https://p.example/o?l=[RANDDFL-(list.dat)-1]", false},

		// Nested: the inner [COUNTRY] is whitelisted, the RAND* wrapper is not.
		// A regexp over macro tokens would stop at the inner ']' and miss this.
		{"https://p.example/o?l=[RANDLINE-([COUNTRY].dat)-1]", false},

		// Mixed: one bad macro is enough.
		{"https://p.example/o?c=[COUNTRY]&ip=[IP]", false},

		// A bare bracket is not a macro, but it is also not worth guessing at.
		{"https://p.example/o?a[]=1", false},
	}
	for _, c := range cases {
		if got := Cacheable(c.tmpl); got != c.want {
			t.Errorf("Cacheable(%q) = %v, want %v", c.tmpl, got, c.want)
		}
	}
}
