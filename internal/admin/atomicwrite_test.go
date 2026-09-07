package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileAtomicHonoursMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pw")
	if err := writeFileAtomic(p, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600 — a password hash the machine can read", got)
	}
}

// os.WriteFile into an existing file leaves that file's mode alone. With a
// fixed "<path>.tmp" name, a 0644 leftover from an earlier crash would swallow
// the next 0600 write and hand the hash to everyone. os.CreateTemp cannot: the
// name is fresh and the file is born 0600.
func TestWriteFileAtomicIgnoresAStaleTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pw")
	if err := os.WriteFile(p+".tmp", []byte("leftover"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

// A temporary file holding a valid password hash must not survive the call —
// under a name nobody thinks to look at, it is a second copy of the secret.
func TestWriteFileAtomicLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pw")
	if err := writeFileAtomic(p, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range dirEntries(t, dir) {
		if strings.Contains(name, "tmp") {
			t.Fatalf("left %q behind", name)
		}
	}
}

func TestWriteFileAtomicCleansUpAfterAFailedRename(t *testing.T) {
	dir := t.TempDir()
	// The target is a directory, so Rename fails after the temporary file has
	// already been written and chmodded.
	p := filepath.Join(dir, "target")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("secret"), 0o600); err == nil {
		t.Fatal("expected the rename to fail")
	}
	for _, name := range dirEntries(t, dir) {
		if name != "target" {
			t.Fatalf("a failed write left %q holding the secret", name)
		}
	}
}

// The reader sees the old content or the new one, never a truncated file.
func TestWriteFileAtomicReplacesWholeContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pw")
	if err := writeFileAtomic(p, []byte("first-and-longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second" {
		t.Fatalf("content = %q", b)
	}
}
