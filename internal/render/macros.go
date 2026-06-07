// Package render expands output macros and builds the HTTP response based on the
// redirect type and rand macros.
package render

import (
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// MacroDeps — data for expanding output macros.
type MacroDeps struct {
	Key       string   // [KEY] (url-encoded)
	Path      string   // [PATH]
	IP        string   // [IP]
	Country   string   // [COUNTRY], [()COUNTRY()]
	City      string   // [CITY], [()CITY()]
	Region    string   // [REGION]
	Lang      string   // [LANG]
	Device    string   // [DEVICE]
	Operator  string   // [OPERATOR]
	Domain    string   // [DOMAIN]
	UserAgent string   // [USERAGENT]
	CID       string   // [CID]
	Pars      []string // [PAR-1]..[PAR-5]
	DataDir   string   // .dat directory for [RANDLINE]/[RANDDFL]
	Rng       *rand.Rand
}

var (
	reRandNum  = regexp.MustCompile(`\[RANDNUM-([0-9]+)-([0-9]+)\]`)
	reRandStr  = regexp.MustCompile(`\[RANDSTR-\((.+?)\)-([0-9]+)\]`)
	reRandLine = regexp.MustCompile(`\[RANDLINE-\((.+?)\)-([0-9]+)(/u)?\]`)
	reRandDfl  = regexp.MustCompile(`\[RANDDFL-\((.+?)\)-([0-9]+)(/u)?\]`)
)

// Expand expands all supported macros in the out string.
func Expand(s string, d MacroDeps) string {
	rep := func(macro, val string) {
		if strings.Contains(s, macro) {
			s = strings.ReplaceAll(s, macro, val)
		}
	}
	rep("[KEY]", url.QueryEscape(d.Key))
	rep("[PATH]", d.Path)
	rep("[IP]", d.IP)
	rep("[COUNTRY]", d.Country)
	rep("[CITY]", d.City)
	rep("[REGION]", d.Region)
	rep("[LANG]", d.Lang)
	rep("[DEVICE]", d.Device)
	rep("[OPERATOR]", d.Operator)
	rep("[DOMAIN]", d.Domain)
	rep("[USERAGENT]", d.UserAgent)
	rep("[CID]", d.CID)
	// new variants with the () wrapper
	rep("[()COUNTRY()]", d.Country)
	rep("[()CITY()]", d.City)
	for i := 0; i < 5; i++ {
		v := ""
		if i < len(d.Pars) {
			v = d.Pars[i]
		}
		rep("[PAR-"+strconv.Itoa(i+1)+"]", v)
	}
	s = expandRandNum(s, d.Rng)
	s = expandRandStr(s, d.Rng)
	s = expandRandLine(s, d)
	s = expandRandDfl(s, d)
	return s
}

func intn(rng *rand.Rand, n int) int {
	if n <= 0 {
		return 0
	}
	if rng != nil {
		return rng.Intn(n)
	}
	return rand.Intn(n)
}

// [RANDNUM-min-max] → random integer in [min, max].
func expandRandNum(s string, rng *rand.Rand) string {
	return replaceFunc(reRandNum, s, func(g []string) string {
		min, _ := strconv.Atoi(g[1])
		max, _ := strconv.Atoi(g[2])
		if max < min {
			min, max = max, min
		}
		return strconv.Itoa(min + intn(rng, max-min+1))
	})
}

// [RANDSTR-(charset)-count] → random string of length count from charset.
func expandRandStr(s string, rng *rand.Rand) string {
	return replaceFunc(reRandStr, s, func(g []string) string {
		set := g[1]
		count, _ := strconv.Atoi(g[2])
		if set == "" || count <= 0 {
			return ""
		}
		var b strings.Builder
		for i := 0; i < count; i++ {
			b.WriteByte(set[intn(rng, len(set))])
		}
		return b.String()
	})
}

// [RANDLINE-(file)-count(/u)] → count random lines from DataDir/file joined by ';'.
// The /u suffix means no repeats. A nonexistent file → empty substitution.
func expandRandLine(s string, d MacroDeps) string {
	return replaceFunc(reRandLine, s, func(g []string) string {
		file := g[1]
		count, _ := strconv.Atoi(g[2])
		unique := g[3] == "/u"
		lines := readLines(filepath.Join(d.DataDir, file))
		if len(lines) == 0 || count <= 0 {
			return ""
		}
		if unique && count > len(lines) {
			count = len(lines)
		}
		var picked []string
		used := map[int]bool{}
		for len(picked) < count {
			i := intn(d.Rng, len(lines))
			if unique {
				if used[i] {
					continue
				}
				used[i] = true
			}
			picked = append(picked, lines[i])
		}
		return strings.Join(picked, ";")
	})
}

// [RANDDFL-(dir)-count(/u)] → picks a random file in DataDir/dir and takes
// count random lines from it. /u means no repeats.
func expandRandDfl(s string, d MacroDeps) string {
	return replaceFunc(reRandDfl, s, func(g []string) string {
		dir := filepath.Join(d.DataDir, g[1])
		count, _ := strconv.Atoi(g[2])
		unique := g[3] == "/u"
		entries, err := os.ReadDir(dir)
		if err != nil || count <= 0 {
			return ""
		}
		var files []string
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() && n != "randdfl.dat" && n != ".htaccess" {
				files = append(files, n)
			}
		}
		if len(files) == 0 {
			return ""
		}
		lines := readLines(filepath.Join(dir, files[intn(d.Rng, len(files))]))
		if len(lines) == 0 {
			return ""
		}
		if unique && count > len(lines) {
			count = len(lines)
		}
		var picked []string
		used := map[int]bool{}
		for len(picked) < count {
			i := intn(d.Rng, len(lines))
			if unique {
				if used[i] {
					continue
				}
				used[i] = true
			}
			picked = append(picked, lines[i])
		}
		return strings.Join(picked, ";")
	})
}

// replaceFunc replaces all matches of re, passing the subgroups to fn.
func replaceFunc(re *regexp.Regexp, s string, fn func(groups []string) string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		return fn(re.FindStringSubmatch(m))
	})
}

func readLines(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
