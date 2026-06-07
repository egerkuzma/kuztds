package security

import (
	"context"
	"crypto/rand"
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

// Session — admin session data.
type Session struct {
	User    string
	CSRF    string
	Created time.Time
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
