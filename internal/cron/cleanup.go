package cron

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runCleanup deletes keyword files older than KeysDays. Event rows need no
// job: ClickHouse drops them by the table's TTL.
func (r *Runner) runCleanup(cfg Cleanup) (string, error) {
	if cfg.KeysDays <= 0 || r.paths.KeysDir == "" {
		return "nothing to clean: no retention set for keyword files", nil
	}
	cutoff := r.now().Add(-time.Duration(cfg.KeysDays) * 24 * time.Hour)
	removed := 0
	err := filepath.WalkDir(r.paths.KeysDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".dat") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return nil
		}
		if os.Remove(path) == nil {
			removed++
		}
		return nil
	})
	return fmt.Sprintf("%d keyword file(s) older than %d days removed", removed, cfg.KeysDays), err
}
