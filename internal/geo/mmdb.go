package geo

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/oschwald/maxminddb-golang"
)

// MMDB — a resolver backed by the MaxMind GeoLite2-City database (.mmdb).
type MMDB struct {
	r *maxminddb.Reader
}

// OpenMMDB opens the geo database at the given .mmdb path.
func OpenMMDB(path string) (*MMDB, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geo: open %s: %w", path, err)
	}
	return &MMDB{r: r}, nil
}

// Resolve determines geo by IP. On an invalid IP or a miss, returns empty values.
func (m *MMDB) Resolve(ip netip.Addr) Geo {
	empty := Geo{Country: Empty, City: Empty, Region: Empty}
	if m == nil || m.r == nil || !ip.IsValid() {
		return empty
	}
	var rec record
	if err := m.r.Lookup(net.IP(ip.AsSlice()), &rec); err != nil {
		return empty
	}
	return fromRecord(rec)
}

// Close closes the database.
func (m *MMDB) Close() error {
	if m == nil || m.r == nil {
		return nil
	}
	return m.r.Close()
}
