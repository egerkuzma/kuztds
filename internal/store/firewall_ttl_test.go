package store

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

// A counter key without a TTL never resets, which turns a rate limit into a
// permanent ban. A window of 0 (an enabled firewall with the seconds field left
// blank) must still produce a key that expires.
func TestFirewallZeroWindowStillExpires(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")

	for i := 0; i < 2; i++ {
		if ok, err := c.Firewall(ctx, "g1", ip, 2, 0); !ok || err != nil {
			t.Fatalf("request %d must be allowed: ok=%v err=%v", i+1, ok, err)
		}
	}
	if ok, _ := c.Firewall(ctx, "g1", ip, 2, 0); ok {
		t.Fatal("the 3rd request over the limit must be blocked")
	}
	if ttl := mr.TTL("fw:g1:1.2.3.4"); ttl <= 0 {
		t.Fatalf("firewall key must carry a TTL, got %v", ttl)
	}
	// After the default window the IP must be able to come back.
	mr.FastForward(2 * time.Minute)
	if ok, _ := c.Firewall(ctx, "g1", ip, 2, 0); !ok {
		t.Error("after the window expires the IP must be allowed again")
	}
}

// The same invariant for a per-period stream limit: type 2 with no period must
// not leave an immortal counter that disables the stream forever.
func TestPeriodLimitZeroSecondsStillExpires(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	rule := config.LimitRule{Enabled: true, Type: 2, Count: 1, Seconds: 0}

	if ok, err := c.TakeLimit(ctx, "g1", "s1", rule); !ok || err != nil {
		t.Fatalf("first serve must be allowed: ok=%v err=%v", ok, err)
	}
	if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); ok {
		t.Fatal("after 1 serve the limit (1) must be exhausted")
	}
	if ttl := mr.TTL("lim:p:g1:s1"); ttl <= 0 {
		t.Fatalf("period limit key must carry a TTL, got %v", ttl)
	}
	mr.FastForward(25 * time.Hour)
	if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); !ok {
		t.Error("after the period expires the stream must serve again")
	}
}
