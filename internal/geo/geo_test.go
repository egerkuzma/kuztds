package geo

import (
	"net/netip"
	"testing"
)

func TestNop(t *testing.T) {
	g := Nop{}.Resolve(netip.MustParseAddr("8.8.8.8"))
	if g.Country != Empty || g.City != Empty || g.Region != Empty {
		t.Errorf("Nop must return empty values, got %+v", g)
	}
}

func TestNorm(t *testing.T) {
	cases := map[string]string{
		"RU":     "ru",
		"  ":     Empty,
		"":       Empty,
		"Moscow": "moscow",
	}
	for in, want := range cases {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestFromRecord(t *testing.T) {
	var r record
	r.Country.ISOCode = "RU"
	r.City.Names = map[string]string{"en": "Moscow", "ru": "Москва"}
	r.Subdivisions = []struct {
		ISOCode string `maxminddb:"iso_code"`
	}{
		{ISOCode: "RU-MOW"},
	}
	g := fromRecord(r)
	if g.Country != "ru" || g.City != "moscow" || g.Region != "ru-mow" {
		t.Errorf("fromRecord = %+v; want {ru moscow ru-mow}", g)
	}

	// Empty record → all Empty.
	if g := fromRecord(record{}); g.Country != Empty || g.City != Empty || g.Region != Empty {
		t.Errorf("empty record must yield Empty, got %+v", g)
	}
}

func TestMMDB(t *testing.T) {
	m, err := OpenMMDB("testdata/GeoLite2-City-Test.mmdb")
	if err != nil {
		t.Skipf("test mmdb unavailable: %v", err)
	}
	defer m.Close()
	// Known IP from the MaxMind test database: 81.2.69.142 → London, GB.
	g := m.Resolve(netip.MustParseAddr("81.2.69.142"))
	if g.Country != "gb" {
		t.Errorf("country=%q; want gb", g.Country)
	}
	if g.City != "london" {
		t.Errorf("city=%q; want london", g.City)
	}
	// Invalid/unknown IP → empty.
	if g := m.Resolve(netip.MustParseAddr("203.0.113.1")); g.Country != Empty {
		t.Errorf("unknown IP → country=%q; want %q", g.Country, Empty)
	}
}

// MMDB and Nop must satisfy the Resolver interface.
var _ Resolver = (*MMDB)(nil)
var _ Resolver = Nop{}
