package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExpand_SubstitutedValuesAreNotRescanned is the core of the fix: a value put
// in by a scalar macro must be written out as-is. Several of those values come
// straight from the visitor, so rescanning them would let a visitor run macros.
func TestExpand_SubstitutedValuesAreNotRescanned(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		deps MacroDeps
		want string
	}{
		{"par", "p=[PAR-1]", MacroDeps{Pars: []string{"[RANDNUM-777-777]"}}, "p=[RANDNUM-777-777]"},
		{"useragent", "ua=[USERAGENT]", MacroDeps{UserAgent: "[RANDSTR-(Z)-8]"}, "ua=[RANDSTR-(Z)-8]"},
		{"domain", "d=[DOMAIN]", MacroDeps{Domain: "[RANDNUM-1-1]"}, "d=[RANDNUM-1-1]"},
		{"path", "h=[PATH]", MacroDeps{Path: "[RANDNUM-1-1]"}, "h=[RANDNUM-1-1]"},
		{"lang", "l=[LANG]", MacroDeps{Lang: "[RANDNUM-1-1]"}, "l=[RANDNUM-1-1]"},
		// A substituted value must not be able to complete a macro started by
		// the template either.
		{"split", "x=[RANDNUM-1-[PAR-1]", MacroDeps{Pars: []string{"9]"}}, "x=[RANDNUM-1-9]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Expand(c.tmpl, c.deps); got != c.want {
				t.Fatalf("Expand(%q) = %q, want %q", c.tmpl, got, c.want)
			}
		})
	}
}

// TestExpand_RandlineStaysInsideDataDir covers the other half: the file argument
// of [RANDLINE] must not walk out of the data directory.
func TestExpand_RandlineStaysInsideDataDir(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("TOP-SECRET-LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "ok.dat"), []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := MacroDeps{DataDir: data}

	for _, esc := range []string{"../secret.txt", "../../secret.txt", "sub/../../secret.txt", "/etc/hostname"} {
		if got := Expand("[RANDLINE-("+esc+")-1]", d); got != "" {
			t.Fatalf("[RANDLINE-(%s)-1] = %q, want empty — the path escapes DataDir", esc, got)
		}
	}
	// A normal file inside the directory keeps working.
	if got := Expand("[RANDLINE-(ok.dat)-1]", d); got != "inside" {
		t.Fatalf("[RANDLINE-(ok.dat)-1] = %q, want %q", got, "inside")
	}
}

// TestExpand_RanddflStaysInsideDataDir is the same containment check for the
// directory argument of [RANDDFL].
func TestExpand_RanddflStaysInsideDataDir(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	outside := filepath.Join(root, "outside")
	for _, p := range []string{data, outside, filepath.Join(data, "pool")} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "a.dat"), []byte("LEAKED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "pool", "a.dat"), []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := MacroDeps{DataDir: data}

	if got := Expand("[RANDDFL-(../outside)-1]", d); got != "" {
		t.Fatalf("[RANDDFL-(../outside)-1] = %q, want empty", got)
	}
	if got := Expand("[RANDDFL-(pool)-1]", d); got != "inside" {
		t.Fatalf("[RANDDFL-(pool)-1] = %q, want %q", got, "inside")
	}
}

// TestExpand_TemplateNestingStillWorks pins the behaviour the fix had to keep:
// a scalar macro inside a RAND* argument comes from the template, not from the
// visitor, so it is still expanded.
func TestExpand_TemplateNestingStillWorks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ru.dat"), []byte("moscow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Expand("[RANDLINE-([COUNTRY].dat)-1]", MacroDeps{DataDir: dir, Country: "ru"})
	if got != "moscow" {
		t.Fatalf("nested scalar in a RANDLINE argument = %q, want %q", got, "moscow")
	}
}

// TestExpand_UnknownBracketsSurvive guards the single-pass rewrite against
// eating text it does not understand.
func TestExpand_UnknownBracketsSurvive(t *testing.T) {
	in := "a[b][NOPE][KEY][RANDNUM-x-y]] ["
	got := Expand(in, MacroDeps{Key: "k"})
	want := "a[b][NOPE]k[RANDNUM-x-y]] ["
	if got != want {
		t.Fatalf("Expand(%q) = %q, want %q", in, got, want)
	}
	if strings.Count(got, "[") != strings.Count(want, "[") {
		t.Fatalf("bracket count changed: %q", got)
	}
}
