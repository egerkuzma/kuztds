// Package logbuf buffers events and writes them to storage in batches.
//
// Log writes are asynchronous: Push puts an event into a channel and returns
// immediately, while the background
// Run accumulates a batch and inserts it in a single request. The visitor response
// never waits for the log write; on buffer overflow the event is dropped (with a counter).
package logbuf

import (
	"context"
	"log/slog"
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

// Buffer is an asynchronous event buffer.
type Buffer struct {
	in         chan Event
	ins        Inserter
	batchSize  int
	flushEvery time.Duration
	log        *slog.Logger
	closed     chan struct{} // Run stopped accepting: Push must count, not enqueue
	done       chan struct{} // Run returned: the final flush is over

	// Losses are counted per cause, because the three are cured differently:
	// a full buffer means the writer is too slow, a failed insert means storage
	// is unhealthy, and a late push means events arrived during shutdown.
	lostFull   atomic.Int64
	lostInsert atomic.Int64
	lostLate   atomic.Int64
}

// Losses is a per-cause breakdown of events that never reached storage.
type Losses struct {
	Full   int64 // buffer was full: Run could not keep up
	Insert int64 // InsertEvents failed and the batch is not retried
	Late   int64 // pushed after Run stopped reading
}

// Total returns the number of lost events across all causes.
func (l Losses) Total() int64 { return l.Full + l.Insert + l.Late }

// New creates a buffer. capacity — channel size, batchSize — flush threshold by size,
// flushEvery — maximum flush delay.
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
	return &Buffer{
		in:         make(chan Event, capacity),
		ins:        ins,
		batchSize:  batchSize,
		flushEvery: flushEvery,
		log:        log,
		closed:     make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Push puts an event into the buffer without blocking. On overflow — drops it
// and counts it.
//
// The shutdown check is best-effort: Run can stop reading in the nanoseconds
// between the check and the send, in which case the event lands in the channel
// and is lost uncounted. Closing the door before draining keeps that window to
// the width of a single select, but it is not zero.
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

// Losses returns the loss counters broken down by cause. The three are read
// separately, so a snapshot taken under load may be a few events out of step.
func (b *Buffer) Losses() Losses {
	return Losses{
		Full:   b.lostFull.Load(),
		Insert: b.lostInsert.Load(),
		Late:   b.lostLate.Load(),
	}
}

// Done is closed when Run has returned, i.e. the final flush is over. Wait on it
// during shutdown — but always against a deadline, since the last insert has its
// own 30s timeout and a dead storage will use all of it.
func (b *Buffer) Done() <-chan struct{} { return b.done }

// Run starts the accumulate-and-flush loop until ctx is cancelled. Blocking — run
// in a separate goroutine.
func (b *Buffer) Run(ctx context.Context) {
	defer close(b.done)
	t := time.NewTicker(b.flushEvery)
	defer t.Stop()
	batch := make([]Event, 0, b.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		cp := make([]Event, len(batch))
		copy(cp, batch)
		batch = batch[:0]
		fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := b.ins.InsertEvents(fctx, cp); err != nil {
			// The batch is gone and is not retried anywhere — count it as lost,
			// otherwise a failing storage looks exactly like a healthy one.
			b.lostInsert.Add(int64(len(cp)))
			b.log.Error("logbuf: insert failed", "n", len(cp), "err", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			// Stop accepting first, so events arriving from here on are counted
			// instead of landing in a channel that will never be read again.
			close(b.closed)
			// Drain the rest of the channel and flush before exiting.
			for {
				select {
				case e := <-b.in:
					batch = append(batch, e)
					if len(batch) >= b.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case e := <-b.in:
			batch = append(batch, e)
			if len(batch) >= b.batchSize {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}
