package seplist

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func write(t testing.TB, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".dat"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLookup carries over what the old per-request separationOut was tested
// for: substring match, case-insensitive, lines without a separator skipped.
func TestLookup(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sep", "buy;https://shop\n# comment line no semicolon\nsale;https://sale\n")
	s := NewSet(dir, discard())
	s.Load("sep")

	if got := s.Lookup("sep.dat", "iWantToBUY now"); got != "https://shop" {
		t.Errorf("substring + case-insensitive match: got %q", got)
	}
	if got := s.Lookup("sep", "big SALE today"); got != "https://sale" {
		t.Errorf("name without .dat must work too: got %q", got)
	}
	if got := s.Lookup("sep.dat", "nothing"); got != "" {
		t.Errorf("no match → empty, got %q", got)
	}
	if got := s.Lookup("missing.dat", "buy"); got != "" {
		t.Errorf("unloaded list → empty, got %q", got)
	}
	if got := s.Lookup("sep.dat", ""); got != "" {
		t.Errorf("empty key → empty, got %q", got)
	}
}

// TestNilSetLookup: an engine assembled without separation lists must behave as
// if no keyword matched, not panic on the hot path.
func TestNilSetLookup(t *testing.T) {
	var s *Set
	if got := s.Lookup("sep.dat", "buy"); got != "" {
		t.Errorf("nil set → empty, got %q", got)
	}
}

// TestMissingFileIsEmptyNotFatal: a list named in the config but absent on disk
// must leave the stream serving its configured output.
func TestMissingFileIsEmptyNotFatal(t *testing.T) {
	s := NewSet(t.TempDir(), discard())
	s.Load("absent")
	if got := s.Lookup("absent", "buy"); got != "" {
		t.Errorf("absent file → empty, got %q", got)
	}
}

// TestReloadPicksUpChanges: the file is re-read when it changes, and an
// unchanged file is not re-parsed.
func TestReloadPicksUpChanges(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sep", "buy;https://old\n")
	s := NewSet(dir, discard())
	s.Load("sep")
	if got := s.Lookup("sep", "buy"); got != "https://old" {
		t.Fatalf("initial load: got %q", got)
	}

	// Same size would keep the stamp equal if mtime did not move, so shift the
	// mtime explicitly rather than relying on filesystem resolution.
	write(t, dir, "sep", "buy;https://new\n")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "sep.dat"), future, future); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if got := s.Lookup("sep", "buy"); got != "https://new" {
		t.Errorf("after reload: got %q, want the new value", got)
	}

	// An unchanged file must not move the answer either.
	s.Reload()
	if got := s.Lookup("sep", "buy"); got != "https://new" {
		t.Errorf("reload of an unchanged file changed the answer to %q", got)
	}
}

// TestConcurrentLookupDuringReload: the swap is atomic, so a lookup running
// alongside a reload sees one whole generation or the other, never a torn one.
func TestConcurrentLookupDuringReload(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sep", "buy;https://a\n")
	s := NewSet(dir, discard())
	s.Load("sep")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if got := s.Lookup("sep", "buy"); got != "https://a" && got != "https://b" {
					t.Errorf("torn read: %q", got)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		body := "buy;https://a\n"
		if i%2 == 1 {
			body = "buy;https://b\n"
		}
		write(t, dir, "sep", body)
		future := time.Now().Add(time.Duration(i+1) * time.Second)
		_ = os.Chtimes(filepath.Join(dir, "sep.dat"), future, future)
		s.Reload()
	}
	close(stop)
	wg.Wait()
}

func mkList(b *testing.B, dir string, n int) {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("keyword" + strconv.Itoa(i) + ";https://out" + strconv.Itoa(i) + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "sep.dat"), []byte(sb.String()), 0o644); err != nil {
		b.Fatal(err)
	}
}

// oldSeparationOut is the implementation this package replaced, kept here so the
// comparison in the package doc is reproducible rather than asserted.
func oldSeparationOut(dir, file, key string) string {
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return ""
	}
	lk := strings.ToLower(key)
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		kw, out, ok := strings.Cut(ln, ";")
		if !ok || kw == "" {
			continue
		}
		if strings.Contains(lk, strings.ToLower(kw)) {
			return strings.TrimSpace(out)
		}
	}
	return ""
}

// BenchmarkLookup vs BenchmarkOldSeparationOut, worst case: the query matches
// nothing, so the whole list is walked either way. The scan stays linear — what
// goes away is re-reading and re-parsing the file on every request.
func BenchmarkLookup(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		dir := b.TempDir()
		mkList(b, dir, n)
		s := NewSet(dir, discard())
		s.Load("sep")
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if o := s.Lookup("sep.dat", "buy an iphone right now"); o != "" {
					b.Fatal("unexpected hit")
				}
			}
		})
	}
}

func BenchmarkOldSeparationOut(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		dir := b.TempDir()
		mkList(b, dir, n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if o := oldSeparationOut(dir, "sep.dat", "buy an iphone right now"); o != "" {
					b.Fatal("unexpected hit")
				}
			}
		})
	}
}
