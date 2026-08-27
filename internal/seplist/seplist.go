// Package seplist keeps keyword→output separation lists in memory.
//
// A separation file is a "<name>.dat" of "keyword;out" lines. Matching is a
// case-insensitive substring test of the keyword against the visitor's query,
// so the scan stays linear — but it used to be a linear scan over a file read
// from disk on every request that carried a keyword.
//
// Measured worst case — the query matches nothing, so the whole list is walked
// either way (BenchmarkOldSeparationOut vs BenchmarkLookup):
//
//	         before                    after
//	 1000     62 µs   71 KB   8 allocs      8 µs   0 B   0 allocs
//	10000    514 µs  721 KB   8 allocs     87 µs   0 B   0 allocs
//	50000   2949 µs 3769 KB   8 allocs    432 µs   0 B   0 allocs
//
// At a thousand requests a second the old 50k case is three cores spent
// re-reading one unchanged file and nearly four gigabytes of garbage a second.
//
// What is fixed is the reading, not the searching. The match is a substring
// test, not equality, so a map would not help and the scan is still O(n) in the
// number of keywords: 50k keywords at a thousand rps is still about half a core.
// If that ever matters, the answer is a substring index (Aho-Corasick), not a
// different cache — and it should be driven by a measurement, not by this note.
package seplist

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// pair is one parsed line. Key is already lower-cased, so a lookup does not
// have to fold it again for every line of the file.
type pair struct {
	key string
	out string
}

type stamp struct {
	mod  time.Time
	size int64
}

// Set holds the separation lists named in the group config, one per file.
type Set struct {
	dir string
	log *slog.Logger

	mu     sync.RWMutex
	lists  map[string]*atomic.Pointer[[]pair]
	stamps map[string]stamp
}

// NewSet creates a set reading "<name>.dat" files from dir.
func NewSet(dir string, log *slog.Logger) *Set {
	if log == nil {
		log = slog.Default()
	}
	return &Set{
		dir:    dir,
		log:    log,
		lists:  make(map[string]*atomic.Pointer[[]pair]),
		stamps: make(map[string]stamp),
	}
}

// Load reads the named lists for the first time. A missing or unreadable file
// yields an empty list rather than an error: separation that matches nothing
// behaves exactly as it did before this package existed — the stream serves its
// configured output.
func (s *Set) Load(names ...string) {
	for _, name := range names {
		name = strings.TrimSuffix(name, ".dat")
		if name == "" {
			continue
		}
		s.mu.Lock()
		if _, ok := s.lists[name]; ok {
			s.mu.Unlock()
			continue
		}
		holder := &atomic.Pointer[[]pair]{}
		empty := []pair{}
		holder.Store(&empty)
		s.lists[name] = holder
		s.mu.Unlock()
		s.reloadOne(name, holder)
	}
}

// Lookup returns the output whose keyword occurs in key, or "" if none does.
// file may carry the ".dat" suffix, as it does in the config.
//
// Allocation-free: key is folded once, and the list holds pre-folded keywords.
// A nil *Set looks up nothing, so a caller assembled without separation lists
// behaves as if no keyword matched.
func (s *Set) Lookup(file, key string) string {
	if s == nil || file == "" || key == "" {
		return ""
	}
	name := strings.TrimSuffix(file, ".dat")
	s.mu.RLock()
	holder, ok := s.lists[name]
	s.mu.RUnlock()
	if !ok {
		return ""
	}
	lk := strings.ToLower(key)
	for _, p := range *holder.Load() {
		if strings.Contains(lk, p.key) {
			return p.out
		}
	}
	return ""
}

// Reload re-reads the lists whose file changed and swaps them in atomically.
// An unchanged file costs one stat.
func (s *Set) Reload() {
	s.mu.RLock()
	names := make([]string, 0, len(s.lists))
	holders := make([]*atomic.Pointer[[]pair], 0, len(s.lists))
	for name, h := range s.lists {
		names = append(names, name)
		holders = append(holders, h)
	}
	s.mu.RUnlock()
	for i, name := range names {
		s.reloadOne(name, holders[i])
	}
}

func (s *Set) reloadOne(name string, holder *atomic.Pointer[[]pair]) {
	path := filepath.Join(s.dir, name+".dat")
	fi, err := os.Stat(path)
	if err != nil {
		return // no file — keep whatever is loaded (an empty list at worst)
	}
	cur := stamp{mod: fi.ModTime(), size: fi.Size()}

	s.mu.RLock()
	prev, known := s.stamps[name]
	s.mu.RUnlock()
	if known && prev.mod.Equal(cur.mod) && prev.size == cur.size {
		return
	}

	pairs, err := readPairs(path)
	if err != nil {
		// Do not advance the stamp: a file that failed to read must be retried
		// on the next tick, not written off as seen.
		s.log.Warn("seplist: reload failed", "name", name, "err", err)
		return
	}
	holder.Store(&pairs)

	s.mu.Lock()
	s.stamps[name] = cur
	s.mu.Unlock()
	s.log.Info("seplist: loaded", "name", name, "pairs", len(pairs))
}

// Watch runs Reload on a ticker until ctx is cancelled.
func (s *Set) Watch(ctx context.Context, interval time.Duration) {
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

// readPairs parses "keyword;out" lines, lower-casing the keyword. Lines without
// a separator or with an empty keyword are skipped, matching what the previous
// per-request scan did.
func readPairs(path string) ([]pair, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []pair
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		kw, o, ok := strings.Cut(ln, ";")
		if !ok || kw == "" {
			continue
		}
		out = append(out, pair{key: strings.ToLower(kw), out: strings.TrimSpace(o)})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
