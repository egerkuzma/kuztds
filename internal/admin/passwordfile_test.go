package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/security"
)

func goodHash(t *testing.T) string {
	t.Helper()
	h, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func baseConfig(t *testing.T, hash, file string) Config {
	t.Helper()
	return Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		PasswordFile: file,
		Sessions:     security.NewMemoryStore(),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
	}
}

// A missing file is the ordinary first boot: nobody has changed the password
// yet, so the environment value is the right one and must not be an error.
func TestNewFallsBackWhenTheFileIsAbsent(t *testing.T) {
	hash := goodHash(t)
	s, err := New(baseConfig(t, hash, filepath.Join(t.TempDir(), "never-written")))
	if err != nil {
		t.Fatalf("a clean deployment must start: %v", err)
	}
	if s.currentHash() != hash {
		t.Error("expected the environment hash")
	}
}

// An unreadable file is not a missing one. Starting on the environment value
// would silently restore the password that was replaced: the old one works, the
// new one does not, and nothing anywhere says why.
func TestNewRefusesAnUnreadableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(goodHash(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	_, err := New(baseConfig(t, goodHash(t), p))
	if err == nil {
		t.Fatal("expected a refusal, not a silent fallback to the old password")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error %q does not name the file; that is the first thing an operator needs", err)
	}
}

// An empty file is what a torn write leaves behind — os.WriteFile truncates
// first — and it used to fall into the same silent branch as a missing one.
func TestNewRefusesAnEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(baseConfig(t, goodHash(t), p)); err == nil {
		t.Fatal("an empty password file must not read as 'no password change happened'")
	}
}

// Publication is the only door, so the constructor is gated too — that is where
// the two direct assignments used to live.
func TestNewRefusesAnUnusableHash(t *testing.T) {
	bad := "$argon2id$v=19$m=65536,t=0,p=2$c2FsdA$aGFzaA"
	if _, err := New(baseConfig(t, bad, "")); err == nil {
		t.Fatal("a Server must not exist holding a hash it cannot verify against")
	}
}

// The hash and its fingerprint are published together, so they cannot drift.
func TestFingerprintTracksTheHash(t *testing.T) {
	s, err := New(baseConfig(t, goodHash(t), ""))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.current().fp, security.PasswordFingerprint(s.currentHash()); got != want {
		t.Fatalf("fp = %q, want %q", got, want)
	}
	next := goodHash(t)
	if err := s.publish(next); err != nil {
		t.Fatal(err)
	}
	if got, want := s.current().fp, security.PasswordFingerprint(next); got != want {
		t.Fatalf("after publish fp = %q, want %q", got, want)
	}
}

func TestPublishRejectsWhatItCannotReadBack(t *testing.T) {
	s, err := New(baseConfig(t, goodHash(t), ""))
	if err != nil {
		t.Fatal(err)
	}
	before := s.currentHash()
	if err := s.publish("not a hash at all"); err == nil {
		t.Fatal("expected a refusal")
	}
	if s.currentHash() != before {
		t.Fatal("a rejected hash must not become current")
	}
}
