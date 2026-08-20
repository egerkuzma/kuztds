package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/ipindex"
)

// writeGroups writes a groups file and makes sure its mtime differs from the
// previous write — the watcher fingerprints by mtime+size, and two writes
// within the same filesystem timestamp tick would otherwise look identical.
func writeGroups(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Duration(len(body)+1) * time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func newWatcherFor(t *testing.T, dir, path string, groups *config.Groups) *groupsWatcher {
	t.Helper()
	lists := ipindex.NewSet(dir, discardLog())
	lists.Load(ipLists...)
	return newGroupsWatcher(path, groups, lists, discardLog(), append(append([]string(nil), ipLists...), ipListFiles(groups)...))
}

func TestGroupsReloadPicksUpEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	writeGroups(t, path, `[{"id":"promo","status":true,"streams":[
		{"name":"s","status":true,"out":{"redirect":"show_text","out":"OLD"}}]}]`)

	groups, err := config.LoadGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newWatcherFor(t, dir, path, groups)

	// An untouched file must not cost a reload.
	if changed, err := w.reload(); changed || err != nil {
		t.Fatalf("unchanged file: changed=%v err=%v", changed, err)
	}

	// Rename the group and change its output, the way the admin panel would.
	writeGroups(t, path, `[{"id":"promo","status":true,"streams":[
		{"name":"s","status":true,"out":{"redirect":"show_text","out":"NEW"}}]},
		{"id":"extra","status":true,"streams":[]}]`)
	changed, err := w.reload()
	if err != nil || !changed {
		t.Fatalf("edited file: changed=%v err=%v", changed, err)
	}

	g, ok := groups.Get("promo")
	if !ok {
		t.Fatal("promo missing after reload")
	}
	if got := g.Streams[0].Out.Out; got != "NEW" {
		t.Errorf("out = %q after reload, want NEW", got)
	}
	if _, ok := groups.Get("extra"); !ok {
		t.Error("group added by the edit is not visible after reload")
	}
}

// A broken file must never replace a working config, and it must be retried
// rather than remembered as "seen".
func TestGroupsReloadKeepsConfigOnBadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	writeGroups(t, path, `[{"id":"promo","status":true,"streams":[
		{"name":"s","status":true,"out":{"redirect":"show_text","out":"GOOD"}}]}]`)
	groups, err := config.LoadGroups(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newWatcherFor(t, dir, path, groups)

	writeGroups(t, path, `{ this is not valid json`)
	if changed, err := w.reload(); changed || err == nil {
		t.Fatalf("bad json: changed=%v err=%v, want no swap and an error", changed, err)
	}
	g, ok := groups.Get("promo")
	if !ok || g.Streams[0].Out.Out != "GOOD" {
		t.Fatal("the previous config must survive a malformed file")
	}

	// Fixing the file recovers without a restart — the failed read did not
	// advance the stamp.
	writeGroups(t, path, `[{"id":"promo","status":true,"streams":[
		{"name":"s","status":true,"out":{"redirect":"show_text","out":"FIXED"}}]}]`)
	if changed, err := w.reload(); !changed || err != nil {
		t.Fatalf("recovery: changed=%v err=%v", changed, err)
	}
	g, _ = groups.Get("promo")
	if got := g.Streams[0].Out.Out; got != "FIXED" {
		t.Errorf("out = %q, want FIXED", got)
	}
}

// A missing file is not a reason to drop the running config either.
func TestGroupsReloadKeepsConfigWhenFileDisappears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	writeGroups(t, path, `[{"id":"promo","status":true,"streams":[]}]`)
	groups, _ := config.LoadGroups(path)
	w := newWatcherFor(t, dir, path, groups)

	os.Remove(path)
	if changed, err := w.reload(); changed || err == nil {
		t.Fatalf("missing file: changed=%v err=%v", changed, err)
	}
	if _, ok := groups.Get("promo"); !ok {
		t.Error("config must survive the file going missing")
	}
}

// A stream added by the edit may reference an ip_list the engine never read.
// Publishing the config before loading that list would make the filter match
// nothing — silently.
func TestGroupsReloadLoadsNewlyReferencedIPList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	writeGroups(t, path, `[{"id":"promo","status":true,"streams":[]}]`)
	if err := os.WriteFile(filepath.Join(dir, "vip.dat"), []byte("9.9.9.0/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	groups, _ := config.LoadGroups(path)
	lists := ipindex.NewSet(dir, discardLog())
	lists.Load(ipLists...)
	w := newGroupsWatcher(path, groups, lists, discardLog(), ipLists)

	// Not referenced yet → not loaded.
	if _, ok := lists.Lookup("vip", netip.MustParseAddr("9.9.9.9")); ok {
		t.Fatal("vip must not be loaded before anything references it")
	}

	writeGroups(t, path, `[{"id":"promo","status":true,"streams":[
		{"name":"s","status":true,"rules":{"ip_list":{"flag":2,"file":"vip.dat"}},
		 "out":{"redirect":"show_text","out":"X"}}]}]`)
	if changed, err := w.reload(); !changed || err != nil {
		t.Fatalf("reload: changed=%v err=%v", changed, err)
	}
	if _, ok := lists.Lookup("vip", netip.MustParseAddr("9.9.9.9")); !ok {
		t.Error("the ip_list referenced by the new stream must be loaded by the reload")
	}
}
