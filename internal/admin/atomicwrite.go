package admin

import (
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path through a temporary file in the same
// directory, then renames it into place.
//
// os.WriteFile truncates first and writes second. Kill the process between the
// two and the file is empty; run out of disk and it is half a record. For the
// password hash both outcomes are worse than they look: an empty file reads as
// "no hash here" and silently falls back to whatever the environment says,
// which is the password from before the change.
//
// mode is a parameter rather than a constant because the two callers differ: a
// list is 0o644, a password hash is 0o600, and a helper that hardcodes the
// looser one turns a torn write into a secret the whole machine can read. The
// temporary file is created by os.CreateTemp, which opens it 0600 and picks a
// unique name — os.WriteFile into a fixed "<path>.tmp" would neither change the
// mode of a file left behind by an earlier crash nor keep two writers apart.
//
// The directory is taken from path on purpose. os.CreateTemp with an empty dir
// uses $TMPDIR, which in production is usually a different filesystem, and
// Rename across filesystems fails with EXDEV — a failure no test reproduces,
// because there the source and the target both land under the same tmpfs.
//
// This does not make the write durable: without fsync on the file and on the
// directory, losing power can still leave nothing behind. It makes the write
// atomic for a reader, which is what a killed process and a full disk need.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Removing after a successful rename fails with ENOENT and costs nothing.
	// Leaving it out costs a stray file holding a valid password hash, under a
	// name nobody looks at.
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
