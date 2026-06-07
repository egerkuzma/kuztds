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
// Returns true if the attempt is allowed.
func (c *Counters) LoginAllow(ctx context.Context, key string, max int, window time.Duration) bool {
	if max <= 0 {
		return true
	}
	k := "login:" + key
	n, err := c.rdb.Incr(ctx, k).Result()
	if err != nil {
		return true // fail-open
	}
	if n == 1 && window > 0 {
		c.rdb.Expire(ctx, k, window)
	}
	return n <= int64(max)
}
