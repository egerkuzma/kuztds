package ipindex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDat(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name+".dat")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetLoadAndLookup(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, dir, "ip_google", "66.249.64.0/19\n")
	writeDat(t, dir, "ip_blacklist", "10.0.0.0/8\n")

	s := NewSet(dir, nil)
	s.Load("ip_google", "ip_blacklist", "ip_missing") // ip_missing absent -> empty

	if _, ok := s.Lookup("ip_google", mustAddr("66.249.70.1")); !ok {
		t.Error("ip_google: expected a hit")
	}
	if _, ok := s.Lookup("ip_blacklist", mustAddr("10.1.2.3")); !ok {
		t.Error("ip_blacklist: expected a hit")
	}
	if _, ok := s.Lookup("ip_missing", mustAddr("1.2.3.4")); ok {
		t.Error("ip_missing: there should be no hits")
	}
	if _, ok := s.Lookup("nonexistent", mustAddr("1.2.3.4")); ok {
		t.Error("an unknown list must be empty")
	}
}

func TestSetReloadOnChange(t *testing.T) {
	dir := t.TempDir()
	writeDat(t, dir, "ip_others", "1.1.1.0/24\n")

	s := NewSet(dir, nil)
	s.Load("ip_others")

	if _, ok := s.Lookup("ip_others", mustAddr("2.2.2.2")); ok {
		t.Fatal("2.2.2.2 must not match the original list")
	}

	// Change the file and force a newer mtime (deterministically).
	p := writeDat(t, dir, "ip_others", "1.1.1.0/24\n2.2.2.0/24\n")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}

	changed := s.Reload()
	if len(changed) != 1 || changed[0] != "ip_others" {
		t.Fatalf("expected reload of ip_others, got %v", changed)
	}
	if _, ok := s.Lookup("ip_others", mustAddr("2.2.2.2")); !ok {
		t.Error("after reload 2.2.2.2 must match")
	}

	// A repeated reload without changes — nothing is re-read.
	if changed := s.Reload(); len(changed) != 0 {
		t.Errorf("an unchanged file must not be re-read, got %v", changed)
	}
}
