// Package store provides storage clients: Redis (counters) and ClickHouse (logs).
package store

import (
	"context"
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

// incrTTLScript increments a key and stamps the TTL on the first increment,
// in one round trip.
//
// The TTL has to be set inside the same atomic step as the INCR. Done as two
// client calls, a failure between them leaves a counter with no expiry, and a
// rate limit backed by a key that never resets is not a limit but a permanent
// block. The previous Go version compensated by deleting the key when EXPIRE
// failed — throwing away a live counter to avoid an immortal one. With the two
// operations in a single script there is nothing to compensate for.
var incrTTLScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return n
`)

// takeLimitScript takes one unit of the stream's serve limit: it increments the
// counter only if it is still below the limit. Returns the new value, or -1 if
// the limit is exhausted (in which case nothing was incremented).
//
// This replaces the LimitAllowed/RecordServe pair. Those read the counter and
// incremented it in two separate round trips, so N concurrent requests could all
// read the same under-the-limit value and all be served — the limit overshot by
// as much as the concurrency. Checking and taking in one script removes the
// window entirely.
//
// The counter must not move when the limit is exhausted. router.matches applies
// the limit as its last filter and Select returns the first matching stream, so
// an increment here means this stream is the one being served. Incrementing on a
// refusal instead would let an exhausted stream that sits early in the list be
// bumped by every request for the rest of the window, and the daily report would
// show many times more serves than actually happened.
var takeLimitScript = redis.NewScript(`
local lim = tonumber(ARGV[2])
local n = tonumber(redis.call('GET', KEYS[1]) or '0')
if n >= lim then return -1 end
n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return n
`)

// incrWithTTL increments key and guarantees it carries a TTL.
func (c *Counters) incrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrTTLScript.Run(ctx, c.rdb, []string{key}, ttl.Milliseconds()).Int64()
}

// TakeLimit reports whether the stream's serve limit still had room, consuming
// one unit if it did. Atomic: the check and the increment are a single script.
//
// Called from the router as the last filter, i.e. exactly once per request per
// stream, and only for the stream that is about to be served.
//
// On a Redis failure it returns true — fail-open, like the rest of this package:
// a broken counter must not stop traffic.
func (c *Counters) TakeLimit(ctx context.Context, id, stream string, rule config.LimitRule) (bool, error) {
	if !rule.Enabled || rule.Count <= 0 {
		return true, nil
	}
	key, ttl := c.limitKey(id, stream, rule)
	n, err := takeLimitScript.Run(ctx, c.rdb, []string{key}, ttl.Milliseconds(), rule.Count).Int64()
	if err != nil {
		return true, err // fail-open
	}
	return n >= 0, nil
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
