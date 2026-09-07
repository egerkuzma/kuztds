package admin

import (
	"sync"
	"testing"
	"time"
)

func TestHashGateAdmitsUpToItsLimitAndRefusesTheRest(t *testing.T) {
	g := newHashGate(2)
	if !g.enter() || !g.enter() {
		t.Fatal("the first two must be admitted")
	}
	if g.enter() {
		t.Fatal("the third must be refused, not queued: waiting would trade an OOM for a lockout")
	}
	g.leave()
	if !g.enter() {
		t.Fatal("a freed slot must be reusable")
	}
}

// The counter is incremented before the refusal is returned, so the rejection
// that triggers the first report is counted in it. Incrementing afterwards
// publishes a zero on the line that matters most.
func TestHashGateReportsTheFirstRejectionImmediately(t *testing.T) {
	g := newHashGate(1)
	if !g.enter() {
		t.Fatal("first must be admitted")
	}
	if g.enter() {
		t.Fatal("second must be refused")
	}
	if n := g.report(); n != 1 {
		t.Fatalf("report = %d, want 1 — the first second of an attack is the one that matters", n)
	}
}

// Within the interval nothing more is written: a line per rejection would write
// to stdout at the speed of the attack, through the logger's own mutex.
func TestHashGateRateLimitsItsReports(t *testing.T) {
	g := newHashGate(1)
	now := time.Unix(1700000000, 0)
	g.now = func() time.Time { return now }

	g.enter()
	for i := 0; i < 5; i++ {
		g.enter()
	}
	if n := g.report(); n != 5 {
		t.Fatalf("first report = %d, want 5", n)
	}
	g.enter()
	if n := g.report(); n != 0 {
		t.Fatalf("second report within the interval = %d, want 0", n)
	}
	now = now.Add(2 * time.Second)
	if n := g.report(); n != 1 {
		t.Fatalf("report after the interval = %d, want the one accumulated since", n)
	}
}

// Choosing the writer and taking the total must be one step. Two callers
// crossing the interval together would otherwise both write, or split one
// interval's rejections across two lines and lose the meaning of both.
func TestHashGateElectsASingleReporter(t *testing.T) {
	g := newHashGate(1)
	g.enter()
	const rejections = 200
	for i := 0; i < rejections; i++ {
		g.enter()
	}

	var mu sync.Mutex
	var total, writers int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n := g.report(); n > 0 {
				mu.Lock()
				total += n
				writers++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if writers != 1 {
		t.Fatalf("%d goroutines wrote a line, want exactly 1", writers)
	}
	if total != rejections {
		t.Fatalf("reported %d of %d rejections", total, rejections)
	}
}
