package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// FileGroups — a GroupsStore backed by a JSON groups configuration file.
// Save writes atomically (via a temporary file + rename).
type FileGroups struct {
	path string
}

// NewFileGroups creates a groups store backed by a JSON file.
func NewFileGroups(path string) *FileGroups { return &FileGroups{path: path} }

func (f *FileGroups) List(context.Context) ([]config.Group, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []config.Group{}, nil
		}
		return nil, err
	}
	var groups []config.Group
	if err := json.Unmarshal(b, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (f *FileGroups) Save(_ context.Context, groups []config.Group) error {
	b, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// FileLists — a ListsStore backed by a directory of .dat files (IP lists, wap operators,
// signatures). Names are strictly validated — no path traversal allowed.
type FileLists struct {
	dir string
}

// NewFileLists creates a lists store in directory dir.
func NewFileLists(dir string) *FileLists { return &FileLists{dir: dir} }

var listNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.dat$`)

func (l *FileLists) safePath(name string) (string, error) {
	if !listNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("invalid list name")
	}
	return filepath.Join(l.dir, name), nil
}

func (l *FileLists) Names() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".dat") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (l *FileLists) Read(name string) (string, error) {
	p, err := l.safePath(name)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (l *FileLists) Write(name, content string) error {
	p, err := l.safePath(name)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// FileKeys reads collected keywords from keysDir/<group>/<date>[-se].dat.
type FileKeys struct{ dir string }

// NewFileKeys creates a keys reader in directory dir.
func NewFileKeys(dir string) *FileKeys { return &FileKeys{dir: dir} }

var safeSeg = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func (k *FileKeys) Read(group, date string, se bool) (string, error) {
	if !safeSeg.MatchString(group) || !safeSeg.MatchString(date) {
		return "", errors.New("invalid group/date")
	}
	name := date
	if se {
		name += "-se"
	}
	b, err := os.ReadFile(filepath.Join(k.dir, group, name+".dat"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
