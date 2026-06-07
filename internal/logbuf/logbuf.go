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
	dropped    atomic.Int64
}

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
	}
}

// Push puts an event into the buffer without blocking. On overflow — drops it
// and increments the loss counter.
func (b *Buffer) Push(e Event) {
	select {
	case b.in <- e:
	default:
		b.dropped.Add(1)
	}
}

// Dropped returns the number of events dropped due to overflow.
func (b *Buffer) Dropped() int64 { return b.dropped.Load() }

// Run starts the accumulate-and-flush loop until ctx is cancelled. Blocking — run
// in a separate goroutine.
func (b *Buffer) Run(ctx context.Context) {
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
			b.log.Error("logbuf: insert failed", "n", len(cp), "err", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
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
