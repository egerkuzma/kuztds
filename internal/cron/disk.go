package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// runDisk checks free space and tells Telegram when a path crosses the
// threshold — once on the way down and once on the way back up, not on every
// run in between.
func (r *Runner) runDisk(ctx context.Context, cfg Config) (string, error) {
	paths := cfg.Disk.Paths
	if len(paths) == 0 {
		paths = []string{r.paths.DataDir}
	}
	limit := cfg.Disk.MinFreePercent
	if limit <= 0 {
		limit = 10
	}
	var parts []string
	var errs []error
	for _, p := range paths {
		free, total, err := r.disk(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		pct := 0
		if total > 0 {
			pct = int(free * 100 / total)
		}
		part := fmt.Sprintf("%s: %d%% free (%s of %s)", p, pct, gib(free), gib(total))

		var alerted, was bool
		r.state(func(st *Status) { _, was = st.DiskAlerted[p] })
		switch {
		case pct < limit && !was:
			part += r.notifyIfConfigured(ctx, cfg.Telegram,
				fmt.Sprintf("⚠️ KuzTDS: low disk space on %s — %d%% free (%s), threshold %d%%.", p, pct, gib(free), limit))
			alerted = true
		case pct >= limit && was:
			part += r.notifyIfConfigured(ctx, cfg.Telegram,
				fmt.Sprintf("✅ KuzTDS: disk space on %s is back to %d%% free (%s).", p, pct, gib(free)))
		}
		r.state(func(st *Status) {
			if pct < limit {
				if alerted || was {
					if st.DiskAlerted == nil {
						st.DiskAlerted = map[string]int{}
					}
					st.DiskAlerted[p] = pct
				}
			} else {
				delete(st.DiskAlerted, p)
			}
		})
		if pct < limit {
			part += " — LOW"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; "), errors.Join(errs...)
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
