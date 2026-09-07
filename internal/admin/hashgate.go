package admin

import (
	"sync/atomic"
	"time"
)

// defaultHashSlots bounds how many password verifications run at once.
//
// VerifyPassword runs on an unauthenticated request before any credential is
// checked, and argon2id allocates. The per-IP login limiter does not bound
// that: it counts per address while the memory is shared, so a hundred
// addresses at ten attempts a minute is a thousand allocations, and the process
// dies of the arithmetic rather than of any single attempt.
//
// What the gate actually bounds is slots × m, and m is not a constant here: it
// is read out of the encoded hash by VerifyPassword. With the 64 MiB that
// HashPassword produces, four slots is 256 MiB. The ceiling is four times
// whatever the largest m the hash validator will accept — so this number and
// that one are a pair, and neither may be raised without the other being
// recomputed. Deriving slots from a memory budget and that ceiling is the
// mechanical version, and lands once the validation change is on main; until
// then the pairing is written down rather than enforced, which is weaker and
// is why it is spelled out here instead of left as arithmetic in someone's
// head.
const defaultHashSlots = 4

// hashGate admits a bounded number of concurrent password verifications and
// counts the ones it turns away.
//
// It never waits. A gate that queued would trade an out-of-memory for a lockout:
// a scanner filling the slots would park the administrator behind its backlog,
// which is the same door closed by a different mechanism. Turning the excess
// away with 429 costs an attacker a sustained flood instead of a burst — and a
// sustained flood is visible, which a burst is not.
type hashGate struct {
	slots chan struct{}

	// rejected accumulates since the last line written. A line per rejection
	// would write to stdout at the speed of the attack, through slog's handler
	// mutex — the log amplification this package already pays for elsewhere.
	rejected atomic.Int64

	// lastLogged is the unix-nano stamp of the last line. Zero on purpose: the
	// first rejection must be reported immediately, because an attack is
	// detected by its beginning. Seeded with the current time it would swallow
	// the first second, which is the only second that matters.
	lastLogged atomic.Int64

	every time.Duration
	now   func() time.Time
}

func newHashGate(slots int) *hashGate {
	if slots <= 0 {
		slots = defaultHashSlots
	}
	return &hashGate{
		slots: make(chan struct{}, slots),
		every: time.Second,
		now:   time.Now,
	}
}

// enter takes a slot, or reports that none was free. The caller must release
// what it took.
func (g *hashGate) enter() bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		g.rejected.Add(1)
		return false
	}
}

func (g *hashGate) leave() { <-g.slots }

// report returns the number of rejections accumulated since the last report, or
// zero when it is not this caller's turn to write.
//
// The counter is incremented before the slot is refused, so the rejection that
// triggers a report is included in it — the other order publishes a zero on the
// first line.
//
// Choosing the writer and taking the total have to be one step. Two callers
// crossing the interval together would otherwise either both write, or split one
// interval's rejections across two lines: the CompareAndSwap elects a single
// writer, and only that one swaps the counter out.
func (g *hashGate) report() int64 {
	now := g.now().UnixNano()
	last := g.lastLogged.Load()
	if now-last < int64(g.every) {
		return 0
	}
	if !g.lastLogged.CompareAndSwap(last, now) {
		return 0
	}
	return g.rejected.Swap(0)
}
