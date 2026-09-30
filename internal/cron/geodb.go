package cron

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/oschwald/maxminddb-golang"
)

const maxGeoBytes = 1 << 30

func (r *Runner) runGeoDB(ctx context.Context, cfg GeoDB) (string, error) {
	if len(cfg.Sources) == 0 {
		return "no sources configured", nil
	}
	var done []string
	var errs []error
	for _, src := range cfg.Sources {
		target := r.paths.CityDB
		env := "KUZTDS_GEO_DB"
		if src.Kind == "asn" {
			target, env = r.paths.ASNDB, "KUZTDS_ASN_DB"
		}
		if target == "" {
			errs = append(errs, fmt.Errorf("%s: %s is not set for the cron service, nowhere to put the database", src.Kind, env))
			continue
		}
		msg, err := r.updateGeo(ctx, src, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Kind, err))
			continue
		}
		done = append(done, src.Kind+": "+msg)
	}
	return strings.Join(done, "; "), errors.Join(errs...)
}

// updateGeo downloads one database next to its target, checks that it is a
// database of the expected kind, and only then renames it into place. The
// engine notices the new file on its reload interval.
func (r *Runner) updateGeo(ctx context.Context, src GeoSource, target string) (string, error) {
	// Ask only for what changed: the databases are tens of megabytes and come
	// out a few times a week at most. The condition is dropped when there is
	// nothing to keep — no file on disk, or a different URL than last time.
	var seen GeoSeen
	r.state(func(st *Status) { seen = st.GeoSeen[src.Kind] })
	var hdr map[string]string
	if _, err := os.Stat(target); err == nil && seen.URL == src.URL && seen.LastModified != "" {
		hdr = map[string]string{"If-Modified-Since": seen.LastModified}
	}
	body, respHdr, err := r.fetch(ctx, src.URL, hdr, src.User, src.Password, maxGeoBytes)
	if statusOf(err) == http.StatusNotModified {
		return "not modified since " + seen.LastModified, nil
	}
	if err != nil {
		return "", err
	}
	defer body.Close()
	remember := func() {
		r.state(func(st *Status) {
			if st.GeoSeen == nil {
				st.GeoSeen = map[string]GeoSeen{}
			}
			st.GeoSeen[src.Kind] = GeoSeen{URL: src.URL, LastModified: respHdr.Get("Last-Modified")}
		})
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".dl*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	err = unpackMMDB(body, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}

	db, err := maxminddb.Open(tmp.Name())
	if err != nil {
		return "", errors.New("the download is not a MaxMind database")
	}
	typ, built := db.Metadata.DatabaseType, int(db.Metadata.BuildEpoch)
	db.Close()
	if !kindMatches(src.Kind, typ) {
		return "", fmt.Errorf("expected a %s database, the download is %q", src.Kind, typ)
	}

	var have int
	r.state(func(st *Status) { have = st.GeoBuilt[src.Kind] })
	if _, err := os.Stat(target); err == nil && have == built {
		remember()
		return typ + " is up to date", nil
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	r.state(func(st *Status) {
		if st.GeoBuilt == nil {
			st.GeoBuilt = map[string]int{}
		}
		st.GeoBuilt[src.Kind] = built
	})
	remember()
	return typ + " updated", nil
}

func kindMatches(kind, dbType string) bool {
	t := strings.ToLower(dbType)
	if kind == "asn" {
		return strings.Contains(t, "asn") || strings.Contains(t, "isp")
	}
	return strings.Contains(t, "city") || strings.Contains(t, "country")
}

// unpackMMDB writes the database found in src to dst. src may be the .mmdb
// itself, a gzip of it, or a .tar.gz holding it (what MaxMind serves).
func unpackMMDB(src io.Reader, dst io.Writer) error {
	br := bufio.NewReader(src)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer gz.Close()
		inner := bufio.NewReaderSize(gz, 1024)
		if head, _ := inner.Peek(512); len(head) == 512 && string(head[257:262]) == "ustar" {
			tr := tar.NewReader(inner)
			for {
				h, err := tr.Next()
				if err == io.EOF {
					return errors.New("no .mmdb file inside the archive")
				}
				if err != nil {
					return err
				}
				if h.Typeflag == tar.TypeReg && strings.HasSuffix(h.Name, ".mmdb") {
					_, err = io.Copy(dst, io.LimitReader(tr, maxGeoBytes))
					return err
				}
			}
		}
		_, err = io.Copy(dst, io.LimitReader(inner, maxGeoBytes))
		return err
	}
	_, err := io.Copy(dst, br)
	return err
}
