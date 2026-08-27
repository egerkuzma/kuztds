package fetch

import (
	"strconv"
	"testing"
	"time"
)

// clock is a hand-driven time source: the cache must be testable without
// sleeping for a TTL to pass.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestClient() (*Client, *clock) {
	c := New("")
	ck := &clock{t: time.Unix(1_700_000_000, 0)}
	c.now = ck.now
	return c, ck
}

// TestGetCachedTTLZeroDoesNotStore pins the primary fix: a caller that declines
// to cache must leave nothing behind. This is what stops a per-visitor template
// from adding one permanent entry per request.
func TestGetCachedTTLZeroDoesNotStore(t *testing.T) {
	c, _ := newTestClient()
	for i := 0; i < 1000; i++ {
		_, err := c.GetCached("k"+strconv.Itoa(i), 0, func() (string, error) { return "v", nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := c.Len(); n != 0 {
		t.Errorf("ttl=0 stored %d entries, want 0", n)
	}
}

// TestGetCachedDropsExpiredOnLookup is the leak as it actually was: entries
// stopped being served when they expired but stayed in the map forever.
func TestGetCachedDropsExpiredOnLookup(t *testing.T) {
	c, ck := newTestClient()
	load := func() (string, error) { return "v", nil }

	if _, err := c.GetCached("k", time.Minute, load); err != nil {
		t.Fatal(err)
	}
	if n := c.Len(); n != 1 {
		t.Fatalf("after store: %d entries, want 1", n)
	}

	// Past the TTL the entry is dead weight; asking for it must clear it, not
	// just refuse to serve it.
	ck.t = ck.t.Add(2 * time.Minute)
	if _, err := c.GetCached("k", time.Minute, load); err != nil {
		t.Fatal(err)
	}
	if n := c.Len(); n != 1 {
		t.Errorf("after refetch: %d entries, want 1 (the fresh one)", n)
	}

	// An expired entry nobody refetches is only cleared if it is looked up. The
	// cap is what covers the rest — see TestGetCachedCap.
	c2, ck2 := newTestClient()
	if _, err := c2.GetCached("gone", time.Minute, load); err != nil {
		t.Fatal(err)
	}
	ck2.t = ck2.t.Add(2 * time.Minute)
	if _, err := c2.GetCached("gone", 0, load); err != nil {
		t.Fatal(err)
	}
	if n := c2.Len(); n != 1 {
		t.Errorf("ttl=0 lookup touched the map: %d entries, want 1 untouched", n)
	}
}

// TestGetCachedCap is the backstop: even a caller that caches everything cannot
// grow the map without bound.
func TestGetCachedCap(t *testing.T) {
	c, _ := newTestClient()
	load := func() (string, error) { return "v", nil }
	for i := 0; i < maxEntries+100; i++ {
		if _, err := c.GetCached("k"+strconv.Itoa(i), time.Hour, load); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.Len(); n > maxEntries {
		t.Errorf("cache holds %d entries, cap is %d", n, maxEntries)
	}
	if n := c.Len(); n == 0 {
		t.Error("cache emptied itself entirely")
	}
}

// TestGetCachedStillCaches guards the other direction: the bound must not turn
// the cache off. A whitelist typo or an over-eager cap would show up here.
func TestGetCachedStillCaches(t *testing.T) {
	c, _ := newTestClient()
	calls := 0
	load := func() (string, error) { calls++; return "v", nil }
	for i := 0; i < 10; i++ {
		if _, err := c.GetCached("same", time.Hour, load); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("load called %d times, want 1", calls)
	}
}
