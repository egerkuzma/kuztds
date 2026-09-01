// Package logbuf buffers events and writes them to storage in batches.
//
// Log writes are asynchronous: Push puts an event into a channel and returns
// immediately, Run accumulates a batch, and a separate writer goroutine inserts
// it. The visitor response never waits for the log write; on overflow the event
// is dropped (with a counter).
//
// Accumulating and inserting are deliberately different goroutines. With the
// insert inlined into the accumulator, a storage that answers slowly — a
// ClickHouse busy merging parts will hold an insert for tens of seconds and
// then succeed — stops the reader for exactly that long, the channel fills, and
// every event in the window is dropped. Handing the batch over keeps the reader
// running at all times.
package logbuf

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Event is a single log row (fields match the ClickHouse events schema).
type Event struct {
	Ts        time.Time
	GroupID   string
	GroupName string
	Stream    string
	Out       string
	Keyword   string
	Redirect  string
	Device    string
	Operator  string
	Country   string
	City      string
	Region    string
	Lang      string
	Uniq      uint8
	Bot       string
	IP        string
	Referer   string
	UserAgent string
	Domain    string
	Page      string
	SE        string
	OS        string
	OSVersion string
	Browser   string
	BrowserV  string
	Brand     string
	Counter   uint32
	CID       string
	Postback  string
}

// Inserter inserts a batch of events into storage (implemented by store.CH).
type Inserter interface {
	InsertEvents(ctx context.Context, batch []Event) error
}

// insertTimeout bounds a single insert. It stays generous on purpose: a batch
// that misses five seconds under merge pressure will often land at twenty, and
// the batch is not retried anywhere, so a short deadline turns recoverable
// slowness into certain loss.
const insertTimeout = 30 * time.Second

// insertGrace bounds how long Close waits for a cancelled insert to unwind. A
// driver that honours its context needs microseconds; the timer exists only so
// that one which does not cannot hold the process open indefinitely.
const insertGrace = 2 * time.Second

// Buffer is an asynchronous event buffer.
type Buffer struct {
	in         chan Event
	batches    chan []Event
	ins        Inserter
	batchSize  int
	flushEvery time.Duration
	log        *slog.Logger

	// closed is owned by Close and by nobody else. Run only reads it. Two
	// closers on one channel is a panic, and it would land in the middle of a
	// shutdown, where a test that exercises one path at a time cannot see it.
	closed    chan struct{}
	closeOnce sync.Once
	runDone   chan struct{} // Run returned
	drained   chan struct{} // Close finished with b.in: the writer may wind down
	wrDone    chan struct{} // the writer returned: nothing is in flight

	// insertCtx is the parent of every insert, so Close can reach into a batch
	// that is already inside InsertEvents. Without it the last insert carries an
	// uncancellable 30s of its own: Close would either wait past its own
	// deadline or walk away from a live goroutine, and in the second case
	// nothing would ever increment lostInsert for that batch.
	insertCtx    context.Context
	cancelInsert context.CancelFunc

	// Losses are counted per cause, because they are cured differently:
	// a full buffer means the accumulator could not keep up, a full queue means
	// the writer could not, a failed insert means storage is unhealthy, and a
	// late push means events arrived during shutdown.
	lostFull   atomic.Int64
	lostQueue  atomic.Int64
	lostInsert atomic.Int64
	lostLate   atomic.Int64
}

// Losses is a per-cause breakdown of events that never reached storage.
type Losses struct {
	Full   int64 // in-channel was full: the accumulator could not keep up
	Queue  int64 // batch queue was full: the writer could not keep up
	Insert int64 // InsertEvents failed and the batch is not retried
	Late   int64 // pushed after Close shut the door
}

// Total returns the number of lost events across all causes.
func (l Losses) Total() int64 { return l.Full + l.Queue + l.Insert + l.Late }

// New creates a buffer. capacity — channel size, batchSize — flush threshold by
// size, flushEvery — maximum flush delay.
func New(ins Inserter, capacity, batchSize int, flushEvery time.Duration, log *slog.Logger) *Buffer {
	if log == nil {
		log = slog.Default()
	}
	if capacity <= 0 {
		capacity = 10000
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	if flushEvery <= 0 {
		flushEvery = time.Second
	}
	// The queue holds roughly as many events as the in-channel, so a stalled
	// writer buys the same grace twice over instead of once.
	depth := capacity / batchSize
	if depth < 2 {
		depth = 2
	}
	ictx, icancel := context.WithCancel(context.Background())
	b := &Buffer{
		insertCtx:    ictx,
		cancelInsert: icancel,

		in:         make(chan Event, capacity),
		batches:    make(chan []Event, depth),
		ins:        ins,
		batchSize:  batchSize,
		flushEvery: flushEvery,
		log:        log,
		closed:     make(chan struct{}),
		runDone:    make(chan struct{}),
		drained:    make(chan struct{}),
		wrDone:     make(chan struct{}),
	}
	// The writer belongs to the buffer, not to Run: Close has to be able to
	// finish the queue whatever happened to the accumulator.
	go b.write()
	return b
}

// Push puts an event into the buffer without blocking. On overflow — drops it
// and counts it.
//
// The shutdown check is best-effort: Close can shut the door in the nanoseconds
// between the check and the send, in which case the event lands in the channel.
// Close drains what is there, so such an event is usually still written.
func (b *Buffer) Push(e Event) {
	select {
	case <-b.closed:
		b.lostLate.Add(1)
		return
	default:
	}
	select {
	case b.in <- e:
	default:
		b.lostFull.Add(1)
	}
}

// Dropped returns the total number of events that never reached storage.
func (b *Buffer) Dropped() int64 { return b.Losses().Total() }

// Losses returns the loss counters broken down by cause. The four are read
// separately, so a snapshot taken under load may be a few events out of step.
func (b *Buffer) Losses() Losses {
	return Losses{
		Full:   b.lostFull.Load(),
		Queue:  b.lostQueue.Load(),
		Insert: b.lostInsert.Load(),
		Late:   b.lostLate.Load(),
	}
}

// Run accumulates events into batches and hands them to the writer. Blocking —
// run in a separate goroutine.
//
// ctx is a stop signal, not a deadline: cancelling it makes Run put down what it
// is holding and leave. It does not drain, does not flush and does not close
// anything — all of that belongs to Close, which owns one budget and one door.
// Two goroutines draining the same channel would tear a batch in half.
func (b *Buffer) Run(ctx context.Context) {
	defer close(b.runDone)
	t := time.NewTicker(b.flushEvery)
	defer t.Stop()
	batch := make([]Event, 0, b.batchSize)

	for {
		select {
		case <-ctx.Done():
			b.handoff(&batch)
			return
		case <-b.closed:
			b.handoff(&batch)
			return
		case e := <-b.in:
			batch = append(batch, e)
			if len(batch) >= b.batchSize {
				b.handoff(&batch)
			}
		case <-t.C:
			b.handoff(&batch)
		}
	}
}

// Close stops accepting, drains what is left and waits for the writer, all
// within ctx. Returns ctx.Err() if the deadline ran out first — in which case
// the caller is being told the truth: work is still in flight, and whatever the
// process does next will decide its fate.
//
// Idempotent and safe to call from several goroutines.
func (b *Buffer) Close(ctx context.Context) error {
	b.closeOnce.Do(func() {
		close(b.closed)
		// Wait for Run, deadline or not. Run answers b.closed from a loop that
		// never blocks, so this is short — and skipping it would mean draining
		// b.in alongside a live accumulator, which tears a batch in half. An
		// expired ctx is a reason to stop waiting for storage, never a reason
		// to skip the bookkeeping: everything below still has to be counted.
		select {
		case <-b.runDone:
		case <-time.After(insertGrace):
			// Run was never started, or is wedged. Press on: the queue still
			// has to be wound up, or the writer waits forever and nothing in
			// b.in is ever counted.
			b.log.Error("logbuf: the accumulator did not stop; draining alongside it")
		}
		batch := make([]Event, 0, b.batchSize)
		for drained := false; !drained; {
			select {
			case e := <-b.in:
				batch = append(batch, e)
				if len(batch) >= b.batchSize {
					b.queue(ctx, &batch)
				}
			default:
				drained = true
			}
		}
		b.queue(ctx, &batch)
		close(b.drained)
	})
	select {
	case <-b.wrDone:
		return nil
	case <-ctx.Done():
	}
	// The deadline is up with a batch still inside InsertEvents. Cancelling it
	// is the difference between a truthful farewell line and a silent one: the
	// writer comes back, counts the batch as lost and exits, and only then do we
	// admit we ran out of time.
	b.cancelInsert()
	select {
	case <-b.wrDone:
	case <-time.After(insertGrace):
		b.log.Error("logbuf: the writer ignored cancellation; loss counters are short by at least one batch")
	}
	return ctx.Err()
}

// handoff passes the accumulated batch to the writer without blocking. A full
// queue means the writer is stuck; dropping here keeps the accumulator reading,
// and the loss is counted whole batches at a time.
func (b *Buffer) handoff(batch *[]Event) {
	cp := b.take(batch)
	if cp == nil {
		return
	}
	select {
	case b.batches <- cp:
	default:
		b.lostQueue.Add(int64(len(cp)))
		b.log.Error("logbuf: batch queue full, dropping batch", "n", len(cp))
	}
}

// queue is handoff for the shutdown path: it waits for room instead of dropping,
// because there is no next flush to catch what falls here.
func (b *Buffer) queue(ctx context.Context, batch *[]Event) {
	cp := b.take(batch)
	if cp == nil {
		return
	}
	// Try without waiting first: a ctx that is already expired must still get
	// the batch as far as the queue, otherwise a shutdown that started late
	// throws away events the writer had room for.
	select {
	case b.batches <- cp:
		return
	default:
	}
	select {
	case b.batches <- cp:
	case <-ctx.Done():
		b.lostQueue.Add(int64(len(cp)))
		b.log.Error("logbuf: shutdown deadline before the batch was queued", "n", len(cp))
	}
}

// take detaches the accumulated events so the caller can keep reusing batch.
// The copy is load-bearing: the writer reads the slice after this returns.
func (b *Buffer) take(batch *[]Event) []Event {
	if len(*batch) == 0 {
		return nil
	}
	cp := make([]Event, len(*batch))
	copy(cp, *batch)
	*batch = (*batch)[:0]
	return cp
}

// write inserts queued batches until Close says the queue is complete, then
// empties what is left and returns.
//
// b.batches is never closed. Run and Close both send on it, and ordering two
// senders against a close is exactly the kind of shutdown-only race that a test
// exercising one path at a time will not catch — the price of getting it wrong
// is a panic in production. A separate done-signal costs one channel and cannot
// be got wrong.
func (b *Buffer) write() {
	defer close(b.wrDone)
	for {
		var cp []Event
		select {
		case cp = <-b.batches:
		case <-b.drained:
			select {
			case cp = <-b.batches:
			default:
				return
			}
		}
		ctx, cancel := context.WithTimeout(b.insertCtx, insertTimeout)
		err := b.ins.InsertEvents(ctx, cp)
		cancel()
		if err != nil {
			// The batch is gone and is not retried anywhere — count it as lost,
			// otherwise a failing storage looks exactly like a healthy one.
			b.lostInsert.Add(int64(len(cp)))
			b.log.Error("logbuf: insert failed", "n", len(cp), "err", err)
		}
	}
}
