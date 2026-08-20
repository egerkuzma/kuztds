package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/ipindex"
)

// save_ip dedupes against the in-memory index, which only catches up on the
// next hot-reload (a minute by default). Until then every hit from the same
// crawler IP used to append another line, so a busy site grew ip_<se>.dat by
// thousands of duplicate lines per minute.
func TestSaveBotIPWritesEachIPOnce(t *testing.T) {
	dir := t.TempDir()
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, log)
	bots := config.Bots{CheckUA: true, SaveIP: true}
	ip := netip.MustParseAddr("66.249.66.1")

	savedBotIPs.Clear()
	for i := 0; i < 50; i++ {
		detectBot(lists, sigs, dir, ip, uaGoogleBot, "-", "en", bots)
	}
	// a second crawler IP still gets recorded
	detectBot(lists, sigs, dir, netip.MustParseAddr("66.249.66.2"), uaGoogleBot, "-", "en", bots)

	b, err := os.ReadFile(filepath.Join(dir, "ip_google.dat"))
	if err != nil {
		t.Fatalf("ip_google.dat not written: %v", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(b)))
	if len(lines) != 2 {
		t.Errorf("50 hits from 2 IPs → 2 lines, got %d: %q", len(lines), string(b))
	}
	if !strings.Contains(string(b), "66.249.66.1") || !strings.Contains(string(b), "66.249.66.2") {
		t.Errorf("both crawler IPs must be recorded, got %q", string(b))
	}
}

// An IP already present in the list is never appended again.
func TestSaveBotIPSkipsKnownIP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ip_google.dat"), []byte("66.249.66.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := discardLog()
	lists := ipindex.NewSet(dir, log)
	lists.Load(ipLists...)
	sigs := detect.NewSignatures(dir, log)

	savedBotIPs.Clear()
	for i := 0; i < 10; i++ {
		detectBot(lists, sigs, dir, netip.MustParseAddr("66.249.66.1"), uaGoogleBot, "-", "en",
			config.Bots{CheckUA: true, SaveIP: true})
	}
	b, _ := os.ReadFile(filepath.Join(dir, "ip_google.dat"))
	if got := strings.TrimSpace(string(b)); got != "66.249.66.1" {
		t.Errorf("a known IP must not be appended again, file = %q", got)
	}
}
