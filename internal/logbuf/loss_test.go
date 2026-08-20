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
