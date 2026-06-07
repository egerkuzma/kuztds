package server

import (
	"net/http"
	"testing"
)

func newRealIP(t *testing.T) *RealIP {
	t.Helper()
	r, err := NewRealIP([]string{"10.0.0.0/8", "173.245.48.0/20"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRealIP_UntrustedPeerIgnoresHeaders(t *testing.T) {
	r := newRealIP(t)
	// The peer is NOT trusted — XFF must be ignored (anti-spoofing).
	h := http.Header{"X-Forwarded-For": {"1.2.3.4"}}
	got := r.From("8.8.8.8:5555", h)
	if got.String() != "8.8.8.8" {
		t.Errorf("expected peer address 8.8.8.8, got %s", got)
	}
}

func TestRealIP_TrustedPeerUsesXFF(t *testing.T) {
	r := newRealIP(t)
	// The peer is trusted (10.x). The real client is the rightmost untrusted one in XFF.
	h := http.Header{"X-Forwarded-For": {"203.0.113.7, 10.0.0.5"}}
	got := r.From("10.0.0.5:443", h)
	if got.String() != "203.0.113.7" {
		t.Errorf("expected client 203.0.113.7, got %s", got)
	}
}

func TestRealIP_CloudflareHeader(t *testing.T) {
	r := newRealIP(t)
	// The peer is Cloudflare (trusted). CF-Connecting-IP takes priority over XFF.
	h := http.Header{
		"Cf-Connecting-Ip": {"198.51.100.22"},
		"X-Forwarded-For":  {"1.2.3.4"},
	}
	got := r.From("173.245.48.10:443", h)
	if got.String() != "198.51.100.22" {
		t.Errorf("expected CF-Connecting-IP 198.51.100.22, got %s", got)
	}
}

func TestRealIP_SpoofedXFFFromUntrusted(t *testing.T) {
	r := newRealIP(t)
	// The client itself sends CF-Connecting-IP, but the peer is untrusted — don't trust it.
	h := http.Header{"Cf-Connecting-Ip": {"10.0.0.1"}}
	got := r.From("8.8.8.8:1", h)
	if got.String() != "8.8.8.8" {
		t.Errorf("spoofing must not pass; got %s", got)
	}
}
