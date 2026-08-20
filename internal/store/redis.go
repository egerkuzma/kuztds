// Package store provides storage clients: Redis (counters) and ClickHouse (logs).
package store

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/redis/go-redis/v9"
)

// Counters implements uniqueness, anti-flood, and stream limit counters on top of Redis.
type Counters struct {
	rdb redis.UniversalClient
	now func() time.Time
}

// NewCounters creates counters on top of a Redis connection.
func NewCounters(rdb redis.UniversalClient) *Counters {
	return &Counters{rdb: rdb, now: time.Now}
}

// OpenRedis opens a connection to Redis at the given address.
func OpenRedis(addr, password string, db int) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Unique returns true if the pair (id, ip) is seen for the first time within ttl.
// Implemented atomically via SET NX.
func (c *Counters) Unique(ctx context.Context, id string, ip netip.Addr, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	key := "uniq:" + id + ":" + ip.String()
	ok, err := c.rdb.SetNX(ctx, key, 1, ttl).Result()
	if err != nil {
		return true, err // fail-open: on failure treat as unique
	}
	return ok, nil
}

// Firewall allows no more than max requests from an IP within the window.
// Fixed window, without database locks.
//
// A non-positive window falls back to defaultWindow rather than to "no TTL":
// an enabled firewall whose seconds field was left blank must not turn into a
// permanent ban after the first max requests (see incrWithTTL).
func (c *Counters) Firewall(ctx context.Context, id string, ip netip.Addr, max int, window time.Duration) (allowed bool, err error) {
	if max <= 0 {
		return true, nil
	}
	if window <= 0 {
		window = defaultWindow
	}
	key := "fw:" + id + ":" + ip.String()
	n, err := c.incrWithTTL(ctx, key, window)
	if err != nil {
		return true, err // fail-open
	}
	return n <= int64(max), nil
}

// defaultWindow — the fallback window for a counter configured without one.
const defaultWindow = time.Minute

// incrWithTTL increments key and guarantees it carries a TTL.
//
// A counter key with no expiry never resets, so the rate limit it backs stops
// being a limit and becomes a permanent block that survives until someone
// flushes Redis by hand. If EXPIRE fails, the key is dropped instead of being
// left immortal — the next request simply starts a fresh window, which matches
// the fail-open policy of the rest of this package.
func (c *Counters) incrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		if err := c.rdb.Expire(ctx, key, ttl).Err(); err != nil {
			_ = c.rdb.Del(ctx, key).Err()
			return 0, err
		}
	}
	return n, nil
}

// LimitAllowed reports whether the stream's display limit has not been exhausted (read-only).
// The counter is incremented in RecordServe upon the actual serve.
func (c *Counters) LimitAllowed(ctx context.Context, id, stream string, rule config.LimitRule) (bool, error) {
	if !rule.Enabled || rule.Count <= 0 {
		return true, nil
	}
	key, _ := c.limitKey(id, stream, rule)
	v, err := c.rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return true, nil // counter does not exist yet
	}
	if err != nil {
		return true, err // fail-open
	}
	return v < rule.Count, nil
}

// RecordServe increments the stream's display counter (called after the serve).
func (c *Counters) RecordServe(ctx context.Context, id, stream string, rule config.LimitRule) error {
	if !rule.Enabled || rule.Count <= 0 {
		return nil
	}
	key, ttl := c.limitKey(id, stream, rule)
	_, err := c.incrWithTTL(ctx, key, ttl)
	return err
}

// Rotate returns an index in [0,mod) for even distribution (evenly):
// an atomic INCR on the group:stream key, modulo the number of variants.
func (c *Counters) Rotate(ctx context.Context, id, stream string, mod int) int {
	if mod <= 1 {
		return 0
	}
	n, err := c.rdb.Incr(ctx, "rot:"+id+":"+stream).Result()
	if err != nil {
		return 0
	}
	return int((n - 1) % int64(mod))
}

// limitKey builds the key and TTL for a limit:
//
//	Type 1 — daily: date in the key, TTL with a margin (48h).
//	Type 2 — per period: fixed window of Seconds length.
//
// A type-2 rule saved without a period gets a day rather than no TTL at all:
// its key is fixed (no date in it), so an immortal counter would disable the
// stream forever instead of for one period.
func (c *Counters) limitKey(id, stream string, rule config.LimitRule) (string, time.Duration) {
	if rule.Type == 2 {
		ttl := time.Duration(rule.Seconds) * time.Second
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		return "lim:p:" + id + ":" + stream, ttl
	}
	day := c.now().UTC().Format("20060102")
	return "lim:d:" + id + ":" + stream + ":" + day, 48 * time.Hour
}
