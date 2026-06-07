package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGroups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	data := `[
	  {"id":"demo","name":"Demo","status":true,"redirect":"http_redirect","out":"https://x/?k=[KEY]",
	   "streams":[{"name":"s1","status":true,"rules":{"country":{"flag":2,"raw":"ru","values":["ru"]}},
	               "out":{"redirect":"show_text","out":"hi"}}]}
	]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := LoadGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	grp, ok := g.Get("demo")
	if !ok {
		t.Fatal("demo group did not load")
	}
	if grp.Redirect != "http_redirect" || len(grp.Streams) != 1 {
		t.Errorf("invalid group data: %+v", grp)
	}
	if grp.Streams[0].Out.Out != "hi" || grp.Streams[0].Rules.Country.Flag != FlagB {
		t.Errorf("invalid stream: %+v", grp.Streams[0])
	}
	if got := len(g.List()); got != 1 {
		t.Errorf("List() = %d; want 1", got)
	}
	if _, ok := g.Get("nope"); ok {
		t.Error("nonexistent group must not be found")
	}
}

func TestGroupAliases(t *testing.T) {
	g := NewGroups(&Group{ID: "promo", Aliases: []string{"p", "sale"}})
	for _, key := range []string{"promo", "p", "sale"} {
		if _, ok := g.Get(key); !ok {
			t.Errorf("group must be found by %q", key)
		}
	}
	// aliases do not duplicate the group in List
	if got := len(g.List()); got != 1 {
		t.Errorf("List() = %d; want 1", got)
	}
}
