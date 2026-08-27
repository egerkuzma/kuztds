package store

import (
	"context"
	"sync"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// TestTakeLimitUnderConcurrency pins the race the LimitAllowed/RecordServe pair
// could not avoid: the check was a GET and the increment a separate INCR, so
// concurrent requests all read the same under-the-limit value and were all
// served. Here the limit must hold exactly, whatever the concurrency.
func TestTakeLimitUnderConcurrency(t *testing.T) {
	c, _ := newCounters(t)
	ctx := context.Background()
	rule := config.LimitRule{Enabled: true, Type: 1, Count: 100}

	const workers = 64
	const each = 20 // 1280 attempts against a limit of 100

	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for j := 0; j < each; j++ {
				if ok, err := c.TakeLimit(ctx, "g1", "s1", rule); err == nil && ok {
					local++
				}
			}
			mu.Lock()
			allowed += local
			mu.Unlock()
		}()
	}
	wg.Wait()

	if allowed != rule.Count {
		t.Errorf("served %d times against a limit of %d", allowed, rule.Count)
	}
}

// TestTakeLimitDoesNotCountRefusals is the other half. router.Select applies the
// limit as its last filter and moves on to the next stream when it refuses, so an
// exhausted stream sitting early in the list is reached by every later request.
// If a refusal still incremented, its counter would climb without a ceiling for
// the rest of the window and the daily report would show far more serves than
// happened.
func TestTakeLimitDoesNotCountRefusals(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()
	rule := config.LimitRule{Enabled: true, Type: 1, Count: 3}

	for i := 0; i < 3; i++ {
		if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); !ok {
			t.Fatalf("serve %d must be allowed", i+1)
		}
	}
	// 500 more requests reach the exhausted stream and are refused.
	for i := 0; i < 500; i++ {
		if ok, _ := c.TakeLimit(ctx, "g1", "s1", rule); ok {
			t.Fatalf("request %d must be refused", i+4)
		}
	}

	key, _ := c.limitKey("g1", "s1", rule)
	got, err := mr.Get(key)
	if err != nil {
		t.Fatalf("counter missing: %v", err)
	}
	if got != "3" {
		t.Errorf("counter = %s, want 3 — refusals must not be counted as serves", got)
	}
}

// TestTakeLimitFailOpen: a dead Redis must not stop traffic.
func TestTakeLimitFailOpen(t *testing.T) {
	c, mr := newCounters(t)
	mr.Close()
	rule := config.LimitRule{Enabled: true, Type: 1, Count: 1}

	ok, err := c.TakeLimit(context.Background(), "g1", "s1", rule)
	if err == nil {
		t.Fatal("want an error from a closed Redis")
	}
	if !ok {
		t.Error("must fail open: a broken counter must not stop traffic")
	}
}

// TestIncrTTLStampsTTLAtomically: the firewall counter always carries an expiry,
// so it can never turn into a permanent ban.
func TestIncrTTLStampsTTLAtomically(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()

	n, err := c.incrWithTTL(ctx, "k", defaultWindow)
	if err != nil || n != 1 {
		t.Fatalf("incrWithTTL = %d, %v", n, err)
	}
	if ttl := mr.TTL("k"); ttl <= 0 {
		t.Fatalf("key must carry a TTL right after the first increment, got %v", ttl)
	}
	if n, _ := c.incrWithTTL(ctx, "k", defaultWindow); n != 2 {
		t.Errorf("second increment = %d, want 2", n)
	}
	mr.FastForward(2 * defaultWindow)
	if n, _ := c.incrWithTTL(ctx, "k", defaultWindow); n != 1 {
		t.Errorf("after the window expires the counter must restart, got %d", n)
	}
}
