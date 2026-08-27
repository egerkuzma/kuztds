package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/seplist"
)

// groupsWatcher re-reads the groups config when its file changes and swaps it
// into the live *config.Groups.
//
// It mirrors ipindex.Set and detect.Signatures: the file is fingerprinted by
// mtime+size, an unchanged file costs one stat, and the swap is atomic
// (config.Groups.Replace under its own lock). Readers on the hot path hold a
// *config.Group pointer from the generation they started with — LoadGroups
// builds a fresh slice every time and nothing mutates it afterwards, so an
// in-flight request keeps serving a consistent snapshot instead of seeing the
// config change underneath it.
//
// The admin writes the file with a temp-file + rename, so a reload never
// observes a half-written config.
type groupsWatcher struct {
	path   string
	groups *config.Groups
	lists  *ipindex.Set
	seps   *seplist.Set
	log    *slog.Logger

	stamp  fileStamp
	loaded map[string]bool // ip_list names already present in lists
}

type fileStamp struct {
	mod  time.Time
	size int64
}

// newGroupsWatcher builds a watcher for an already-loaded config. known is the
// set of ip list names loaded at startup, so a reload only pulls in files it
// has not seen before.
func newGroupsWatcher(path string, groups *config.Groups, lists *ipindex.Set, seps *seplist.Set, log *slog.Logger, known []string) *groupsWatcher {
	w := &groupsWatcher{path: path, groups: groups, lists: lists, seps: seps, log: log, loaded: map[string]bool{}}
	for _, n := range known {
		w.loaded[n] = true
	}
	if st, err := statFile(path); err == nil {
		w.stamp = *st
	}
	return w
}

// reload re-reads the file if it changed and swaps the config in. It reports
// whether a swap happened. A missing or malformed file leaves the running
// config untouched: serving the previous rules is always better than serving
// none, and the admin can be mid-write.
func (w *groupsWatcher) reload() (bool, error) {
	st, err := statFile(w.path)
	if err != nil {
		return false, err
	}
	if st.mod.Equal(w.stamp.mod) && st.size == w.stamp.size {
		return false, nil
	}
	fresh, err := config.LoadGroups(w.path)
	if err != nil {
		// Do not advance the stamp: a file that failed to parse must be retried
		// on the next tick, not written off as "seen".
		return false, err
	}
	// Load ip_list files the new config references before publishing it.
	// Otherwise a per-stream IP filter added through the admin panel would go
	// live pointing at a list the engine never read, and silently match nothing
	// — the exact failure that ipListFiles() was introduced to fix at startup.
	for _, name := range ipListFiles(fresh) {
		if w.loaded[name] {
			continue
		}
		w.lists.Load(name)
		w.loaded[name] = true
		w.log.Info("groups reload: ip list loaded", "name", name)
	}
	// Same for separation lists: a rule added through the admin panel would
	// otherwise point at a file the engine never read and silently match
	// nothing, which looks exactly like "no keyword matched".
	if w.seps != nil {
		w.seps.Load(separationFiles(fresh)...)
	}
	w.groups.Replace(fresh)
	w.stamp = *st
	w.log.Info("groups reloaded", "path", w.path, "count", len(fresh.List()))
	return true, nil
}

// watch runs reload on a ticker until ctx is cancelled.
func (w *groupsWatcher) watch(ctx context.Context, interval time.Duration) {
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
			if _, err := w.reload(); err != nil {
				w.log.Warn("groups reload failed, keeping the previous config", "err", err)
			}
		}
	}
}

func statFile(path string) (*fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &fileStamp{mod: fi.ModTime(), size: fi.Size()}, nil
}
