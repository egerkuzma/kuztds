package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egerkuzma/kuztds/internal/atomicfile"
	"github.com/egerkuzma/kuztds/internal/ipindex"
)

const maxListBytes = 32 << 20

func (r *Runner) runIPLists(ctx context.Context, cfg IPLists) (string, error) {
	if len(cfg.Sources) == 0 {
		return "no sources configured", nil
	}
	var done []string
	var errs []error
	for _, src := range cfg.Sources {
		res, err := r.updateList(ctx, src)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		done = append(done, res...)
	}
	sort.Strings(done)
	return strings.Join(done, ", "), errors.Join(errs...)
}

// updateList fetches one source and writes the list(s) it describes.
func (r *Runner) updateList(ctx context.Context, src IPSource) ([]string, error) {
	body, err := r.get(ctx, src.URL, nil, "", "", maxListBytes)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return nil, err
	}

	lists := map[string][]string{}
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '{' {
		if src.Target == "" {
			return nil, fmt.Errorf("%s: a JSON source needs a target list", hostOf(src.URL))
		}
		lines, err := prefixesFromJSON(t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", hostOf(src.URL), err)
		}
		lists[src.Target] = lines
	} else if src.Target != "" {
		lists[src.Target] = cleanLines(string(raw))
	} else {
		lists = splitSections(string(raw))
	}

	var out []string
	for name, lines := range lists {
		n := countEntries(lines)
		// An upstream that answers 200 with an error page or an empty file
		// must not wipe a working list.
		if n == 0 {
			return out, fmt.Errorf("%s: no addresses for %s, list left as it was", hostOf(src.URL), name)
		}
		path := filepath.Join(r.paths.DataDir, name+".dat")
		if src.Mode == "merge" {
			lines = mergeLines(path, lines)
			n = countEntries(lines)
		}
		if err := atomicfile.Write(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			return out, err
		}
		out = append(out, fmt.Sprintf("%s: %d", name, n))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no \"# name\" sections found", hostOf(src.URL))
	}
	return out, nil
}

// prefixesFromJSON reads the format Google and Bing publish their crawler
// ranges in: {"prefixes":[{"ipv4Prefix":"66.249.64.0/27"},{"ipv6Prefix":…}]}.
func prefixesFromJSON(b []byte) ([]string, error) {
	var doc struct {
		Prefixes []struct {
			V4 string `json:"ipv4Prefix"`
			V6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, errors.New("not a prefixes JSON document")
	}
	var out []string
	for _, p := range doc.Prefixes {
		if p.V4 != "" {
			out = append(out, p.V4)
		}
		if p.V6 != "" {
			out = append(out, p.V6)
		}
	}
	return out, nil
}

// cleanLines keeps addresses and "# label" lines, drops everything else.
func cleanLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || isEntry(line) {
			out = append(out, line)
		}
	}
	return out
}

// splitSections turns "# google\n1.2.3.0/24\n# yandex\n…" into one list per
// section, named ip_<section>. Lines before the first section, and sections
// whose name is not a plain word, are dropped.
func splitSections(s string) map[string][]string {
	out := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "#")))
			name = strings.TrimPrefix(name, "ip_")
			cur = ""
			if listNameRe.MatchString(name) {
				cur = "ip_" + name
			}
			continue
		}
		if cur != "" && isEntry(line) {
			out[cur] = append(out[cur], line)
		}
	}
	return out
}

func isEntry(line string) bool {
	idx, err := ipindex.Parse(strings.NewReader(line))
	return err == nil && idx.Len() == 1
}

func countEntries(lines []string) int {
	n := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") {
			n++
		}
	}
	return n
}

// mergeLines appends the lines the file does not have yet, keeping what is
// there — including entries added by hand or by the engine's save_ip.
func mergeLines(path string, fresh []string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}
	have := cleanLines(string(b))
	seen := make(map[string]bool, len(have))
	for _, l := range have {
		seen[l] = true
	}
	for _, l := range fresh {
		if !seen[l] && !strings.HasPrefix(l, "#") {
			seen[l] = true
			have = append(have, l)
		}
	}
	return have
}
