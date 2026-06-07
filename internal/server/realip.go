// Package server — the engine's HTTP layer: middleware and resolution of the client's real IP.
package server

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/egerkuzma/kuztds/internal/ipindex"
)

// RealIP determines the client's real IP, trusting the proxy headers
// (X-Forwarded-For / CF-Connecting-IP) ONLY if the immediate peer is in
// the trusted proxies list (trusted_proxies). This closes IP spoofing:
// headers from an untrusted peer are ignored.
type RealIP struct {
	trusted *ipindex.Index
}

// NewRealIP builds a resolver from a list of trusted CIDR/IP (same format as in .dat).
func NewRealIP(cidrs []string) (*RealIP, error) {
	idx, err := ipindex.Parse(strings.NewReader(strings.Join(cidrs, "\n")))
	if err != nil {
		return nil, err
	}
	return &RealIP{trusted: idx}, nil
}

func (r *RealIP) isTrusted(a netip.Addr) bool {
	_, ok := r.trusted.Lookup(a)
	return ok
}

// From computes the client IP from the peer address and request headers.
// If the peer is not trusted — headers are ignored (the peer address is returned).
func (r *RealIP) From(remoteAddr string, h http.Header) netip.Addr {
	peer := parseHostAddr(remoteAddr)
	if !peer.IsValid() || !r.isTrusted(peer) {
		return peer
	}
	// The peer is a trusted edge. CF-Connecting-IP is more reliable (Cloudflare overwrites it).
	if cf := strings.TrimSpace(h.Get("CF-Connecting-IP")); cf != "" {
		if a, err := netip.ParseAddr(cf); err == nil {
			return a.Unmap()
		}
	}
	// XFF: go right to left, the first UNtrusted address is the real client.
	parts := splitXFF(h.Values("X-Forwarded-For"))
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(parts[i])
		if err != nil {
			continue
		}
		a = a.Unmap()
		if !r.isTrusted(a) {
			return a
		}
	}
	return peer
}

// Middleware puts the computed client IP into the request context.
func (r *RealIP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ip := r.From(req.RemoteAddr, req.Header)
		ctx := context.WithValue(req.Context(), clientIPKey{}, ip)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

type clientIPKey struct{}

// ClientIP retrieves the client IP from the context (check validity via IsValid).
func ClientIP(ctx context.Context) netip.Addr {
	if a, ok := ctx.Value(clientIPKey{}).(netip.Addr); ok {
		return a
	}
	return netip.Addr{}
}

func parseHostAddr(remoteAddr string) netip.Addr {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// splitXFF splits X-Forwarded-For values (possibly several headers,
// each a comma-separated list) into a flat slice of strings.
func splitXFF(values []string) []string {
	var out []string
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
