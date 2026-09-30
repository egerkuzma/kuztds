package main

import (
	"net/http"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

func linked(groups ...*config.Group) *config.Groups { return config.NewGroups(groups...) }

func text(name, out string, r config.Rules) config.Stream {
	return config.Stream{Name: name, Status: true, Rules: r, Out: config.Output{Redirect: "show_text", Out: out}}
}

func link(name, target string, r config.Rules) config.Stream {
	return config.Stream{Name: name, Status: true, Rules: r, Out: config.Output{Redirect: "group", Out: target}}
}

// A stream of type "group" hands the visitor to another group, whose streams
// are then tried with the same visitor.
func TestGroupLink(t *testing.T) {
	phones := config.Rules{Computer: config.FlagA, Tablet: config.FlagA}
	gg := linked(
		&config.Group{ID: "entry", Name: "entry", Status: true, Redirect: "show_text", Out: "ENTRY DEFAULT", Streams: []config.Stream{
			link("to_mobile", "mobile", phones),
			text("desktop", "DESKTOP", config.Rules{}),
		}},
		&config.Group{ID: "mobile", Name: "mobile", Status: true, Redirect: "show_text", Out: "MOBILE DEFAULT", Streams: []config.Stream{
			text("ios", "IOS", config.Rules{OS: config.ListFilter{Flag: config.FlagB, Values: []string{"iOS"}}}),
		}},
	)
	h := testEnv(t, gg)
	cases := []struct{ ua, body, stream string }{
		{uaIPhone, "IOS", "ios"},            // followed the link, matched there
		{uaSamsung, "MOBILE DEFAULT", "-"},  // followed the link, fell to that group's default
		{uaWinChrome, "DESKTOP", "desktop"}, // did not match the link's conditions
	}
	for _, c := range cases {
		rec := do(t, h, "/entry", "8.8.8.8", map[string]string{"User-Agent": c.ua})
		if rec.Body.String() != c.body || rec.Header().Get("X-Kuztds-Stream") != c.stream {
			t.Errorf("%.30s… → %q (stream %q); want %q (%q)", c.ua, rec.Body.String(), rec.Header().Get("X-Kuztds-Stream"), c.body, c.stream)
		}
	}
}

// A group's own default can be a link too: "nothing matched here → go there".
func TestGroupLinkAsDefault(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Status: true, Redirect: "group", Out: " b "},
		&config.Group{ID: "b", Status: true, Redirect: "show_text", Out: "B"},
	)
	if got := do(t, testEnv(t, gg), "/a", "8.8.8.8", map[string]string{"User-Agent": uaWinChrome}).Body.String(); got != "B" {
		t.Errorf("body = %q; want B", got)
	}
}

// Links that cannot be followed answer like an unknown group; loops end.
func TestGroupLinkBroken(t *testing.T) {
	ua := map[string]string{"User-Agent": uaWinChrome}
	trash404 := func(d *engineDeps) { d.trashMode = "3" }

	missing := linked(&config.Group{ID: "a", Status: true, Redirect: "group", Out: "nowhere"})
	disabled := linked(
		&config.Group{ID: "a", Status: true, Redirect: "group", Out: "b"},
		&config.Group{ID: "b", Status: false, Redirect: "show_text", Out: "B"},
	)
	self := linked(&config.Group{ID: "a", Status: true, Redirect: "group", Out: "a"})
	loop := linked(
		&config.Group{ID: "a", Status: true, Redirect: "group", Out: "b"},
		&config.Group{ID: "b", Status: true, Redirect: "group", Out: "a"},
	)
	for name, gg := range map[string]*config.Groups{"missing": missing, "disabled": disabled, "self": self, "loop": loop} {
		rec := do(t, testEnv(t, gg, trash404), "/a", "8.8.8.8", ua)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s target: status %d, body %q; want the trash answer", name, rec.Code, rec.Body.String())
		}
	}

	// A chain within the limit is followed to its end.
	chain := linked(
		&config.Group{ID: "a", Status: true, Redirect: "group", Out: "b"},
		&config.Group{ID: "b", Status: true, Redirect: "group", Out: "c"},
		&config.Group{ID: "c", Status: true, Redirect: "group", Out: "d"},
		&config.Group{ID: "d", Status: true, Redirect: "show_text", Out: "D"},
	)
	if got := do(t, testEnv(t, chain), "/a", "8.8.8.8", ua).Body.String(); got != "D" {
		t.Errorf("three hops: %q; want D", got)
	}
}

// The event of a forwarded visit is logged under the group that served it and
// names the stream that sent it on — the first one, when links are chained —
// so the sending flow's statistics can count it too.
func TestGroupLinkIsLogged(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "stop", Streams: []config.Stream{link("to_b", "b", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "group", Out: "c"},
		&config.Group{ID: "c", Name: "C", Status: true, Redirect: "stop", Streams: []config.Stream{text("final", "C", config.Rules{})}},
	)
	h, ins, stop := curlEnv(t, gg, "0")
	ua := map[string]string{"User-Agent": uaWinChrome}
	do(t, h, "/a", "8.8.8.8", ua) // a/to_b → b (default link) → c/final
	do(t, h, "/c", "8.8.4.4", ua) // straight in
	stop()
	if len(ins.events) != 2 {
		t.Fatalf("events = %d; want 2", len(ins.events))
	}
	byIP := map[string]int{}
	for i, e := range ins.events {
		byIP[e.IP] = i
	}
	if e := ins.events[byIP["8.8.8.8"]]; e.GroupID != "c" || e.Stream != "final" || e.Via != "a/to_b" {
		t.Errorf("forwarded visit logged as %s/%s via %q; want c/final via a/to_b", e.GroupID, e.Stream, e.Via)
	}
	if e := ins.events[byIP["8.8.4.4"]]; e.Via != "" {
		t.Errorf("a direct visit has via %q", e.Via)
	}
}
