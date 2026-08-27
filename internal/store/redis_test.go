package store

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/redis/go-redis/v9"
)

func newCounters(t *testing.T) (*Counters, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewCounters(rdb), mr
}

func TestUnique(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")

	first, _ := c.Unique(ctx, "g1", ip, time.Minute)
	if !first {
		t.Error("the first visit must be unique")
	}
	second, _ := c.Unique(ctx, "g1", ip, time.Minute)
	if second {
		t.Error("a repeat visit must not be unique")
	}
	// a different IP — unique again
	if ok, _ := c.Unique(ctx, "g1", netip.MustParseAddr("9.9.9.9"), time.Minute); !ok {
		t.Error("a different IP must be unique")
	}
	// after the TTL expires — unique again
	mr.FastForward(2 * time.Minute)
	if ok, _ := c.Unique(ctx, "g1", ip, time.Minute); !ok {
		t.Error("after the TTL the visit is unique again")
	}
}

func TestFirewall(t *testing.T) {
	c, _ := newCounters(t)
	ctx := context.Background()
	ip := netip.MustParseAddr("1.2.3.4")

	// max=3 per window: the first 3 are allowed, the 4th is not.
	for i := 1; i <= 3; i++ {
		if ok, _ := c.Firewall(ctx, "g1", ip, 3, time.Minute); !ok {
			t.Fatalf("request %d must be allowed", i)
		}
	}
	if ok, _ := c.Firewall(ctx, "g1", ip, 3, time.Minute); ok {
		t.Error("the 4th request must be blocked")
	}
}

func TestLimitDaily(t *testing.T) {
	c, _ := newCounters(t)
	ctx := context.Background()
	rule := config.LimitRule{Enabled: true, Type: 1, Count: 2}

	// the first two takes fit under the limit of 2
	if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); !ok {
		t.Fatal("initially the limit must be free")
	}
	if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); !ok {
		t.Fatal("the second serve must still fit under the limit of 2")
	}
	if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); ok {
		t.Error("after 2 serves the limit (2) must be exhausted")
	}
	// a different stream is not affected
	if ok, _ := c.TakeLimit(ctx, "g1", "s2", rule); !ok {
		t.Error("a different stream's limit must not be affected")
	}
}
