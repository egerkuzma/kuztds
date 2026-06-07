package ipindex

import (
	"fmt"
	"math/rand"
	"net/netip"
	"strings"
	"testing"
)

func mustAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		panic(err)
	}
	return a
}

func TestLookup(t *testing.T) {
	const data = `
# google
66.249.64.0/19
8.8.8.8
# yandex
77.88.0.0-77.88.255.255
2a02:6b8::/32
`
	idx, err := Parse(strings.NewReader(data))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cases := []struct {
		ip        string
		wantOK    bool
		wantLabel string
	}{
		{"66.249.70.1", true, "google"}, // inside the CIDR
		{"8.8.8.8", true, "google"},     // single IP
		{"8.8.8.9", false, ""},          // nearby, but not in the list
		{"77.88.55.88", true, "yandex"}, // inside the range
		{"77.89.0.0", false, ""},        // beyond the range boundary
		{"2a02:6b8::1", true, "yandex"}, // IPv6 CIDR
		{"2a03::1", false, ""},          // a different IPv6
		{"1.1.1.1", false, ""},          // not in the list at all
	}
	for _, c := range cases {
		label, ok := idx.Lookup(mustAddr(c.ip))
		if ok != c.wantOK || label != c.wantLabel {
			t.Errorf("Lookup(%s) = (%q,%v); want (%q,%v)", c.ip, label, ok, c.wantLabel, c.wantOK)
		}
	}
}

func TestMergeAdjacent(t *testing.T) {
	// Two adjacent /25s should merge into a single range.
	const data = "10.0.0.0/25\n10.0.0.128/25\n"
	idx, err := Parse(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Len(); got != 1 {
		t.Fatalf("expected 1 merged range, got %d", got)
	}
	if _, ok := idx.Lookup(mustAddr("10.0.0.200")); !ok {
		t.Error("10.0.0.200 should fall within the merged range")
	}
}

// benchData generates n non-overlapping /24s, simulating a large list (ip_others.dat).
func benchData(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		// 1.x.y.0/24, iterating over the second and third octets
		fmt.Fprintf(&b, "%d.%d.%d.0/24\n", 1+(i>>16)%200, (i>>8)%256, i%256)
	}
	return b.String()
}

// BenchmarkLookup measures the hot path: a single lookup against a large index.
// The goal is a few microseconds versus an O(n) linear search.
func BenchmarkLookup(b *testing.B) {
	idx, err := Parse(strings.NewReader(benchData(184000)))
	if err != nil {
		b.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	ips := make([]netip.Addr, 1000)
	for i := range ips {
		ips[i] = netip.AddrFrom4([4]byte{
			byte(1 + rng.Intn(200)), byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256)),
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Lookup(ips[i%len(ips)])
	}
}
