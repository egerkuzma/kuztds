package store

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/redis/go-redis/v9"
)

func TestLoginAllow(t *testing.T) {
	c, mr := newCounters(t)
	ctx := context.Background()

	// max=2 per window: the 1st and 2nd are allowed, the 3rd is not.
	if !allowed(c.LoginAllow(ctx, "ip1", 2, time.Minute)) {
		t.Fatal("the 1st attempt must be allowed")
	}
	if !allowed(c.LoginAllow(ctx, "ip1", 2, time.Minute)) {
		t.Fatal("the 2nd attempt must be allowed")
	}
	if allowed(c.LoginAllow(ctx, "ip1", 2, time.Minute)) {
		t.Error("the 3rd attempt must be blocked")
	}
	// a different key is independent
	if !allowed(c.LoginAllow(ctx, "ip2", 2, time.Minute)) {
		t.Error("a different key must not be affected")
	}
	// after the window — allowed again
	mr.FastForward(2 * time.Minute)
	if !allowed(c.LoginAllow(ctx, "ip1", 2, time.Minute)) {
		t.Error("after the window attempts are allowed again")
	}
	// max<=0 → always allowed
	if !allowed(c.LoginAllow(ctx, "ip3", 0, time.Minute)) {
		t.Error("max=0 → no limit")
	}
}

func TestRotate(t *testing.T) {
	c, _ := newCounters(t)
	ctx := context.Background()
	// mod=3 → the index sequence cycles 0,1,2,0,1,2...
	got := make([]int, 7)
	for i := range got {
		got[i] = c.Rotate(ctx, "g1", "s1", 3)
	}
	for i, v := range got {
		if v < 0 || v >= 3 {
			t.Fatalf("Rotate[%d]=%d out of range [0,3)", i, v)
		}
	}
	// the indexes must change (not get stuck on one)
	if got[0] == got[1] && got[1] == got[2] {
		t.Errorf("Rotate is not rotating: %v", got)
	}
}

func TestRedisSessions(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ss := NewRedisSessions(rdb)
	ctx := context.Background()

	// a missing session → ok=false, without an error
	if _, ok, err := ss.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing session: ok=%v err=%v", ok, err)
	}

	sess := security.Session{User: "admin", CSRF: "tok", Created: time.Now()}
	if err := ss.Create(ctx, "t1", sess, time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, ok, err := ss.Get(ctx, "t1")
	if err != nil || !ok || got.User != "admin" || got.CSRF != "tok" {
		t.Fatalf("Get after create: got=%+v ok=%v err=%v", got, ok, err)
	}

	// deletion
	if err := ss.Delete(ctx, "t1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := ss.Get(ctx, "t1"); ok {
		t.Error("after Delete the session must not be found")
	}

	// TTL: disappears after expiry
	ss.Create(ctx, "t2", sess, time.Minute)
	mr.FastForward(2 * time.Minute)
	if _, ok, _ := ss.Get(ctx, "t2"); ok {
		t.Error("after the TTL the session must disappear")
	}
}
