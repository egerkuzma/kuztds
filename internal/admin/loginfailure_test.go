package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/security"
)

// brokenLimiter stands for a limiter whose storage is unreachable.
type brokenLimiter struct{}

func (brokenLimiter) Allow(context.Context, string) (bool, error) {
	return true, errors.New("dial tcp: connection refused")
}

// failingSessions accepts nothing, the way a session store does when Redis is
// down. It is what turns a correct password into a 500.
type failingSessions struct{ security.SessionStore }

func (failingSessions) Create(context.Context, string, security.Session, time.Duration) error {
	return errors.New("dial tcp: connection refused")
}

func loginServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	s := New(cfg)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func postLogin(t *testing.T, base, user, pass string) int {
	t.Helper()
	resp, err := http.Post(base+"/api/login", "application/json",
		strings.NewReader(`{"Login":"`+user+`","Password":"`+pass+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestBrokenLimiterAnswersBeforeThePasswordCheck is the oracle.
//
// A wrong password is 401. A right password with the session store down is 500,
// because issueSession cannot write the session. With the limiter swallowing its
// storage error into a fail-open true, an unlimited stream of guesses could be
// run against a dead Redis and told which one was right — nobody could log in
// during that window, but the password was learned and Redis comes back.
//
// Refusing on the limiter's error removes the difference instead of hiding it:
// both answers are now 500, and no password is compared to produce them.
func TestBrokenLimiterAnswersBeforeThePasswordCheck(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	srv := loginServer(t, Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     failingSessions{security.NewMemoryStore()},
		Limiter:      brokenLimiter{},
		SessionTTL:   time.Hour,
	})

	wrong := postLogin(t, srv.URL, "admin", "not-the-password")
	right := postLogin(t, srv.URL, "admin", "p@ss")
	if wrong != http.StatusInternalServerError || right != http.StatusInternalServerError {
		t.Fatalf("wrong=%d right=%d: the two must be indistinguishable while the store is down",
			wrong, right)
	}
}

// A wrong user name and a wrong password must cost the same. With
// "!userOK || !VerifyPassword(...)" Go short-circuits and skips argon2 entirely
// for a wrong name: microseconds against a hundred milliseconds, which reads the
// administrator's login name off the clock. Constant-time comparison right next
// to it buys nothing while the operator above leaks the answer.
//
// The threshold is deliberately loose. This asserts that the expensive half runs
// in both cases, not that the two are indistinguishable to a patient attacker —
// argon2 timing varies, and a tight bound here would be a flaky test making a
// claim the code does not support.
func TestWrongUserStillPaysForTheHash(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	srv := loginServer(t, Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Limiter: allowAll{}, SessionTTL: time.Hour,
	})

	measure := func(user string) time.Duration {
		start := time.Now()
		if code := postLogin(t, srv.URL, user, "whatever"); code != http.StatusUnauthorized {
			t.Fatalf("login as %q → %d, want 401", user, code)
		}
		return time.Since(start)
	}
	// Warm the path so the first argon2 allocation is not counted as signal.
	measure("admin")

	wrongUser := measure("someone-else")
	rightUser := measure("admin")
	if wrongUser*4 < rightUser {
		t.Fatalf("wrong user %v vs right user %v: the name is readable off the clock",
			wrongUser, rightUser)
	}
}

// The submitted login must not reach the log: people type passwords into the
// user name box, and the log outlives the password.
func TestSubmittedLoginIsNotLogged(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingLogger{}
	srv := loginServer(t, Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Limiter: allowAll{},
		SessionTTL: time.Hour, Log: rec,
	})

	const secret = "hunter2-typed-in-the-wrong-box"
	postLogin(t, srv.URL, secret, "whatever")

	for _, line := range rec.lines() {
		if strings.Contains(line, secret) {
			t.Fatalf("the submitted login reached the log: %q", line)
		}
	}
	if len(rec.lines()) == 0 {
		t.Fatal("a failed login must still be recorded")
	}
}

// The fingerprint groups repeated attempts without carrying the value, and it is
// keyed per process so a dictionary run against a log finds nothing.
func TestLoginFingerprintGroupsWithoutRevealing(t *testing.T) {
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Limiter: allowAll{}, SessionTTL: time.Hour,
	}
	a, b := New(cfg), New(cfg)

	if a.loginFP("admin") != a.loginFP("admin") {
		t.Fatal("the same login must give the same fingerprint within a process")
	}
	if a.loginFP("admin") == a.loginFP("root") {
		t.Fatal("different logins must give different fingerprints")
	}
	if a.loginFP("admin") == b.loginFP("admin") {
		t.Fatal("the key must be per process, or the log becomes a dictionary target")
	}
}

type recordingLogger struct {
	mu sync.Mutex
	ls []string
}

func (l *recordingLogger) record(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	b.WriteString(msg)
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(strings.TrimSpace(strings.Join(strings.Fields(toString(a)), " ")))
	}
	l.ls = append(l.ls, b.String())
}

func (l *recordingLogger) Warn(msg string, args ...any) { l.record(msg, args...) }
func (l *recordingLogger) Info(msg string, args ...any) { l.record(msg, args...) }

func (l *recordingLogger) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ls...)
}

func toString(a any) string {
	if s, ok := a.(string); ok {
		return s
	}
	return ""
}
