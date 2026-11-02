package scan_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devinthemtn/katbackup/internal/scan"
)

// init writes suggestions straight into the config, so restore's leftovers
// mustn't be among them.
func TestRestoreLeftoversAreNotSuggested(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{
		".zshrc",
		".zshrc.katbak-20261102-150405",
		".config/app/settings",
		".config/app.katbak-20261102-150405/settings",
		".config/.katbackup-123/x",
	} {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cands, err := scan.Scan(home)
	if err != nil {
		t.Fatal(err)
	}
	suggested := map[string]bool{}
	for _, c := range cands {
		if c.Suggested {
			suggested[c.Path] = true
		}
	}
	if len(suggested) != 2 || !suggested[".zshrc"] || !suggested[".config/app"] {
		t.Errorf("suggested %v", suggested)
	}
}
