package fetch

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetCachedCollapsesConcurrentMisses: with MaxConnsPerHost in force,
// connections are finite, so fifty copies of one request occupy slots fifty
// different requests need. Concurrent misses on the same key must produce one
// load.
func TestGetCachedCollapsesConcurrentMisses(t *testing.T) {
	c := New("ua")
	var loads atomic.Int64
	gate := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.GetCached(context.Background(), "k", time.Minute, func(context.Context) (string, error) {
				loads.Add(1)
				<-gate // hold the leader so the rest have to pile up behind it
				return "v", nil
			})
			if err != nil || v != "v" {
				t.Errorf("got %q, %v", v, err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()

	if n := loads.Load(); n != 1 {
		t.Errorf("50 concurrent misses caused %d loads, want 1", n)
	}
}

// TestGetCachedTTLZeroDoesNotShare is the other side. An uncacheable template
// expands to a unique URL per visitor, so there is nothing to share and the
// singleflight bookkeeping would be pure overhead on the hot path.
func TestGetCachedTTLZeroDoesNotShare(t *testing.T) {
	c := New("ua")
	var loads atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = c.GetCached(context.Background(), "k"+strconv.Itoa(i), 0, func(context.Context) (string, error) {
				loads.Add(1)
				return "v", nil
			})
		}(i)
	}
	wg.Wait()

	if n := loads.Load(); n != 20 {
		t.Errorf("%d loads for 20 distinct uncacheable keys, want 20", n)
	}
	if c.Len() != 0 {
		t.Errorf("ttl<=0 stored %d entries, want 0", c.Len())
	}
}

// TestLeaderCancellationDoesNotPoisonWaiters is the reason the shared load runs
// on a detached context. singleflight gives the leader's result to every waiter,
// cancellation included: on the caller's context, one visitor closing the tab
// would cancel the fetch fifty other visitors are waiting on, and all of them
// would drop to the fallback for a reason that is neither theirs nor the
// upstream's.
func TestLeaderCancellationDoesNotPoisonWaiters(t *testing.T) {
	c := New("ua")
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})

	var once sync.Once
	load := func(fctx context.Context) (string, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return "v", nil
		case <-fctx.Done():
			return "", fctx.Err()
		}
	}

	var wg sync.WaitGroup
	var leaderErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, leaderErr = c.GetCached(leaderCtx, "k", time.Minute, load)
	}()
	<-started

	var mu sync.Mutex
	var errs []error
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.GetCached(context.Background(), "k", time.Minute, load)
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}()
	}
	time.Sleep(100 * time.Millisecond)

	cancelLeader() // the leader's visitor closes the tab
	time.Sleep(100 * time.Millisecond)
	close(release) // the upstream answers, as it was going to all along
	wg.Wait()

	_ = leaderErr
	for i, err := range errs {
		if err != nil {
			t.Errorf("waiter %d got %v — the leader's cancellation leaked into it", i, err)
		}
	}
}

// TestSharedErrorReachesEveryWaiter documents the accepted cost: singleflight
// hands one error to all waiters, so a single upstream miss becomes a volley of
// fallbacks rather than N independent attempts. Deliberate — retrying per waiter
// would put the stampede back.
func TestSharedErrorReachesEveryWaiter(t *testing.T) {
	c := New("ua")
	want := errors.New("upstream down")
	var loads atomic.Int64
	gate := make(chan struct{})

	var mu sync.Mutex
	var got []error
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.GetCached(context.Background(), "k", time.Minute, func(context.Context) (string, error) {
				loads.Add(1)
				<-gate
				return "", want
			})
			mu.Lock()
			got = append(got, err)
			mu.Unlock()
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()

	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads, want 1", n)
	}
	for i, err := range got {
		if !errors.Is(err, want) {
			t.Errorf("waiter %d got %v, want the shared error", i, err)
		}
	}
	if c.Len() != 0 {
		t.Errorf("a failed load cached %d entries, want 0", c.Len())
	}
}
