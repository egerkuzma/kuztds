//go:build integration

// Integration tests for the ClickHouse store. Require a running ClickHouse
// (make infra-up). Run with:
//
//	go test -tags=integration ./internal/store/ -run CH
//
// Connection parameters are taken from KUZTDS_CLICKHOUSE_* (default — dev-docker).
package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/logbuf"
)

func chFromEnv(t *testing.T) *CH {
	t.Helper()
	addr := envDef("KUZTDS_CLICKHOUSE_ADDR", "localhost:9000")
	db := envDef("KUZTDS_CLICKHOUSE_DB", "kuztds")
	user := envDef("KUZTDS_CLICKHOUSE_USER", "kuztds")
	pass := envDef("KUZTDS_CLICKHOUSE_PASSWORD", "devpassword")
	ch, err := OpenCH(addr, db, user, pass)
	if err != nil {
		t.Skipf("ClickHouse unavailable (%s): %v — skipping integration tests", addr, err)
	}
	return ch
}

func envDef(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestCHRoundTrip inserts events into a unique group, checks aggregates and
// reads, runs a postback, then cleans up after itself.
func TestCHRoundTrip(t *testing.T) {
	ch := chFromEnv(t)
	defer ch.Close()
	ctx := context.Background()

	group := fmt.Sprintf("itest_%d", os.Getpid())
	now := time.Now()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	t.Cleanup(func() {
		// clean up the test group's events and postbacks
		_ = ch.conn.Exec(ctx, "ALTER TABLE events DELETE WHERE group_name = ?", group)
		_ = ch.conn.Exec(ctx, "ALTER TABLE postbacks DELETE WHERE group_name = ?", group)
	})

	events := []logbuf.Event{
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", Out: "o1",
			Device: "phone", Country: "ru", City: "moscow", OS: "Android", Browser: "Chrome",
			Brand: "Samsung", Uniq: 1, IP: "1.2.3.4", CID: "cid_a", Keyword: "kw"},
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", Out: "o2",
			Device: "computer", Country: "us", Uniq: 0, IP: "5.6.7.8", CID: "cid_b"},
		{Ts: now, GroupID: group, GroupName: group, Stream: "s2", Out: "o3",
			Device: "phone", Country: "ru", Bot: "google", Uniq: 0, IP: "9.9.9.9", CID: "cid_c"},
	}
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	// give the insert time to become visible
	time.Sleep(200 * time.Millisecond)

	// Summary (across all groups in the window) — must be at least our 3.
	sum, err := ch.Summary(ctx, from, to)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Total < 3 {
		t.Errorf("Summary.Total = %d, expected >= 3", sum.Total)
	}

	// Logs filtered by group — exactly 3 rows.
	rows, total, err := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Limit: 100})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Errorf("Logs: total=%d len=%d, expected 3", total, len(rows))
	}

	// Logs bots only — 1 row.
	_, botTotal, err := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, BotsOnly: true, Limit: 100})
	if err != nil {
		t.Fatalf("Logs bots: %v", err)
	}
	if botTotal != 1 {
		t.Errorf("BotsOnly total = %d, expected 1", botTotal)
	}

	// Logs humans only — 2 rows.
	_, humanTotal, _ := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, HumansOnly: true, Limit: 100})
	if humanTotal != 2 {
		t.Errorf("HumansOnly total = %d, expected 2", humanTotal)
	}

	// Logs filtered by country ru — 2.
	_, ruTotal, _ := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Country: []string{"ru"}, Limit: 100})
	if ruTotal != 2 {
		t.Errorf("Country=ru total = %d, expected 2", ruTotal)
	}

	// TimeSeries — at least one point.
	ts, err := ch.TimeSeries(ctx, from, to, 3600)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(ts) == 0 {
		t.Error("TimeSeries is empty")
	}

	// Breakdown by device — contains phone.
	kv, err := ch.Breakdown(ctx, from, to, "device", 50)
	if err != nil {
		t.Fatalf("Breakdown: %v", err)
	}
	if !containsKey(kv, "phone") {
		t.Errorf("Breakdown device does not contain phone: %v", kv)
	}
	// unknown dimension → error.
	if _, err := ch.Breakdown(ctx, from, to, "evil; DROP", 50); err == nil {
		t.Error("Breakdown with an unknown dim must return an error")
	}

	// Postback for cid_a.
	if err := ch.RecordPostback(ctx, "cid_a", 2.5); err != nil {
		t.Fatalf("RecordPostback: %v", err)
	}
	// nonexistent cid → error.
	if err := ch.RecordPostback(ctx, "nope_cid", 1.0); err == nil {
		t.Error("RecordPostback for an unknown cid must return an error")
	}
	time.Sleep(200 * time.Millisecond)

	pb, profit, err := ch.Postbacks(ctx, from, to, group, 100)
	if err != nil {
		t.Fatalf("Postbacks: %v", err)
	}
	if len(pb) != 1 || profit != 2.5 {
		t.Errorf("Postbacks: rows=%d profit=%v, expected 1 row and 2.5", len(pb), profit)
	}
	if len(pb) == 1 && pb[0].CID != "cid_a" {
		t.Errorf("Postback cid = %q, expected cid_a", pb[0].CID)
	}
}

func containsKey(kv []KV, key string) bool {
	for _, x := range kv {
		if x.Key == key {
			return true
		}
	}
	return false
}
