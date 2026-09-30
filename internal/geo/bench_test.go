package geo

import (
	"net/netip"
	"testing"
)

// BenchmarkResolve — one visitor lookup against the City and ASN databases,
// the cost geo adds to every request on the hot path.
func BenchmarkResolve(b *testing.B) {
	d, err := Open("testdata/GeoLite2-City-Test.mmdb", "testdata/GeoLite2-ASN-Test.mmdb", nil)
	if err != nil {
		b.Fatal(err)
	}
	ips := []netip.Addr{
		netip.MustParseAddr("81.2.69.142"), // City: London
		netip.MustParseAddr("1.128.0.1"),   // ASN: Telstra
		netip.MustParseAddr("203.0.113.1"), // in neither
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.Resolve(ips[i%len(ips)])
	}
}
