package fetch

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// clocked returns a client with an injectable clock and a small budget, so the
// bound can be reached in a test without megabytes of strings.
func clocked(t *testing.T, budget int, out *bytes.Buffer) (*Client, *time.Time) {
	t.Helper()
	var w io.Writer = io.Discard
	if out != nil {
		w = out
	}
	c := New("", slog.New(slog.NewTextHandler(w, nil)))
	cur := time.Unix(1000, 0)
	c.now = func() time.Time { return cur }
	c.budget = budget
	return c, &cur
}

func put(t *testing.T, c *Client, key, val string) {
	t.Helper()
	putTTL(t, c, key, val, time.Minute)
}

func putTTL(t *testing.T, c *Client, key, val string, ttl time.Duration) {
	t.Helper()
	got, err := c.GetCached(key, ttl, func() (string, error) { return val, nil })
	if err != nil || got != val {
		t.Fatalf("GetCached(%q): got %q, %v", key, got, err)
	}
}

func has(c *Client, key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.c[key]
	return ok
}

// A key built from [CID] or [PAR-n] is asked for once and never again, so a
// delete-on-lookup would never find it. The sweep has to walk the map.
func TestOneOffKeysAreSweptWhenTheMapDoubles(t *testing.T) {
	c, cur := clocked(t, cacheBudget, nil)
	c.sweepAt = 4
	for _, k := range []string{"a", "b", "c", "d"} {
		put(t, c, k, "v")
	}
	*cur = cur.Add(2 * time.Minute) // all four expired
	put(t, c, "e", "v")             // len 4 >= sweepAt → sweep, then store

	if n := len(c.c); n != 1 || !has(c, "e") {
		t.Fatalf("after the sweep want only e, have %d entries", n)
	}
	if c.size != len("e")+len("v") {
		t.Fatalf("size = %d after the sweep, want %d", c.size, len("e")+len("v"))
	}
	if c.sweepAt != minSweep {
		t.Fatalf("next sweep at %d, want the floor %d", c.sweepAt, minSweep)
	}
}

// Over budget with nothing expired: the new value is not stored and nothing
// live is thrown out for it. The caller still gets its value; the next caller
// loads it again.
func TestBudgetRefusesInsteadOfEvicting(t *testing.T) {
	c, _ := clocked(t, 100, nil)
	v := strings.Repeat("x", 40)
	put(t, c, "a", v) // 41
	put(t, c, "b", v) // 82
	put(t, c, "c", v) // 123 > 100 → refused

	if !has(c, "a") || !has(c, "b") {
		t.Fatal("live entries were evicted to make room")
	}
	if has(c, "c") {
		t.Fatal("stored past the budget")
	}
	calls := 0
	for i := 0; i < 2; i++ {
		c.GetCached("c", time.Minute, func() (string, error) { calls++; return v, nil })
	}
	if calls != 2 {
		t.Fatalf("a refused key must load every time, loaded %d of 2", calls)
	}
}

// Keys count too. Otherwise a long key over a one-byte body is free, and the
// number of entries is unbounded again.
func TestKeysCountAgainstTheBudget(t *testing.T) {
	c, _ := clocked(t, 100, nil)
	k := strings.Repeat("k", 60)
	put(t, c, k+"1", "v")
	put(t, c, k+"2", "v")
	if has(c, k+"2") {
		t.Fatal("second 61-byte key fits in a 100-byte budget only if keys are not counted")
	}
}

func TestValueLargerThanBudgetIsReturnedButNeverStored(t *testing.T) {
	c, _ := clocked(t, 100, nil)
	put(t, c, "big", strings.Repeat("x", 200))
	if has(c, "big") || c.size != 0 {
		t.Fatalf("stored a value larger than the whole budget (size=%d)", c.size)
	}
}

// A refusal is not permanent: once what fills the map expires, the sweep the
// budget check triggers frees the room.
func TestRefusalEndsWhenTheOldEntriesExpire(t *testing.T) {
	c, cur := clocked(t, 100, nil)
	v := strings.Repeat("x", 40)
	put(t, c, "a", v)
	put(t, c, "b", v)
	put(t, c, "c", v) // refused
	*cur = cur.Add(2 * time.Minute)
	put(t, c, "c", v) // a and b expired → swept → stored
	if !has(c, "c") || has(c, "a") {
		t.Fatal("expired entries must give way to the new one")
	}
}

// The line is written at most once a minute and carries the refused key, the
// entry count and the bytes held — enough to tell "eight huge bodies" from
// "eight thousand one-off keys" without reading the address.
func TestCacheFullLineIsRateLimitedAndNamesTheShape(t *testing.T) {
	var out bytes.Buffer
	c, cur := clocked(t, 100, &out)
	v := strings.Repeat("x", 40)
	// The fillers outlive the test's clock, so every later store is a refusal
	// and not a sweep.
	putTTL(t, c, "a", v, time.Hour)
	putTTL(t, c, "b", v, time.Hour)
	for i := 0; i < 3; i++ {
		put(t, c, "curl:http://partner/?cid=deadbeef", v)
	}
	if n := strings.Count(out.String(), "cache full"); n != 1 {
		t.Fatalf("three refusals in one minute wrote %d lines, want 1:\n%s", n, out.String())
	}
	line := out.String()
	for _, want := range []string{"cid=deadbeef", "entries=2", "cache_bytes=82", "value_bytes=40", "budget_bytes=100"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q:\n%s", want, line)
		}
	}

	*cur = cur.Add(30 * time.Second)
	put(t, c, "d", v)
	if n := strings.Count(out.String(), "cache full"); n != 1 {
		t.Fatalf("a refusal 30s later wrote a second line")
	}
	*cur = cur.Add(31 * time.Second)
	put(t, c, "d", v)
	if n := strings.Count(out.String(), "cache full"); n != 2 {
		t.Fatalf("a refusal a minute later must write again, have %d lines", n)
	}
}

func TestLongKeysAreCutInTheLog(t *testing.T) {
	var out bytes.Buffer
	c, _ := clocked(t, 100, &out)
	put(t, c, strings.Repeat("k", 300), strings.Repeat("x", 300))
	if !strings.Contains(out.String(), "key="+strings.Repeat("k", 100)+" ") {
		t.Fatalf("key not cut to 100 bytes:\n%s", out.String())
	}
}
