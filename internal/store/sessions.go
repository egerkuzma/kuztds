package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/redis/go-redis/v9"
)

// RedisSessions is a Redis-backed admin session store (implements
// security.SessionStore). Sessions live with a TTL and survive process restarts.
type RedisSessions struct {
	rdb redis.UniversalClient
}

// NewRedisSessions creates a store on top of Redis.
func NewRedisSessions(rdb redis.UniversalClient) *RedisSessions {
	return &RedisSessions{rdb: rdb}
}

func (s *RedisSessions) key(token string) string { return "sess:" + token }

func (s *RedisSessions) Create(ctx context.Context, token string, sess security.Session, ttl time.Duration) error {
	b, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.key(token), b, ttl).Err()
}

func (s *RedisSessions) Get(ctx context.Context, token string) (security.Session, bool, error) {
	b, err := s.rdb.Get(ctx, s.key(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return security.Session{}, false, nil
	}
	if err != nil {
		return security.Session{}, false, err
	}
	var sess security.Session
	if err := json.Unmarshal(b, &sess); err != nil {
		return security.Session{}, false, err
	}
	return sess, true, nil
}

func (s *RedisSessions) Delete(ctx context.Context, token string) error {
	return s.rdb.Del(ctx, s.key(token)).Err()
}

// LoginAllow limits login attempts: no more than max per window per key.
// Returns whether the attempt is allowed, and any storage failure.
//
// The counter and its expiry are set in one atomic step, through the same
// script the firewall uses. Done as INCR followed by a separate EXPIRE, a
// failure between the two round trips leaves "login:<key>" with no expiry at
// all — and a login limiter backed by a key that never resets is not a limiter
// but a permanent lockout, on the one door there is no way back in through.
//
// A non-positive window falls back to defaultWindow for the same reason: the
// branch that skipped EXPIRE entirely produced exactly that immortal key.
//
// Unlike the rest of this package, the error is returned rather than swallowed
// into a fail-open true. The counters guarding traffic must not stop traffic
// when Redis is unwell; the login counter answers a different question, and its
// caller needs to know that the storage is unreachable before it decides what
// to say. See the handler for what it does with it.
func (c *Counters) LoginAllow(ctx context.Context, key string, max int, window time.Duration) (bool, error) {
	if max <= 0 {
		return true, nil
	}
	if window <= 0 {
		window = defaultWindow
	}
	n, err := c.incrWithTTL(ctx, "login:"+key, window)
	if err != nil {
		return true, err
	}
	return n <= int64(max), nil
}
