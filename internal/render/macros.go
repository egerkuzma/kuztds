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

// The RAND* macros are anchored: Expand matches them at the current position
// rather than scanning the whole string, so a value substituted for a scalar
// macro is never re-examined for macros of its own.
var (
	reRandNum  = regexp.MustCompile(`^\[RANDNUM-([0-9]+)-([0-9]+)\]`)
	reRandStr  = regexp.MustCompile(`^\[RANDSTR-\((.+?)\)-([0-9]+)\]`)
	reRandLine = regexp.MustCompile(`^\[RANDLINE-\((.+?)\)-([0-9]+)(/u)?\]`)
	reRandDfl  = regexp.MustCompile(`^\[RANDDFL-\((.+?)\)-([0-9]+)(/u)?\]`)
)

// Expand expands all supported macros in the out string.
//
// Expansion is a single left-to-right pass and substituted values are written
// out verbatim, never rescanned. That matters because several macros carry
// visitor-controlled data ([USERAGENT], [DOMAIN], [PAR-n], [PATH], [LANG]): if
// the result were scanned again, a visitor could put macros of their own into a
// header and have the engine execute them — reading files through
// [RANDLINE-(../...)] or forcing a huge allocation through [RANDSTR].
//
// Macros nested inside a RAND* argument still work, because the argument comes
// from the template itself; only its scalar macros are expanded (see
// expandScalars), and the visitor never gets a say in it.
func Expand(s string, d MacroDeps) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '[' {
			b.WriteByte(s[i])
			i++
			continue
		}
		val, n := d.macroAt(s[i:])
		if n == 0 {
			b.WriteByte('[')
			i++
			continue
		}
		b.WriteString(val)
		i += n
	}
	return b.String()
}

// macroAt matches a single macro at the start of s. It returns the replacement
// and the number of bytes consumed, or n == 0 when s does not start with a
// macro.
func (d MacroDeps) macroAt(s string) (val string, n int) {
	for _, m := range d.scalars() {
		if strings.HasPrefix(s, m.name) {
			return m.val, len(m.name)
		}
	}
	if g, n := matchRand(reRandNum, s); n > 0 {
		return randNum(d.expandScalars(g[1]), d.expandScalars(g[2]), d.Rng), n
	}
	if g, n := matchRand(reRandStr, s); n > 0 {
		return randStr(d.expandScalars(g[1]), d.expandScalars(g[2]), d.Rng), n
	}
	if g, n := matchRand(reRandLine, s); n > 0 {
		return d.randLine(d.expandScalars(g[1]), d.expandScalars(g[2]), g[3] == "/u"), n
	}
	if g, n := matchRand(reRandDfl, s); n > 0 {
		return d.randDfl(d.expandScalars(g[1]), d.expandScalars(g[2]), g[3] == "/u"), n
	}
	return "", 0
}

type scalarMacro struct{ name, val string }

// scalars lists the plain name → value macros. [()COUNTRY()] and [()CITY()]
// come first: they are longer literals, and prefix matching takes the first hit.
func (d MacroDeps) scalars() []scalarMacro {
	out := []scalarMacro{
		{"[()COUNTRY()]", d.Country},
		{"[()CITY()]", d.City},
		{"[KEY]", url.QueryEscape(d.Key)},
		{"[PATH]", d.Path},
		{"[IP]", d.IP},
		{"[COUNTRY]", d.Country},
		{"[CITY]", d.City},
		{"[REGION]", d.Region},
		{"[LANG]", d.Lang},
		{"[DEVICE]", d.Device},
		{"[OPERATOR]", d.Operator},
		{"[DOMAIN]", d.Domain},
		{"[USERAGENT]", d.UserAgent},
		{"[CID]", d.CID},
	}
	for i := 0; i < 5; i++ {
		v := ""
		if i < len(d.Pars) {
			v = d.Pars[i]
		}
		out = append(out, scalarMacro{"[PAR-" + strconv.Itoa(i+1) + "]", v})
	}
	return out
}

// expandScalars replaces only the scalar macros, used for RAND* arguments taken
// from the template (e.g. [RANDLINE-([COUNTRY].dat)-1]). It does not recurse
// into RAND* macros, so it cannot be driven by a substituted value.
func (d MacroDeps) expandScalars(s string) string {
	if !strings.Contains(s, "[") {
		return s
	}
	for _, m := range d.scalars() {
		if strings.Contains(s, m.name) {
			s = strings.ReplaceAll(s, m.name, m.val)
		}
	}
	return s
}

// matchRand matches an anchored RAND* pattern at the start of s.
func matchRand(re *regexp.Regexp, s string) (groups []string, n int) {
	g := re.FindStringSubmatch(s)
	if g == nil {
		return nil, 0
	}
	return g, len(g[0])
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
func randNum(minRaw, maxRaw string, rng *rand.Rand) string {
	min, _ := strconv.Atoi(minRaw)
	max, _ := strconv.Atoi(maxRaw)
	if max < min {
		min, max = max, min
	}
	return strconv.Itoa(min + intn(rng, max-min+1))
}

// [RANDSTR-(charset)-count] → random string of length count from charset.
func randStr(set, countRaw string, rng *rand.Rand) string {
	count, _ := strconv.Atoi(countRaw)
	if set == "" || count <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < count; i++ {
		b.WriteByte(set[intn(rng, len(set))])
	}
	return b.String()
}

// [RANDLINE-(file)-count(/u)] → count random lines from DataDir/file joined by ';'.
// The /u suffix means no repeats. A nonexistent file → empty substitution.
func (d MacroDeps) randLine(file, countRaw string, unique bool) string {
	count, _ := strconv.Atoi(countRaw)
	path, ok := underDir(d.DataDir, file)
	if !ok {
		return ""
	}
	return pickLines(readLines(path), count, unique, d.Rng)
}

// [RANDDFL-(dir)-count(/u)] → picks a random file in DataDir/dir and takes
// count random lines from it. /u means no repeats.
func (d MacroDeps) randDfl(sub, countRaw string, unique bool) string {
	count, _ := strconv.Atoi(countRaw)
	dir, ok := underDir(d.DataDir, sub)
	if !ok {
		return ""
	}
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
	return pickLines(readLines(filepath.Join(dir, files[intn(d.Rng, len(files))])), count, unique, d.Rng)
}

// pickLines takes count random lines joined by ';' (no repeats when unique).
func pickLines(lines []string, count int, unique bool, rng *rand.Rand) string {
	if len(lines) == 0 || count <= 0 {
		return ""
	}
	if unique && count > len(lines) {
		count = len(lines)
	}
	var picked []string
	used := map[int]bool{}
	for len(picked) < count {
		i := intn(rng, len(lines))
		if unique {
			if used[i] {
				continue
			}
			used[i] = true
		}
		picked = append(picked, lines[i])
	}
	return strings.Join(picked, ";")
}

// underDir resolves rel against base and refuses anything that escapes base.
// Without it "[RANDLINE-(../../etc/passwd)-1]" would read outside the data
// directory and put the result straight into the visitor's redirect.
func underDir(base, rel string) (string, bool) {
	if base == "" || rel == "" {
		return "", false
	}
	if filepath.IsAbs(rel) {
		return "", false
	}
	clean := filepath.Clean(base)
	p := filepath.Join(clean, rel)
	if p != clean && !strings.HasPrefix(p, clean+string(filepath.Separator)) {
		return "", false
	}
	return p, true
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
