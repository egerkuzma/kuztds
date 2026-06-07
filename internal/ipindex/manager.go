package ipindex

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Set manages a collection of named IP lists (blacklist, ip_google, wap, …).
// Each list is a "<name>.dat" file in the dir directory, wrapped in a Holder for
// atomic replacement. Reload re-reads only files whose mtime/size changed, so the
// background update is cheap even for large lists.
//
// Files are loaded into memory once and updated in the background.
type Set struct {
	dir string
	log *slog.Logger

	mu      sync.RWMutex
	holders map[string]*Holder
	stamps  map[string]stamp // file fingerprint for change detection
}

type stamp struct {
	mod  time.Time
	size int64
}

// NewSet creates a list manager that reads files from the dir directory.
func NewSet(dir string, log *slog.Logger) *Set {
	if log == nil {
		log = slog.Default()
	}
	return &Set{
		dir:     dir,
		log:     log,
		holders: make(map[string]*Holder),
		stamps:  make(map[string]stamp),
	}
}

// Load performs the initial load of the listed lists.
// A missing or corrupt individual file does not fail the whole set: for such a
// list an empty index is created and the error is logged (the "fail open"
// behavior is safer for the hot path than crashing the engine).
func (s *Set) Load(names ...string) {
	for _, name := range names {
		idx, st, err := s.read(name)
		if err != nil {
			s.log.Warn("ipindex: list not loaded", "name", name, "err", err)
			idx = &Index{}
		}
		s.mu.Lock()
		s.holders[name] = NewHolder(idx)
		if st != nil {
			s.stamps[name] = *st
		}
		s.mu.Unlock()
		s.log.Info("ipindex: list loaded", "name", name, "ranges", idx.Len())
	}
}

// Get returns the current index of a list (nil-safe: empty for an unknown name).
func (s *Set) Get(name string) *Index {
	s.mu.RLock()
	h := s.holders[name]
	s.mu.RUnlock()
	if h == nil {
		return &Index{}
	}
	return h.Load()
}

// Lookup — look up an ip in the named list.
func (s *Set) Lookup(name string, ip netip.Addr) (label string, ok bool) {
	return s.Get(name).Lookup(ip)
}

// Reload re-reads all lists whose file changed and atomically swaps the index.
// Returns the names of the lists that were actually updated.
func (s *Set) Reload() (changed []string) {
	s.mu.RLock()
	names := make([]string, 0, len(s.holders))
	for name := range s.holders {
		names = append(names, name)
	}
	s.mu.RUnlock()

	for _, name := range names {
		st, err := s.statOf(name)
		if err != nil {
			continue // file disappeared — keep the previous index
		}
		s.mu.RLock()
		prev, known := s.stamps[name]
		s.mu.RUnlock()
		if known && prev.mod.Equal(st.mod) && prev.size == st.size {
			continue // unchanged
		}

		idx, ns, err := s.read(name)
		if err != nil {
			s.log.Warn("ipindex: reload failed", "name", name, "err", err)
			continue
		}
		s.mu.Lock()
		if h := s.holders[name]; h != nil {
			h.Store(idx)
		} else {
			s.holders[name] = NewHolder(idx)
		}
		if ns != nil {
			s.stamps[name] = *ns
		}
		s.mu.Unlock()
		changed = append(changed, name)
		s.log.Info("ipindex: list reloaded", "name", name, "ranges", idx.Len())
	}
	return changed
}

// Watch runs a periodic Reload until ctx is cancelled.
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

func (s *Set) path(name string) string {
	return filepath.Join(s.dir, name+".dat")
}

func (s *Set) statOf(name string) (*stamp, error) {
	fi, err := os.Stat(s.path(name))
	if err != nil {
		return nil, err
	}
	return &stamp{mod: fi.ModTime(), size: fi.Size()}, nil
}

func (s *Set) read(name string) (*Index, *stamp, error) {
	st, err := s.statOf(name)
	if err != nil {
		return nil, nil, fmt.Errorf("stat: %w", err)
	}
	idx, err := LoadFile(s.path(name))
	if err != nil {
		return nil, st, err
	}
	return idx, st, nil
}
