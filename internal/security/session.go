package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// RandomToken returns a cryptographically random token (32 bytes, base64url).
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// EqualTokens compares tokens in constant time (protection against timing attacks).
func EqualTokens(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// PasswordFingerprint returns a short fingerprint of the password hash — the
// first 8 bytes of SHA-256, base64url. It is stored inside a session so that a
// password change invalidates every session issued under the previous password:
// the fingerprint travels with the session (including through Redis), needs no
// clocks, no files and no extra store methods.
//
// It is not a secret and reveals nothing about the password: the argon2id hash
// it is derived from already carries a random salt.
func PasswordFingerprint(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// Session — admin session data.
type Session struct {
	User    string
	CSRF    string
	Created time.Time
	// PwFP — fingerprint of the password hash the session was issued under
	// (see PasswordFingerprint). A session whose PwFP no longer matches the
	// current password is rejected.
	PwFP string
}

// SessionStore stores sessions by token. Implementations: MemoryStore (here) and
// store.RedisSessions (for production).
type SessionStore interface {
	Create(ctx context.Context, token string, s Session, ttl time.Duration) error
	Get(ctx context.Context, token string) (Session, bool, error)
	Delete(ctx context.Context, token string) error
}

// MemoryStore — thread-safe in-memory session store (for tests and
// single-instance deployments).
type MemoryStore struct {
	mu   sync.Mutex
	data map[string]sessionEntry
	now  func() time.Time
}

type sessionEntry struct {
	s   Session
	exp time.Time
}

// NewMemoryStore creates an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]sessionEntry), now: time.Now}
}

func (m *MemoryStore) Create(_ context.Context, token string, s Session, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[token] = sessionEntry{s: s, exp: m.now().Add(ttl)}
	return nil
}

func (m *MemoryStore) Get(_ context.Context, token string) (Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[token]
	if !ok {
		return Session{}, false, nil
	}
	if m.now().After(e.exp) {
		delete(m.data, token)
		return Session{}, false, nil
	}
	return e.s, true, nil
}

func (m *MemoryStore) Delete(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, token)
	return nil
}
