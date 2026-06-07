//go:build uitest

// Syntax check of the embedded JS interface. A single typo in the script
// breaks the entire SPA (the page stays blank), so we verify parsing via
// node. Requires node to be installed; run:
//
//	go test -tags=uitest ./internal/admin/ -run JSSyntax
package admin

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

var scriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

func TestJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found — skipping JS syntax check")
	}
	blocks := scriptRe.FindAllStringSubmatch(string(indexHTML), -1)
	if len(blocks) == 0 {
		t.Fatal("no <script> blocks found in the SPA")
	}
	for i, b := range blocks {
		f := filepath.Join(t.TempDir(), "block.js")
		if err := os.WriteFile(f, []byte(b[1]), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(node, "--check", f).CombinedOutput()
		if err != nil {
			t.Errorf("syntax error in script block #%d:\n%s", i, out)
		}
	}
}
