package detect

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Signatures keeps signature lists (signature_ua/ref, ua_blacklist) in memory
// with atomic swap on hot-reload — analogous to ipindex.Set, but for strings.
type Signatures struct {
	dir string
	log *slog.Logger

	ua      atomic.Pointer[[]string]
	ref     atomic.Pointer[[]string]
	uaBlack atomic.Pointer[[]string]

	stamps map[string]fileStamp
	smu    chan struct{} // lightweight mutex for stamps (buffer 1)
}

type fileStamp struct {
	mod  time.Time
	size int64
}

const (
	fileUA      = "signature_ua"
	fileRef     = "signature_ref"
	fileUABlack = "ua_blacklist"
)

// NewSignatures creates the holder and performs the initial load.
func NewSignatures(dir string, log *slog.Logger) *Signatures {
	if log == nil {
		log = slog.Default()
	}
	s := &Signatures{dir: dir, log: log, stamps: map[string]fileStamp{}, smu: make(chan struct{}, 1)}
	s.smu <- struct{}{}
	empty := []string{}
	s.ua.Store(&empty)
	s.ref.Store(&empty)
	s.uaBlack.Store(&empty)
	s.Reload()
	return s
}

// UA returns the current list of UA signatures.
func (s *Signatures) UA() []string { return *s.ua.Load() }

// Ref returns the current list of referer signatures.
func (s *Signatures) Ref() []string { return *s.ref.Load() }

// UABlacklist returns the current UA blacklist.
func (s *Signatures) UABlacklist() []string { return *s.uaBlack.Load() }

// Reload re-reads changed files and atomically swaps the lists.
func (s *Signatures) Reload() {
	s.reloadOne(fileUA, &s.ua)
	s.reloadOne(fileRef, &s.ref)
	s.reloadOne(fileUABlack, &s.uaBlack)
}

func (s *Signatures) reloadOne(name string, dst *atomic.Pointer[[]string]) {
	path := filepath.Join(s.dir, name+".dat")
	fi, err := os.Stat(path)
	if err != nil {
		return // no file — keep the previous (empty) list
	}
	cur := fileStamp{mod: fi.ModTime(), size: fi.Size()}

	<-s.smu
	prev, known := s.stamps[name]
	s.smu <- struct{}{}
	if known && prev.mod.Equal(cur.mod) && prev.size == cur.size {
		return
	}

	lines, err := readLines(path)
	if err != nil {
		s.log.Warn("detect: signatures reload failed", "name", name, "err", err)
		return
	}
	dst.Store(&lines)

	<-s.smu
	s.stamps[name] = cur
	s.smu <- struct{}{}
	s.log.Info("detect: signatures loaded", "name", name, "lines", len(lines))
}

// Watch runs periodic Reload until ctx is cancelled.
func (s *Signatures) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reload()
		}
	}
}

// readLines reads non-empty lines of the file, skipping comments (#).
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
