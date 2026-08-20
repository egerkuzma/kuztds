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
//
// This one is deliberately NOT fixed here and stays green: the loss-accounting
// change buys visibility, not durability. A slow ClickHouse still eats the
// buffer at exactly the same rate — 10000 capacity at 1000 events/sec is ten
// seconds. Making flush asynchronous is a separate, riskier change.
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

// TestBuffer_InsertFailureIsCounted covers the second loss path: when
// InsertEvents fails, flush() throws the batch away and does not retry it.
// The events are still lost, but they are now added to the loss counter, so a
// failing storage no longer looks exactly like a healthy one.
func TestBuffer_InsertFailureIsCounted(t *testing.T) {
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
	// All n events failed to be stored and are not retried anywhere — the
	// counter must say so.
	waitFor(t, func() bool { return b.Dropped() == n })
	if got := b.Dropped(); got != n {
		t.Fatalf("Dropped() = %d, want %d", got, n)
	}
}

// TestBuffer_PushAfterShutdownIsCounted covers the third loss path: an event
// pushed once Run has stopped reading b.in can never reach storage, however much
// room the channel still has. main.go now shuts the server down before cancelling
// the buffer, so the window is small — but whatever falls into it is counted
// instead of vanishing without a trace.
func TestBuffer_PushAfterShutdownIsCounted(t *testing.T) {
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
	if got := b.Dropped(); got != n {
		t.Fatalf("Dropped() = %d, want %d — late pushes must be counted, not swallowed", got, n)
	}
}

// slowRecordingInserter blocks inside the insert until released and remembers
// what actually made it into storage.
type slowRecordingInserter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	stored int
}

func newSlowRecordingInserter() *slowRecordingInserter {
	return &slowRecordingInserter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *slowRecordingInserter) InsertEvents(_ context.Context, batch []Event) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	s.mu.Lock()
	s.stored += len(batch)
	s.mu.Unlock()
	return nil
}

func (s *slowRecordingInserter) Stored() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stored
}

// TestBuffer_DoneWaitsForFinalFlush covers the fourth loss path, the reason
// reordering cancel() and srv.Shutdown is not enough on its own. Run's farewell
// flush is synchronous and runs under a 30s timeout of its own, so returning
// from cancel() proves nothing: the batch may still be inside InsertEvents.
// Done() is that missing signal — it closes only after the final flush is over,
// which is what main.go now waits on (bounded by the shutdown deadline).
func TestBuffer_DoneWaitsForFinalFlush(t *testing.T) {
	ins := newSlowRecordingInserter()
	// timer disabled and batchSize above n: the only flush is the one on cancel.
	b := New(ins, 100, 1000, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()

	const n = 10
	for i := 0; i < n; i++ {
		b.Push(Event{Stream: "s"})
	}
	cancel()

	select {
	case <-ins.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the farewell flush was never entered")
	}

	// This is the instant main.go returns from cancel(). The batch is still
	// inside InsertEvents: nothing is stored yet, and Done() is still open —
	// a process exiting here would lose all n events.
	if got := ins.Stored(); got != 0 {
		t.Fatalf("stored at the moment cancel() returns = %d, want 0", got)
	}
	select {
	case <-b.Done():
		t.Fatal("Done() closed before the final flush completed")
	default:
	}

	// Waiting on Done() is what makes the events durable.
	close(ins.release)
	select {
	case <-b.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done() never closed after the flush was released")
	}
	if got := ins.Stored(); got != n {
		t.Fatalf("stored after waiting on Done() = %d, want %d", got, n)
	}
	if got := b.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d, want 0 — the farewell batch was stored, not lost", got)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}
