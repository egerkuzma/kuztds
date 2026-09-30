package cron

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/store"
)

// maxAnnounce bounds one run: a backlog after downtime becomes a summary, not
// a flood.
const maxAnnounce = 20

// runConversions announces the postbacks recorded since the last run.
func (r *Runner) runConversions(ctx context.Context, cfg Config) (string, error) {
	if r.pb == nil {
		return "", errors.New("no ClickHouse connection: set KUZTDS_CLICKHOUSE_ADDR for the cron service")
	}
	if !cfg.Telegram.Configured() {
		return "", errors.New("telegram is not configured")
	}
	// Whole seconds: a postback's timestamp has second precision, so a window
	// that ends mid-second would skip the ones recorded later in that second.
	to := r.now().Truncate(time.Second)
	var from time.Time
	r.state(func(st *Status) { from = st.ConvSince })
	if from.IsZero() {
		// First run: start from now rather than announce history.
		r.state(func(st *Status) { st.ConvSince = to })
		return "watching for new conversions", nil
	}
	rows, _, err := r.pb.Postbacks(ctx, from, to, "", 1000)
	if err != nil {
		return "", err
	}
	tpl := cfg.Conversions.Template
	if strings.TrimSpace(tpl) == "" {
		tpl = DefaultConversionTemplate
	}
	// rows come newest first; announce in the order they happened.
	sent := 0
	for i := len(rows) - 1; i >= 0 && sent < maxAnnounce; i-- {
		if err := r.notify(ctx, cfg.Telegram, conversionText(tpl, rows[i])); err != nil {
			// Do not advance: the next run retries from the same point.
			return fmt.Sprintf("%d of %d announced", sent, len(rows)), err
		}
		sent++
	}
	if rest := len(rows) - sent; rest > 0 {
		sum := 0.0
		for _, p := range rows[:rest] {
			sum += p.Profit
		}
		if err := r.notify(ctx, cfg.Telegram, fmt.Sprintf("… and %d more conversions, %.2f in total.", rest, sum)); err != nil {
			return fmt.Sprintf("%d of %d announced", sent, len(rows)), err
		}
	}
	r.state(func(st *Status) { st.ConvSince = to })
	return fmt.Sprintf("%d new conversion(s)", len(rows)), nil
}

func conversionText(tpl string, p store.PostbackRow) string {
	return strings.NewReplacer(
		"[PROFIT]", strconv.FormatFloat(p.Profit, 'f', 2, 64),
		"[GROUP]", p.Group, "[STREAM]", p.Stream,
		"[COUNTRY]", strings.ToUpper(p.Country), "[CITY]", p.City,
		"[DEVICE]", p.Device, "[DOMAIN]", p.Domain, "[CID]", p.CID,
	).Replace(tpl)
}
