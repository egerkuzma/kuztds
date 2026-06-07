package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "TestUA" {
			t.Errorf("UA not sent, got %q", r.Header.Get("User-Agent"))
		}
		w.Write([]byte("hello body"))
	}))
	defer srv.Close()

	c := New("TestUA")
	got, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "hello body" {
		t.Errorf("body = %q", got)
	}
}

func TestGetNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("nope"))
	}))
	defer srv.Close()

	c := New("")
	body, err := c.Get(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if body != "nope" {
		t.Errorf("body still returned on error: %q", body)
	}
	if err.Error() == "" {
		t.Error("httpError.Error() empty")
	}
}

func TestGetBadURL(t *testing.T) {
	c := New("")
	if _, err := c.Get(context.Background(), "://bad url"); err == nil {
		t.Error("expected error on malformed URL")
	}
}

func TestGetContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("late"))
	}))
	defer srv.Close()

	c := New("")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Get(ctx, srv.URL); err == nil {
		t.Error("expected timeout error")
	}
}

func TestGetCachedHitsCache(t *testing.T) {
	c := New("")
	var calls atomic.Int32
	load := func() (string, error) {
		calls.Add(1)
		return "value", nil
	}
	for i := 0; i < 3; i++ {
		v, err := c.GetCached("k", time.Minute, load)
		if err != nil || v != "value" {
			t.Fatalf("GetCached: v=%q err=%v", v, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("load called %d times, want 1 (cached)", calls.Load())
	}
}

func TestGetCachedTTLZeroNeverCaches(t *testing.T) {
	c := New("")
	var calls atomic.Int32
	load := func() (string, error) { calls.Add(1); return "v", nil }
	c.GetCached("k", 0, load)
	c.GetCached("k", 0, load)
	if calls.Load() != 2 {
		t.Errorf("ttl=0 must not cache, load called %d times", calls.Load())
	}
}

func TestGetCachedExpiry(t *testing.T) {
	c := New("")
	base := time.Unix(1000, 0)
	cur := base
	c.now = func() time.Time { return cur }

	var calls atomic.Int32
	load := func() (string, error) { calls.Add(1); return "v", nil }

	c.GetCached("k", time.Minute, load) // calls=1, exp=base+1m
	cur = base.Add(30 * time.Second)
	c.GetCached("k", time.Minute, load) // still valid → cache
	if calls.Load() != 1 {
		t.Fatalf("within TTL should not reload, calls=%d", calls.Load())
	}
	cur = base.Add(2 * time.Minute) // expired
	c.GetCached("k", time.Minute, load)
	if calls.Load() != 2 {
		t.Errorf("after TTL should reload, calls=%d", calls.Load())
	}
}

func TestGetCachedErrorNotCached(t *testing.T) {
	c := New("")
	var calls atomic.Int32
	load := func() (string, error) {
		calls.Add(1)
		return "", context.DeadlineExceeded
	}
	if _, err := c.GetCached("k", time.Minute, load); err == nil {
		t.Fatal("expected error propagated")
	}
	// a repeat call loads again (the error is not cached)
	c.GetCached("k", time.Minute, load)
	if calls.Load() != 2 {
		t.Errorf("error must not be cached, calls=%d", calls.Load())
	}
}

func TestNewDefaultUA(t *testing.T) {
	c := New("")
	if c.ua != "Mozilla/5.0" {
		t.Errorf("default UA = %q", c.ua)
	}
}
