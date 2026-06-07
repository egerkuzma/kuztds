package security

import (
	"context"
	"testing"
	"time"
)

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("s3cret-pass", h) {
		t.Error("a correct password should pass")
	}
	if VerifyPassword("wrong", h) {
		t.Error("an incorrect password should not pass")
	}
	if VerifyPassword("s3cret-pass", "garbage") {
		t.Error("a corrupted hash should not pass")
	}
	// two hashes of the same password differ (different salt)
	h2, _ := HashPassword("s3cret-pass")
	if h == h2 {
		t.Error("hashes should differ by salt")
	}
}

func TestRandomTokenUnique(t *testing.T) {
	a, _ := RandomToken()
	b, _ := RandomToken()
	if a == "" || a == b {
		t.Errorf("tokens should be non-empty and distinct: %q %q", a, b)
	}
	if !EqualTokens(a, a) || EqualTokens(a, b) {
		t.Error("EqualTokens works incorrectly")
	}
}

func TestMemoryStore(t *testing.T) {
	m := NewMemoryStore()
	cur := time.Unix(1000, 0)
	m.now = func() time.Time { return cur }
	ctx := context.Background()

	_ = m.Create(ctx, "tok", Session{User: "admin", CSRF: "c"}, time.Minute)
	s, ok, _ := m.Get(ctx, "tok")
	if !ok || s.User != "admin" {
		t.Fatal("the session should be found")
	}
	// TTL expiration
	cur = cur.Add(2 * time.Minute)
	if _, ok, _ := m.Get(ctx, "tok"); ok {
		t.Error("an expired session should not be found")
	}
	// deletion
	cur = time.Unix(1000, 0)
	_ = m.Create(ctx, "tok2", Session{User: "x"}, time.Minute)
	_ = m.Delete(ctx, "tok2")
	if _, ok, _ := m.Get(ctx, "tok2"); ok {
		t.Error("a deleted session should not be found")
	}
}
