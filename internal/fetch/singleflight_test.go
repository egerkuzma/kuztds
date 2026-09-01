package fetch

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingLoad is a load function that reports when it has been entered and
// waits until the test releases it. It carries no timers: every step of the
// tests below is driven by a channel, because a cache race proved with a sleep
// is a test that is green by luck and gets switched off the month it blinks.
type blockingLoad struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
	once    sync.Once
	val     string
	err     error
}

func newBlockingLoad(val string) *blockingLoad {
	return &blockingLoad{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		val:     val,
	}
}

func (b *blockingLoad) fn(ctx context.Context) (string, error) {
	b.calls.Add(1)
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return b.val, b.err
}

// TestGetCachedCollapsesConcurrentMisses is the stampede: a cold key and N
// arrivals at once used to mean N trips to the partner, each with its own ten
// second timeout.
//
// The count is checked while the load is still held. Checking it afterwards
// would be checking a race: once the flight completes, singleflight drops the
// key, and a caller descheduled between the cache lookup and DoChan opens a
// second flight — harmless, since it hits the cache instead, but not something
// a test should be asserting about.
func TestGetCachedCollapsesConcurrentMisses(t *testing.T) {
	c := New("")
	load := newBlockingLoad("body")

	const n = 20
	var ready, wg sync.WaitGroup
	ready.Add(n)
	wg.Add(n)
	got := make([]string, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ready.Done()
			v, err := c.GetCached(context.Background(), "k", time.Minute, load.fn)
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
				return
			}
			got[i] = v
		}(i)
	}

	ready.Wait()
	<-load.entered
	if n := load.calls.Load(); n != 1 {
		t.Fatalf("upstream was called %d times while one load was in flight, want 1", n)
	}

	close(load.release)
	wg.Wait()
	for i, v := range got {
		if v != "body" {
			t.Errorf("caller %d got %q", i, v)
		}
	}
}

// TestGetCachedLeavingCallerDoesNotFailTheOthers pins why the load runs on a
// context of its own. singleflight hands the leader's result to everyone in the
// queue, so a load carrying the leader's request context would turn one visitor
// closing a tab into a failed fetch for every visitor behind them — and the
// more of them there are, the worse it gets.
func TestGetCachedLeavingCallerDoesNotFailTheOthers(t *testing.T) {
	c := New("")
	load := newBlockingLoad("body")

	leaverCtx, leave := context.WithCancel(context.Background())
	leaverDone := make(chan error, 1)
	go func() {
		_, err := c.GetCached(leaverCtx, "k", time.Minute, load.fn)
		leaverDone <- err
	}()
	<-load.entered // the leader is inside the load

	stayerDone := make(chan result, 1)
	go func() {
		v, err := c.GetCached(context.Background(), "k", time.Minute, load.fn)
		stayerDone <- result{v, err}
	}()

	// The visitor who started the fetch goes away.
	leave()
	if err := <-leaverDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("the leaving caller should see its own cancellation, got %v", err)
	}

	close(load.release)
	r := <-stayerDone
	if r.err != nil {
		t.Fatalf("the caller who stayed was failed by the one who left: %v", r.err)
	}
	if r.val != "body" {
		t.Fatalf("stayer got %q, want body", r.val)
	}
	if n := load.calls.Load(); n != 1 {
		t.Fatalf("upstream was called %d times, want 1", n)
	}
}

type result struct {
	val string
	err error
}

// TestGetCachedStoresEvenIfEveryCallerLeft is the other half of the same
// decision. The fetch outlives its waiters, so it has to finish the job: the
// entry is written from inside the group. Storing in the caller would make an
// abandoned fetch ten seconds of work thrown away, and the next arrival would
// start the whole thing again.
func TestGetCachedStoresEvenIfEveryCallerLeft(t *testing.T) {
	c := New("")
	stored := make(chan struct{})
	c.onStore = func() { close(stored) }
	load := newBlockingLoad("body")

	ctx, leave := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.GetCached(ctx, "k", time.Minute, load.fn)
	}()

	<-load.entered
	leave()
	<-done // nobody is waiting on the fetch any more

	close(load.release)
	<-stored // the fetch finished the job on its own

	// A hit now, and no second trip upstream.
	v, err := c.GetCached(context.Background(), "k", time.Minute, load.fn)
	if err != nil {
		t.Fatalf("GetCached: %v", err)
	}
	if v != "body" {
		t.Fatalf("got %q, want body", v)
	}
	if n := load.calls.Load(); n != 1 {
		t.Fatalf("upstream was called %d times, want 1 — the abandoned fetch was thrown away", n)
	}
}

// TestGetCachedFailedLoadIsNotCached: a failure must not be remembered, or one
// bad minute at the partner becomes a cached bad minute for the whole ttl.
func TestGetCachedFailedLoadIsNotCached(t *testing.T) {
	c := New("")
	var calls atomic.Int64
	boom := errors.New("boom")
	load := func(context.Context) (string, error) {
		calls.Add(1)
		return "", boom
	}
	for i := 0; i < 3; i++ {
		if _, err := c.GetCached(context.Background(), "k", time.Minute, load); !errors.Is(err, boom) {
			t.Fatalf("call %d: err = %v", i, err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("upstream called %d times, want 3 — a failure must not be cached", n)
	}
}

// TestGetCachedTTLZeroDoesNotCollapse guards the meaning of ttl 0. It says
// "answer this one per visitor", not merely "do not keep the answer": a Remote
// on Cache: 0 is a per-visitor decision, and without [IP] in the template every
// visitor shares the key. Collapsing there would hand one draw to a whole burst
// of arrivals, which is a change of behaviour, not an optimisation.
func TestGetCachedTTLZeroDoesNotCollapse(t *testing.T) {
	c := New("")
	load := newBlockingLoad("body")

	const n = 5
	var ready, wg sync.WaitGroup
	ready.Add(n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			_, _ = c.GetCached(context.Background(), "k", 0, load.fn)
		}()
	}
	ready.Wait()
	<-load.entered
	// Everyone fetches for themselves; wait for the last one to arrive.
	for load.calls.Load() < n {
		runtime.Gosched()
	}
	close(load.release)
	wg.Wait()

	if got := load.calls.Load(); got != n {
		t.Fatalf("upstream called %d times for %d callers at ttl 0, want %d", got, n, n)
	}
}
