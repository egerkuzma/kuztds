package geo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// DB — a resolver backed by MaxMind-format databases: one City (or Country)
// file for location, and optionally one ASN file for the network. Either may
// be absent; what is missing simply stays empty in the answer.
//
// Files are read into memory and swapped atomically when they change on disk
// (Watch), so a fresh database dropped in by the updater goes live without a
// restart. Reading into memory rather than mapping the file is deliberate: a
// reader that is being replaced may still be in use by a request, and a byte
// slice the garbage collector owns cannot be unmapped from under it.
type DB struct {
	city source
	asn  source
	log  *slog.Logger
}

type source struct {
	path string
	r    atomic.Pointer[maxminddb.Reader]
	st   atomic.Pointer[fileStamp]
}

type fileStamp struct {
	mod  time.Time
	size int64
}

// Open builds a resolver from the given files. An empty path means "no such
// database". A configured file that cannot be read is an error only if it is
// the City database and no ASN database loaded either — otherwise the
// resolver starts with what it has and Watch picks the rest up when it
// appears (the updater may not have downloaded it yet).
func Open(cityPath, asnPath string, log *slog.Logger) (*DB, error) {
	if log == nil {
		log = slog.Default()
	}
	d := &DB{log: log}
	d.city.path, d.asn.path = cityPath, asnPath
	errCity := d.city.load()
	errASN := d.asn.load()
	if d.city.r.Load() == nil && d.asn.r.Load() == nil {
		if errCity != nil {
			return d, errCity
		}
		return d, errASN
	}
	return d, nil
}

// OpenMMDB opens a single City/Country database (kept for callers that only
// need location).
func OpenMMDB(path string) (*DB, error) { return Open(path, "", nil) }

// load reads the file if it is configured and has changed since the last
// successful load. The previous reader stays in place on any failure: a
// half-written or corrupt download must not blank the geo data.
func (s *source) load() error {
	if s.path == "" {
		return nil
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("geo: %w", err)
	}
	st := &fileStamp{mod: fi.ModTime(), size: fi.Size()}
	if cur := s.st.Load(); cur != nil && cur.mod.Equal(st.mod) && cur.size == st.size {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("geo: %w", err)
	}
	r, err := maxminddb.FromBytes(b)
	if err != nil {
		return fmt.Errorf("geo: open %s: %w", s.path, err)
	}
	s.r.Store(r)
	s.st.Store(st)
	return nil
}

// Reload re-reads the files that changed on disk and reports which did.
func (d *DB) Reload() (changed []string) {
	for _, s := range []*source{&d.city, &d.asn} {
		before := s.st.Load()
		if err := s.load(); err != nil {
			// A missing file is the normal state before the first download;
			// anything else is worth a line.
			if !errors.Is(err, os.ErrNotExist) {
				d.log.Warn("geo db not reloaded", "path", s.path, "err", err)
			}
			continue
		}
		if s.st.Load() != before {
			changed = append(changed, s.path)
		}
	}
	return changed
}

// Watch reloads changed databases every interval until ctx is done.
func (d *DB) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, p := range d.Reload() {
				d.log.Info("geo db reloaded", "path", p)
			}
		}
	}
}

// Resolve determines geo by IP. On an invalid IP or a miss, returns empty values.
func (d *DB) Resolve(ip netip.Addr) Geo {
	g := None()
	if d == nil || !ip.IsValid() {
		return g
	}
	nip := net.IP(ip.AsSlice())
	if r := d.city.r.Load(); r != nil {
		var rec record
		if err := r.Lookup(nip, &rec); err == nil {
			g = fromRecord(rec)
		}
	}
	if r := d.asn.r.Load(); r != nil {
		var rec asnRecord
		if err := r.Lookup(nip, &rec); err == nil {
			g.ASN, g.Org = rec.Number, keep(rec.Org)
		}
	}
	return g
}

// Info describes a loaded database, for the admin panel.
type Info struct {
	Path   string    `json:"path"`
	Type   string    `json:"type"`   // "GeoLite2-City", "GeoLite2-ASN", …
	Built  time.Time `json:"built"`  // build time from the database metadata
	Loaded bool      `json:"loaded"` // false: configured but not readable yet
}

// Databases lists the configured databases and what is loaded from them.
func (d *DB) Databases() []Info {
	if d == nil {
		return nil
	}
	var out []Info
	for _, s := range []*source{&d.city, &d.asn} {
		if s.path == "" {
			continue
		}
		in := Info{Path: s.path}
		if r := s.r.Load(); r != nil {
			in.Loaded = true
			in.Type = r.Metadata.DatabaseType
			in.Built = time.Unix(int64(r.Metadata.BuildEpoch), 0).UTC()
		}
		out = append(out, in)
	}
	return out
}

// Close is kept for symmetry with file-backed resources; the readers hold
// plain memory and need no release.
func (d *DB) Close() error { return nil }
