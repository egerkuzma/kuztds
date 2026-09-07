package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// expiryCommands is every way a client can stamp a TTL from a second round
// trip. The set matters more than it looks: a hook that only drops "expire"
// proves the limiter no longer sends that one command, and stays green if
// someone reimplements it as INCR plus PExpire — the same bug under a
// different name. What has to be caught is the class, not the string we
// happened to burn on.
var expiryCommands = map[string]bool{
	"expire": true, "pexpire": true, "expireat": true, "pexpireat": true,
}

// dropExpiry fails any client-issued expiry command, the way a connection reset
// between two round trips does. The limiter must not have a step there to lose.
type dropExpiry struct{}

func (dropExpiry) DialHook(next redis.DialHook) redis.DialHook { return next }
func (dropExpiry) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (dropExpiry) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if expiryCommands[cmd.Name()] {
			return errors.New("connection reset by peer")
		}
		return next(ctx, cmd)
	}
}

func newLoginCounters(t *testing.T, hooks ...redis.Hook) (*Counters, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	for _, h := range hooks {
		rdb.AddHook(h)
	}
	return NewCounters(rdb), mr
}

// TestLoginAllowExpiryCannotBeLost is the regression this file exists for.
//
// LoginAllow used to INCR and then EXPIRE as two client calls. Lose the second
// one — a reset, a timeout, a process that dies in between — and "login:<key>"
// stays without an expiry forever. The limiter then blocks the administrator
// permanently, on the one door that has no way back in through: there is no
// admin UI to clear a key that locks you out of the admin UI.
//
// The counter and its expiry now go out in a single script, so there is no
// window to lose. Dropping every client-side expiry command proves it: the key
// still gets a TTL and the limiter still recovers.
func TestLoginAllowExpiryCannotBeLost(t *testing.T) {
	c, mr := newLoginCounters(t, dropExpiry{})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if !c.LoginAllow(ctx, "1.2.3.4", 3, time.Minute) {
			t.Fatalf("attempt %d must be allowed", i+1)
		}
	}
	if c.LoginAllow(ctx, "1.2.3.4", 3, time.Minute) {
		t.Fatal("4th attempt within the window must be blocked")
	}
	if ttl := mr.TTL("login:1.2.3.4"); ttl <= 0 {
		t.Fatalf("ttl = %v: the key has no expiry and the lockout is permanent", ttl)
	}

	mr.FastForward(2 * time.Minute)
	if !c.LoginAllow(ctx, "1.2.3.4", 3, time.Minute) {
		t.Fatal("after the window the limiter must let the administrator back in")
	}
}

// TestLoginAllowBlankWindowIsNotForever covers the branch that used to skip
// EXPIRE altogether. No caller passes a non-positive window today, so this is
// about the next one: a limiter configured without a window must fall back to
// a default, not turn into a permanent ban after max attempts.
func TestLoginAllowBlankWindowIsNotForever(t *testing.T) {
	c, mr := newLoginCounters(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		c.LoginAllow(ctx, "5.6.7.8", 2, 0)
	}
	if ttl := mr.TTL("login:5.6.7.8"); ttl <= 0 {
		t.Fatalf("ttl = %v, want the default window", ttl)
	}
	if c.LoginAllow(ctx, "5.6.7.8", 2, 0) {
		t.Fatal("3rd attempt must be blocked")
	}
	mr.FastForward(defaultWindow + time.Second)
	if !c.LoginAllow(ctx, "5.6.7.8", 2, 0) {
		t.Fatal("blank window must expire, not ban forever")
	}
}

// TestLoginAllowFailsOpen keeps the package's rule: a broken Redis must not
// lock people out.
func TestLoginAllowFailsOpen(t *testing.T) {
	c, mr := newLoginCounters(t)
	mr.Close()
	if !c.LoginAllow(context.Background(), "9.9.9.9", 1, time.Minute) {
		t.Fatal("a dead Redis must fail open")
	}
}
