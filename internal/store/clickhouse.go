package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/egerkuzma/kuztds/internal/logbuf"
)

// CH — a ClickHouse client for batch-inserting events. Implements logbuf.Inserter.
type CH struct {
	conn driver.Conn
}

// OpenCH opens a connection to ClickHouse.
func OpenCH(addr, database, username, password string) (*CH, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: database, Username: username, Password: password},
		DialTimeout: 5 * time.Second,
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse: open: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse: ping: %w", err)
	}
	return &CH{conn: conn}, nil
}

// Close closes the connection.
func (c *CH) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// StatsSummary — aggregates over a period (for the admin dashboard).
type StatsSummary struct {
	Total  int64 `json:"total"`
	Unique int64 `json:"unique"`
	Bots   int64 `json:"bots"`
}

// Summary computes event aggregates over a period. The query is parameterized (no
// concatenation) — unlike the string-built SQL in the old admin panel.
func (c *CH) Summary(ctx context.Context, from, to time.Time) (StatsSummary, error) {
	// count()/countIf() in ClickHouse are UInt64, so we scan into uint64.
	var total, uniq, bots uint64
	row := c.conn.QueryRow(ctx,
		`SELECT count() AS total,
		        countIf(uniq = 1) AS uniq,
		        countIf(bot != '' AND bot != '-') AS bots
		 FROM events
		 WHERE ts >= ? AND ts < ?`,
		from, to,
	)
	if err := row.Scan(&total, &uniq, &bots); err != nil {
		return StatsSummary{}, err
	}
	return StatsSummary{Total: int64(total), Unique: int64(uniq), Bots: int64(bots)}, nil
}

// TSPoint — a time-series point for charts.
type TSPoint struct {
	TS     time.Time `json:"ts"`
	Total  int64     `json:"total"`
	Unique int64     `json:"unique"`
	Bots   int64     `json:"bots"`
}

// TimeSeries — aggregates over time (stepSec-second step) for a period. Across all groups.
func (c *CH) TimeSeries(ctx context.Context, from, to time.Time, stepSec int) ([]TSPoint, error) {
	if stepSec <= 0 {
		stepSec = 3600
	}
	rows, err := c.conn.Query(ctx,
		`SELECT toStartOfInterval(ts, INTERVAL ? second) AS t,
		        count() AS total, countIf(uniq=1) AS uniq,
		        countIf(bot!='' AND bot!='-') AS bots
		 FROM events WHERE ts >= ? AND ts < ?
		 GROUP BY t ORDER BY t`,
		stepSec, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TSPoint
	for rows.Next() {
		var p TSPoint
		var total, uniq, bots uint64
		if err := rows.Scan(&p.TS, &total, &uniq, &bots); err != nil {
			return nil, err
		}
		p.Total, p.Unique, p.Bots = int64(total), int64(uniq), int64(bots)
		out = append(out, p)
	}
	return out, rows.Err()
}

// KV — a key/count pair for breakdowns.
type KV struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// breakdownDims — an allowlist of columns for grouping (protection against injection).
var breakdownDims = map[string]string{
	"device": "device", "country": "country", "os": "os",
	"browser": "browser", "brand": "brand", "operator": "operator",
	"group": "group_name", "stream": "stream_name", "bot": "bot",
	"domain": "domain", "city": "city", "lang": "lang",
}

// DeleteGroupLogs deletes a group's events (ALTER ... DELETE, asynchronous mutation).
func (c *CH) DeleteGroupLogs(ctx context.Context, group string) error {
	return c.conn.Exec(ctx, "ALTER TABLE events DELETE WHERE group_name = ?", group)
}

// Breakdown — a breakdown by dimension dim over a period (top limit).
func (c *CH) Breakdown(ctx context.Context, from, to time.Time, dim string, limit int) ([]KV, error) {
	col, ok := breakdownDims[dim]
	if !ok {
		return nil, fmt.Errorf("store: unknown dimension %q", dim)
	}
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := c.conn.Query(ctx,
		`SELECT `+col+` AS k, count() AS n FROM events
		 WHERE ts >= ? AND ts < ? GROUP BY k ORDER BY n DESC LIMIT ?`,
		from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KV
	for rows.Next() {
		var kv KV
		var n uint64
		if err := rows.Scan(&kv.Key, &n); err != nil {
			return nil, err
		}
		kv.Count = int64(n)
		out = append(out, kv)
	}
	return out, rows.Err()
}

// LogFilter — log-viewing filters (all optional). Categorical fields are
// value lists (multi-select → SQL IN), IP is a single substring.
type LogFilter struct {
	From, To             time.Time
	Group, Stream        []string
	Country, Device      []string
	OS, Browser, Brand   []string
	IP                   string
	BotsOnly, HumansOnly bool
	Limit, Offset        int
}

const (
	// defaultLogRows — one page of the interactive log table.
	defaultLogRows = 100
	// maxLogRows bounds a single Logs call. The ceiling is the size of a CSV
	// export, not of a page: the export legitimately asks for a whole period,
	// and clamping it down to a page size silently truncated every export to
	// defaultLogRows rows. Paginating the interactive endpoint is the admin
	// layer's job (see maxPageRows there).
	maxLogRows = 50000
)

// clampLogLimit bounds a requested row count to something the process can hold.
func clampLogLimit(limit int) int {
	if limit <= 0 {
		return defaultLogRows
	}
	if limit > maxLogRows {
		return maxLogRows
	}
	return limit
}

// LogRow — a log row for the table.
type LogRow struct {
	TS      time.Time `json:"ts"`
	Group   string    `json:"group"`
	Stream  string    `json:"stream"`
	Country string    `json:"country"`
	City    string    `json:"city"`
	Device  string    `json:"device"`
	OS      string    `json:"os"`
	Browser string    `json:"browser"`
	Brand   string    `json:"brand"`
	Bot     string    `json:"bot"`
	Uniq    uint8     `json:"uniq"`
	IP      string    `json:"ip"`
	Keyword string    `json:"keyword"`
	Out     string    `json:"out"`
}

// Logs returns log rows matching the filter and the total number of matching rows.
// All conditions are parameterized (no concatenation of values).
func (c *CH) Logs(ctx context.Context, f LogFilter) ([]LogRow, int64, error) {
	where := "ts >= ? AND ts < ?"
	args := []any{f.From, f.To}
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		ph := make([]string, len(vals))
		for i, v := range vals {
			ph[i] = "?"
			args = append(args, v)
		}
		where += " AND " + col + " IN (" + strings.Join(ph, ",") + ")"
	}
	eq := func(col, val string) {
		if val != "" {
			where += " AND " + col + " = ?"
			args = append(args, val)
		}
	}
	in("group_name", f.Group)
	in("stream_name", f.Stream)
	in("country", f.Country)
	in("device", f.Device)
	in("os", f.OS)
	in("browser", f.Browser)
	in("brand", f.Brand)
	eq("ip", f.IP)
	if f.BotsOnly {
		where += " AND bot != '' AND bot != '-'"
	}
	if f.HumansOnly {
		where += " AND (bot = '' OR bot = '-')"
	}

	var total uint64
	if err := c.conn.QueryRow(ctx, "SELECT count() FROM events WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := clampLogLimit(f.Limit)
	q := "SELECT ts, group_name, stream_name, country, city, device, os, browser, brand, bot, uniq, ip, keyword, out" +
		" FROM events WHERE " + where + " ORDER BY ts DESC LIMIT ? OFFSET ?"
	rows, err := c.conn.Query(ctx, q, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var r LogRow
		if err := rows.Scan(&r.TS, &r.Group, &r.Stream, &r.Country, &r.City, &r.Device,
			&r.OS, &r.Browser, &r.Brand, &r.Bot, &r.Uniq, &r.IP, &r.Keyword, &r.Out); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, int64(total), rows.Err()
}

// RecordPostback finds an event by cid and writes a conversion to postbacks.
// Returns an error if the event is not found.
func (c *CH) RecordPostback(ctx context.Context, cid string, profit float64) error {
	var gname, sname, country, city, device, operator, domain, page string
	err := c.conn.QueryRow(ctx,
		`SELECT group_name, stream_name, country, city, device, operator, domain, page
		 FROM events WHERE cid = ? ORDER BY ts DESC LIMIT 1`, cid).
		Scan(&gname, &sname, &country, &city, &device, &operator, &domain, &page)
	if err != nil {
		return err
	}
	batch, err := c.conn.PrepareBatch(ctx, `INSERT INTO postbacks
		(domain, page, device, operator, country, city, profit, group_name, stream_name, cid)`)
	if err != nil {
		return err
	}
	if err := batch.Append(domain, page, device, operator, country, city, profit, gname, sname, cid); err != nil {
		return err
	}
	return batch.Send()
}

// PostbackRow — a conversion row.
type PostbackRow struct {
	TS      time.Time `json:"ts"`
	Group   string    `json:"group"`
	Stream  string    `json:"stream"`
	Domain  string    `json:"domain"`
	Page    string    `json:"page"`
	Device  string    `json:"device"`
	Country string    `json:"country"`
	City    string    `json:"city"`
	Profit  float64   `json:"profit"`
	CID     string    `json:"cid"`
}

// Postbacks returns conversions over a period (optional group filter) and the total profit.
func (c *CH) Postbacks(ctx context.Context, from, to time.Time, group string, limit int) ([]PostbackRow, float64, error) {
	where := "ts >= ? AND ts < ?"
	args := []any{from, to}
	if group != "" {
		where += " AND group_name = ?"
		args = append(args, group)
	}
	var total float64
	if err := c.conn.QueryRow(ctx, "SELECT sum(profit) FROM postbacks WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := c.conn.Query(ctx,
		"SELECT ts, group_name, stream_name, domain, page, device, country, city, profit, cid FROM postbacks WHERE "+
			where+" ORDER BY ts DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []PostbackRow
	for rows.Next() {
		var p PostbackRow
		if err := rows.Scan(&p.TS, &p.Group, &p.Stream, &p.Domain, &p.Page, &p.Device, &p.Country, &p.City, &p.Profit, &p.CID); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// InsertEvents inserts a batch of events in a single batch request (lock-free).
func (c *CH) InsertEvents(ctx context.Context, events []logbuf.Event) error {
	if len(events) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, `INSERT INTO events
		(ts, group_id, group_name, stream_name, out, keyword, redirect, device,
		 operator, country, city, region, lang, uniq, bot, ip, referer, useragent,
		 domain, page, se, os, os_version, browser, browser_ver, brand,
		 counter, cid, postback)`)
	if err != nil {
		return fmt.Errorf("clickhouse: prepare batch: %w", err)
	}
	for _, e := range events {
		if err := batch.Append(
			e.Ts, e.GroupID, e.GroupName, e.Stream, e.Out, e.Keyword, e.Redirect,
			e.Device, e.Operator, e.Country, e.City, e.Region, e.Lang, e.Uniq,
			e.Bot, e.IP, e.Referer, e.UserAgent, e.Domain, e.Page, e.SE,
			e.OS, e.OSVersion, e.Browser, e.BrowserV, e.Brand,
			e.Counter, e.CID, e.Postback,
		); err != nil {
			return fmt.Errorf("clickhouse: append: %w", err)
		}
	}
	return batch.Send()
}
