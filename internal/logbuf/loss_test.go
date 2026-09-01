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

// TestBuffer_KeepsReadingWhileTheWriterIsStuck pins the reason accumulating and
// inserting are separate goroutines.
//
// With the insert inlined in Run's loop, a slow storage parked the reader and
// b.in filled up: a running buffer dropped events at exactly the rate of a
// buffer with no Run at all. Now the writer takes the stall alone and the
// accumulator keeps reading, so a stall shorter than the queue costs nothing.
func TestBuffer_KeepsReadingWhileTheWriterIsStuck(t *testing.T) {
	ins := newBlockingInserter()
	b := New(ins, 1000, 2, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	b.Push(Event{Stream: "a"})
	b.Push(Event{Stream: "b"})
	select {
	case <-ins.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}

	// The writer stays parked inside InsertEvents for the rest of the test.
	// Inline, these 200 would have been dropped almost to the last one.
	const n = 200
	for i := 0; i < n; i++ {
		b.Push(Event{Stream: "c"})
	}
	if l := b.Losses(); l.Total() != 0 {
		t.Fatalf("losses = %+v, want none — the accumulator must keep reading", l)
	}

	close(ins.release)
}

// TestBuffer_QueueOverflowIsItsOwnCause covers the fourth cause. Once the queue
// behind a stuck writer is full there is nowhere to put the next batch, and it
// is refused whole. The cause has to be separate from Full: Full accuses the
// accumulator, Queue accuses storage, and the two are cured differently.
func TestBuffer_QueueOverflowIsItsOwnCause(t *testing.T) {
	ins := newBlockingInserter()
	// batchSize=1: every push is a batch, so the queue is the only bottleneck.
	b := New(ins, 2, 1, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	b.Push(Event{Stream: "a"})
	select {
	case <-ins.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the writer never entered InsertEvents")
	}

	// Paced, so b.in is always empty and nothing can be blamed on Full.
	for i := 0; i < 40 && b.Losses().Queue == 0; i++ {
		b.Push(Event{Stream: "c"})
		time.Sleep(5 * time.Millisecond)
	}
	if l := b.Losses(); l.Queue == 0 {
		t.Fatalf("losses = %+v, want Queue > 0", l)
	}
	if l := b.Losses(); l.Full != 0 || l.Insert != 0 || l.Late != 0 {
		t.Fatalf("losses = %+v, want Queue only", l)
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
	if l := b.Losses(); l.Insert != n || l.Full != 0 || l.Late != 0 {
		t.Fatalf("losses = %+v, want Insert=%d only", l, n)
	}
}

// TestBuffer_PushAfterShutdownIsCounted covers the third loss path: an event
// pushed after Close has shut the door can never reach storage, however much
// room the channel still has. main.go shuts the server down before closing the
// buffer, so the window is small — but whatever falls into it is counted
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

	cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer ccancel()
	if err := b.Close(cctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Close")
	}

	// This is the shutdown window: handlers are still alive and still logging.
	const n = 10
	for i := 0; i < n; i++ {
		b.Push(Event{Stream: "late"})
	}

	if got := ins.Total(); got != 0 {
		t.Fatalf("events stored after Run exited = %d, want 0", got)
	}
	if l := b.Losses(); l.Late != n || l.Full != 0 || l.Insert != 0 {
		t.Fatalf("losses = %+v, want Late=%d only — late pushes must be counted, not swallowed", l, n)
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

// TestBuffer_CloseWaitsForTheWriter covers the last loss path: returning from
// cancel() proves nothing. Cancelling Run is a stop signal, not a deadline —
// Run puts down what it holds and leaves, and everything else belongs to Close,
// which owns the door, the drain and the wait under one budget.
func TestBuffer_CloseWaitsForTheWriter(t *testing.T) {
	ins := newSlowRecordingInserter()
	// timer disabled and batchSize above n: nothing flushes on its own.
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
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	// This is the instant main.go used to return from cancel(). Nothing is
	// stored: a process exiting here loses all n events, and no counter moves.
	if got := ins.Stored(); got != 0 {
		t.Fatalf("stored at the moment cancel() returns = %d, want 0", got)
	}

	// Close under a deadline shorter than the stuck insert reports the truth
	// instead of pretending the work is finished.
	short, scancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer scancel()
	if err := b.Close(short); err == nil {
		t.Fatal("Close returned nil while the insert was still in flight")
	}
	select {
	case <-ins.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Close never handed the drained events to the writer")
	}

	// Released, a second Close completes: it is idempotent, and the events are
	// durable only once it returns without an error.
	close(ins.release)
	full, fcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer fcancel()
	if err := b.Close(full); err != nil {
		t.Fatalf("Close after release: %v", err)
	}
	if got := ins.Stored(); got != n {
		t.Fatalf("stored after Close = %d, want %d", got, n)
	}
	if got := b.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d, want 0 — the farewell batch was stored, not lost", got)
	}
}

// ctxRespectingInserter blocks until its context is cancelled, then fails the
// way a driver that honours cancellation does.
type ctxRespectingInserter struct {
	entered chan struct{}
	once    sync.Once
}

func newCtxRespectingInserter() *ctxRespectingInserter {
	return &ctxRespectingInserter{entered: make(chan struct{})}
}

func (c *ctxRespectingInserter) InsertEvents(ctx context.Context, _ []Event) error {
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return ctx.Err()
}

// TestBuffer_CloseCancelsTheInsertItGivesUpOn is the answer to "what does Close
// do when its deadline is up and the writer is still inside InsertEvents?".
//
// Waiting would make the deadline a lie. Walking away would repeat the bug this
// whole change is about: the batch dies with the process and no counter ever
// moves, so the farewell line under-reports exactly when it matters. Close does
// neither — it cancels the insert, lets the writer record the loss, and only
// then reports that it ran out of time.
func TestBuffer_CloseCancelsTheInsertItGivesUpOn(t *testing.T) {
	ins := newCtxRespectingInserter()
	b := New(ins, 100, 1000, time.Hour, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	const n = 7
	for i := 0; i < n; i++ {
		b.Push(Event{Stream: "s"})
	}

	short, scancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer scancel()
	err := b.Close(short)
	if err == nil {
		t.Fatal("Close returned nil although the insert never finished")
	}
	select {
	case <-ins.entered:
	default:
		t.Fatal("the batch never reached InsertEvents")
	}
	// The point: by the time Close returns, the loss is on the books.
	if l := b.Losses(); l.Insert != n {
		t.Fatalf("losses = %+v, want Insert=%d — a batch Close gave up on must still be counted", l, n)
	}
}
