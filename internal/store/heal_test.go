package store

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

// Before the counter and its expiry were one script, a lost EXPIRE left an
// immortal key. Such keys are still sitting in the Redis of any deployment that
// upgraded, and "PEXPIRE only on the first increment" never touches them: the
// IP stays banned until someone flushes Redis by hand. The script now stamps a
// TTL on any key it finds without one.
func TestFirewallHealsAnImmortalKey(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")

	// A legacy key: over the limit, no expiry.
	mr.Set("fw:g1:1.2.3.4", "999")
	if ttl := mr.TTL("fw:g1:1.2.3.4"); ttl != 0 {
		t.Fatalf("precondition: key must have no TTL, got %v", ttl)
	}
	if ok, _ := c.Firewall(ctx, "g1", ip, 10, time.Minute); ok {
		t.Fatal("an over-limit key is still over the limit on this request")
	}
	if ttl := mr.TTL("fw:g1:1.2.3.4"); ttl <= 0 {
		t.Fatalf("the immortal key must have been given a TTL, got %v", ttl)
	}
	mr.FastForward(2 * time.Minute)
	if ok, _ := c.Firewall(ctx, "g1", ip, 10, time.Minute); !ok {
		t.Error("after the window the IP must be allowed again — the ban was not permanent")
	}
}

func TestLimitHealsAnImmortalKey(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	rule := config.LimitRule{Enabled: true, Type: 2, Count: 5, Seconds: 60}

	mr.Set("lim:p:g1:s1", "3") // under the limit, but immortal
	if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); !ok {
		t.Fatal("3 < 5: the serve must be allowed")
	}
	if ttl := mr.TTL("lim:p:g1:s1"); ttl <= 0 {
		t.Fatalf("the immortal limit key must have been given a TTL, got %v", ttl)
	}
}
