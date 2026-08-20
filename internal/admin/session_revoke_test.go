package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/security"
)

// changePassword calls POST /api/password with the given csrf token.
func changePassword(t *testing.T, c *http.Client, base, csrf, old, new string) int {
	t.Helper()
	body := `{"old":"` + old + `","new":"` + new + `"}`
	req, err := http.NewRequest(http.MethodPost, base+"/api/password", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestPasswordChangeRevokesOtherSessions checks that changing the admin password
// terminates sessions issued before the change.
//
// Scenario: an admin session cookie leaked (a second browser, a stolen laptop,
// a shared machine). The admin reacts the way everyone expects to work — changes
// the password. The leaked session must die; otherwise the attacker keeps full
// admin access until SessionTTL expires (12h by default in cmd/admin).
//
// The test is deliberately black-box: it says nothing about *how* revocation
// happens (a store method, a timestamp cutoff in middleware, session versioning).
// It only asserts the observable contract — the old cookie stops working.
func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	srv := newTestServer(t)

	// The leaked session: someone else logged in with the credentials.
	leaked := newClient(t)
	if code, _ := login(t, leaked, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("leaked session login: expected 200, got %d", code)
	}

	// The real admin, in a separate browser.
	owner := newClient(t)
	code, csrf := login(t, owner, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("owner login: expected 200, got %d", code)
	}

	// Sanity check: before the change the leaked session works.
	resp, err := leaked.Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("before the password change the leaked session must work, got %d", resp.StatusCode)
	}

	if code := changePassword(t, owner, srv.URL, csrf, "p@ss", "n3w-p@ss"); code != http.StatusOK {
		t.Fatalf("password change: expected 200, got %d", code)
	}

	// The old password no longer works — the change did take effect.
	if code, _ := login(t, newClient(t), srv.URL, "admin", "p@ss"); code != http.StatusUnauthorized {
		t.Fatalf("login with the old password: expected 401, got %d", code)
	}

	// The point of the test: the leaked session must be dead too.
	r2, err := leaked.Get(srv.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Errorf("after the password change the leaked session must be 401, got %d "+
			"(the session survives the password change — the attacker keeps admin access until SessionTTL)",
			r2.StatusCode)
	}
}

// TestPasswordChangeSurvivesRestart checks that revocation outlives the process.
//
// In production sessions live in Redis and survive a restart of the admin
// binary, while anything the process remembered about the password change does
// not. So the test restarts the server — new Server, same session store, same
// password file — and demands that the leaked cookie stays dead.
func TestPasswordChangeSurvivesRestart(t *testing.T) {
	sessions := security.NewMemoryStore()
	pwFile := filepath.Join(t.TempDir(), "admin.hash")
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}

	newServer := func() *httptest.Server {
		s := New(Config{
			AdminUser:    "admin",
			PasswordHash: hash,
			PasswordFile: pwFile,
			Sessions:     sessions,
			Limiter:      allowAll{},
			SessionTTL:   time.Hour,
		})
		srv := httptest.NewServer(s.Handler())
		t.Cleanup(srv.Close)
		return srv
	}

	srv := newServer()
	leaked := newClient(t)
	if code, _ := login(t, leaked, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("leaked session login: expected 200, got %d", code)
	}

	owner := newClient(t)
	_, csrf := login(t, owner, srv.URL, "admin", "p@ss")
	if code := changePassword(t, owner, srv.URL, csrf, "p@ss", "n3w-p@ss"); code != http.StatusOK {
		t.Fatalf("password change: expected 200, got %d", code)
	}

	// Restart: a fresh process picks the hash up from the file, the session
	// store keeps every token that was issued before.
	restarted := newServer()
	resp, err := leaked.Get(restarted.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after a restart the leaked session must still be 401, got %d", resp.StatusCode)
	}
}
