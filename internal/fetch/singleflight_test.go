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

// TestWaiterLeavesOnItsOwnCancel is the other half of "detach the work, not the
// wait". Do blocks until the fetch finishes, so a detached load would hold every
// waiter's goroutine for the full deadline even after its visitor has gone —
// eight seconds each on the curl path, and MaxConnsPerHost does not help because
// it counts sockets, not goroutines.
func TestWaiterLeavesOnItsOwnCancel(t *testing.T) {
	c := New("ua")
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

	go func() { _, _ = c.GetCached(context.Background(), "k", time.Minute, load) }()
	<-started

	wctx, wcancel := context.WithCancel(context.Background())
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_, err := c.GetCached(wctx, "k", time.Minute, load)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter got %v, want context.Canceled", err)
		}
		done <- time.Since(start)
	}()

	time.Sleep(50 * time.Millisecond)
	wcancel() // this visitor closes the tab

	select {
	case el := <-done:
		if el > time.Second {
			t.Errorf("waiter took %v to leave after its own cancel", el)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not leave on its own cancel — it is pinned to the shared load")
	}

	// The shared load was not harmed by the waiter leaving.
	close(release)
	v, err := c.GetCached(context.Background(), "k", time.Minute, load)
	if err != nil || v != "v" {
		t.Errorf("shared load result = %q, %v", v, err)
	}
}

// TestDeadlinedLeaderLeavingDoesNotKillTheLoad is the trap that opens up the
// moment Do becomes DoChan. The load's context needs a cancel func, and if that
// cancel is deferred in GetCached's own frame, the first caller to walk away
// tears down the context the shared fetch is running on — the leader-cancellation
// bug back through the door, now triggered by a waiter leaving early.
//
// It only bites when the caller carries a deadline, which on the hot path is
// always: remoteTimeout and curlTimeout put one on every call.
func TestDeadlinedLeaderLeavingDoesNotKillTheLoad(t *testing.T) {
	c := New("ua")
	leaderCtx, leaderCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer leaderCancel()

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

	go func() { _, _ = c.GetCached(leaderCtx, "k", time.Minute, load) }()
	<-started

	var wg sync.WaitGroup
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

	leaderCancel() // the leader, which owns the deadline, walks away
	time.Sleep(100 * time.Millisecond)
	close(release) // the upstream answers
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("waiter %d got %v — the leader's cancel func tore down the shared load", i, err)
		}
	}
}
