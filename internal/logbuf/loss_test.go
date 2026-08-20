package logbuf

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// blockingInserter holds the first insert until it is released, so the test can
// observe what happens to Push while Run sits inside flush().
type blockingInserter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingInserter() *blockingInserter {
	return &blockingInserter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingInserter) InsertEvents(_ context.Context, _ []Event) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}

// TestBuffer_DropsWhilePreviousFlushIsInFlight shows that flush() runs inline in
// Run's loop: while a slow insert is in flight nobody reads b.in, so a running
// buffer drops events exactly like a buffer with no Run at all.
func TestBuffer_DropsWhilePreviousFlushIsInFlight(t *testing.T) {
	ins := newBlockingInserter()
	// capacity=4, batchSize=2, timer disabled: two events are enough to enter flush.
	b := New(ins, 4, 2, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	b.Push(Event{Stream: "a"})
	b.Push(Event{Stream: "b"})

	select {
	case <-ins.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("flush was never entered")
	}

	// Run is parked inside InsertEvents. Nothing drains the channel now:
	// the first 4 events fill it, the remaining 16 are dropped.
	for i := 0; i < 20; i++ {
		b.Push(Event{Stream: "c"})
	}
	if got := b.Dropped(); got != 16 {
		t.Fatalf("dropped while flushing = %d, want 16", got)
	}

	close(ins.release)
}

// failingInserter always fails and remembers how many events it was handed.
type failingInserter struct {
	mu   sync.Mutex
	seen int
}

func (f *failingInserter) InsertEvents(_ context.Context, batch []Event) error {
	f.mu.Lock()
	f.seen += len(batch)
	f.mu.Unlock()
	return errors.New("clickhouse is down")
}

func (f *failingInserter) Seen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

// TestBuffer_InsertFailureIsNotCounted documents the second, quieter loss path:
// when InsertEvents fails, flush() throws the batch away (only a log line is
// written) and dropped is left untouched. Every event is gone, yet Dropped()
// reports zero — so the counter cannot be used to detect this kind of loss.
func TestBuffer_InsertFailureIsNotCounted(t *testing.T) {
	ins := &failingInserter{}
	b := New(ins, 100, 5, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	const n = 10
	for i := 0; i < n; i++ {
		b.Push(Event{Stream: "s"})
	}

	waitFor(t, func() bool { return ins.Seen() >= n })

	if got := ins.Seen(); got != n {
		t.Fatalf("events handed to storage = %d, want %d", got, n)
	}
	// All n events failed to be stored and are not retried anywhere,
	// but the loss counter stays at zero.
	if got := b.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d, want 0 — this test pins the current behaviour", got)
	}
}

// TestBuffer_PushAfterRunExitsIsSilentlyLost documents the third loss path — the
// only one that fires on every single restart. cmd/engine/main.go cancels the
// buffer's context (main.go:183) before it calls srv.Shutdown (main.go:186), so
// Run drains, returns, and stops reading b.in while the HTTP server keeps
// serving in-flight requests for up to 10 more seconds. Every Push in that
// window lands in a channel nobody reads: the event is not inserted, not
// dropped, and not counted — Dropped() stays at zero.
func TestBuffer_PushAfterRunExitsIsSilentlyLost(t *testing.T) {
	ins := &fakeInserter{}
	// capacity comfortably larger than n, so nothing is lost on the channel path.
	b := New(ins, 100, 1000, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx was cancelled")
	}

	// This is the shutdown window: handlers are still alive and still logging.
	const n = 10
	for i := 0; i < n; i++ {
		b.Push(Event{Stream: "late"})
	}

	if got := ins.Total(); got != 0 {
		t.Fatalf("events stored after Run exited = %d, want 0", got)
	}
	if got := b.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d, want 0 — the loss leaves no trace at all; this test pins the current behaviour", got)
	}
}
