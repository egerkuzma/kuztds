package geo

import (
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNop(t *testing.T) {
	g := Nop{}.Resolve(netip.MustParseAddr("8.8.8.8"))
	if g != None() {
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
	r.Subdivisions = append(r.Subdivisions, r.Subdivisions...)[:0]
	r.Subdivisions = append(r.Subdivisions, struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	}{ISOCode: "RU-MOW"})
	g := fromRecord(r)
	if g.Country != "ru" || g.City != "moscow" || g.Region != "ru-mow" {
		t.Errorf("fromRecord = %+v; want {ru moscow ru-mow}", g)
	}

	// No ISO code for the subdivision: its name stands in.
	r.Subdivisions[0].ISOCode, r.Subdivisions[0].Names = "", map[string]string{"en": "Moscow Oblast"}
	if g := fromRecord(r); g.Region != "moscow oblast" {
		t.Errorf("region without an ISO code = %q; want the name", g.Region)
	}

	// Empty record → all Empty.
	if g := fromRecord(record{}); g != None() {
		t.Errorf("empty record must yield Empty, got %+v", g)
	}
}

func TestMMDB(t *testing.T) {
	m, err := OpenMMDB("testdata/GeoLite2-City-Test.mmdb")
	if err != nil {
		t.Fatalf("test mmdb: %v", err)
	}
	// Known IP from the MaxMind test database: 81.2.69.142 → London, GB.
	g := m.Resolve(netip.MustParseAddr("81.2.69.142"))
	if g.Country != "gb" || g.City != "london" || g.Region != "eng" {
		t.Errorf("geo = %+v; want gb / london / eng", g)
	}
	if g.Timezone != "Europe/London" {
		t.Errorf("timezone = %q; want Europe/London", g.Timezone)
	}
	// No ASN database configured: the network stays unknown.
	if g.ASN != 0 || g.Org != Empty {
		t.Errorf("without an ASN db: asn=%d org=%q", g.ASN, g.Org)
	}
	// Unknown IP → empty.
	if g := m.Resolve(netip.MustParseAddr("203.0.113.1")); g != None() {
		t.Errorf("unknown IP → %+v; want all empty", g)
	}
	// Invalid IP → empty, no panic.
	if g := m.Resolve(netip.Addr{}); g != None() {
		t.Errorf("invalid IP → %+v", g)
	}
}

// City + ASN together: one answer carries both location and network.
func TestCityAndASN(t *testing.T) {
	d, err := Open("testdata/GeoLite2-City-Test.mmdb", "testdata/GeoLite2-ASN-Test.mmdb", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1.128.0.0/11 is Telstra (AS1221) in the ASN test database.
	g := d.Resolve(netip.MustParseAddr("1.128.0.1"))
	if g.ASN != 1221 || g.Org != "Telstra Pty Ltd" {
		t.Errorf("asn = %d %q; want 1221 Telstra Pty Ltd", g.ASN, g.Org)
	}
	// An address only the City database knows: location without a network.
	g = d.Resolve(netip.MustParseAddr("81.2.69.142"))
	if g.Country != "gb" || g.ASN != 0 {
		t.Errorf("81.2.69.142 = %+v", g)
	}
	info := d.Databases()
	if len(info) != 2 || !info[0].Loaded || info[0].Type != "GeoLite2-City" || info[1].Type != "GeoLite2-ASN" {
		t.Errorf("Databases() = %+v", info)
	}
}

// A Country-only database answers the country and leaves the rest empty.
func TestCountryDatabase(t *testing.T) {
	d, err := Open("testdata/GeoLite2-Country-Test.mmdb", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Resolve(netip.MustParseAddr("81.2.69.142"))
	if g.Country != "gb" || g.City != Empty || g.Timezone != Empty {
		t.Errorf("country db → %+v", g)
	}
}

// ASN alone is a valid setup: country then comes from the CDN header.
func TestASNOnly(t *testing.T) {
	d, err := Open("", "testdata/GeoLite2-ASN-Test.mmdb", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Resolve(netip.MustParseAddr("1.0.0.1"))
	if g.ASN != 15169 || g.Country != Empty {
		t.Errorf("asn-only → %+v", g)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.mmdb"), "", nil); err == nil {
		t.Error("a missing City database with nothing else must be an error")
	}
	bad := filepath.Join(t.TempDir(), "bad.mmdb")
	if err := os.WriteFile(bad, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bad, "", nil); err == nil {
		t.Error("a corrupt database must be an error")
	}
	// City missing but ASN present: start with what there is.
	d, err := Open(filepath.Join(t.TempDir(), "later.mmdb"), "testdata/GeoLite2-ASN-Test.mmdb", nil)
	if err != nil {
		t.Fatalf("ASN present, City not yet downloaded: %v", err)
	}
	if g := d.Resolve(netip.MustParseAddr("1.0.0.1")); g.ASN != 15169 {
		t.Errorf("got %+v", g)
	}
}

// The updater replaces the file on disk; the resolver must pick the new one up
// and must keep the old one when the replacement is garbage.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "geo.mmdb")
	stamp := time.Now()
	cp := func(src string) {
		t.Helper()
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		// Distinct mtimes regardless of the filesystem's timestamp resolution.
		stamp = stamp.Add(2 * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	london := netip.MustParseAddr("81.2.69.142")

	cp("testdata/GeoLite2-Country-Test.mmdb")
	d, err := Open(path, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if g := d.Resolve(london); g.City != Empty {
		t.Fatalf("country db must not know the city: %+v", g)
	}
	if ch := d.Reload(); len(ch) != 0 {
		t.Errorf("nothing changed, Reload reported %v", ch)
	}

	cp("testdata/GeoLite2-City-Test.mmdb")
	if ch := d.Reload(); len(ch) != 1 {
		t.Fatalf("Reload = %v; want the changed path", ch)
	}
	if g := d.Resolve(london); g.City != "london" {
		t.Errorf("after reload: %+v", g)
	}

	// A broken download: the previous database stays in service.
	if err := os.WriteFile(path, []byte("truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ch := d.Reload(); len(ch) != 0 {
		t.Errorf("a corrupt file must not count as a reload: %v", ch)
	}
	if g := d.Resolve(london); g.City != "london" {
		t.Errorf("corrupt replacement wiped the data: %+v", g)
	}
}

func TestUTCOffset(t *testing.T) {
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		tz   string
		at   time.Time
		want string
	}{
		{"Europe/Moscow", winter, "+3"},
		{"Asia/Kolkata", winter, "+5:30"},
		{"America/Los_Angeles", winter, "-8"},
		{"America/Los_Angeles", summer, "-7"}, // daylight saving
		{"Europe/London", winter, "+0"},
		{"Asia/Kathmandu", winter, "+5:45"},
		{"America/St_Johns", winter, "-3:30"},
		{"Not/AZone", winter, Empty},
		{Empty, winter, Empty},
		{"", winter, Empty},
	}
	for _, c := range cases {
		if got := (Geo{Timezone: c.tz}).UTCOffset(c.at); got != c.want {
			t.Errorf("%s at %s: offset %q; want %q", c.tz, c.at.Format("Jan"), got, c.want)
		}
	}
}

func TestNormOffset(t *testing.T) {
	ok := map[string]string{
		"3": "+3", "+3": "+3", "+03": "+3", "+03:00": "+3", "UTC+3": "+3", "gmt+3": "+3",
		"-8": "-8", "−8": "-8", "5:30": "+5:30", "+0530": "+5:30", "-3:30": "-3:30",
		"0": "+0", "UTC": "+0", "Z": "+0", "+14": "+14", " +2 ": "+2",
	}
	for in, want := range ok {
		if got, good := NormOffset(in); !good || got != want {
			t.Errorf("NormOffset(%q) = %q, %v; want %q", in, got, good, want)
		}
	}
	for _, in := range []string{"Europe/Moscow", "abc", "+15", "3:75", "+", "1:2:3"} {
		if got, good := NormOffset(in); good {
			t.Errorf("NormOffset(%q) = %q; want not an offset", in, got)
		}
	}
}

// DB and Nop must satisfy the Resolver interface.
var _ Resolver = (*DB)(nil)
var _ Resolver = Nop{}
