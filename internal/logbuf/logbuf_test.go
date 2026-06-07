package logbuf

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeInserter accumulates batches for verification.
type fakeInserter struct {
	mu      sync.Mutex
	batches [][]Event
	total   int
}

func (f *fakeInserter) InsertEvents(_ context.Context, batch []Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, batch)
	f.total += len(batch)
	return nil
}

func (f *fakeInserter) Total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

func TestBuffer_FlushBySize(t *testing.T) {
	ins := &fakeInserter{}
	b := New(ins, 100, 5, time.Hour, nil) // flush every 5, timer effectively disabled
	ctx, cancel := context.WithCancel(context.Background())
	go b.Run(ctx)

	for i := 0; i < 12; i++ {
		b.Push(Event{Stream: "s"})
	}
	// wait until at least 2 full batches (10 events) are inserted
	waitFor(t, func() bool { return ins.Total() >= 10 })
	cancel()
	waitFor(t, func() bool { return ins.Total() == 12 }) // remainder is flushed on cancel
}

func TestBuffer_FlushByTimer(t *testing.T) {
	ins := &fakeInserter{}
	b := New(ins, 100, 1000, 50*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	b.Push(Event{Stream: "s"})
	b.Push(Event{Stream: "s"})
	waitFor(t, func() bool { return ins.Total() == 2 }) // flush by timer, without waiting for batchSize
}

func TestBuffer_DropOnOverflow(t *testing.T) {
	ins := &fakeInserter{}
	// capacity=2, but we don't start Run — the channel isn't drained → overflow.
	b := New(ins, 2, 1000, time.Hour, nil)
	for i := 0; i < 10; i++ {
		b.Push(Event{Stream: "s"})
	}
	if b.Dropped() == 0 {
		t.Error("expected dropped events on overflow")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met within the allotted time")
}
