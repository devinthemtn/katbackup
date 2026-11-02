package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	for _, tc := range []struct{ name, stamped, module, git, want string }{
		{"release build", "1.2.3", "v1.2.3", "9.9.9", "1.2.3"},
		{"stamped wins", "1.2.3", "v9.9.9+dirty", "", "1.2.3"},
		{"tagged go install", "", "v1.2.3", "9.9.9", "1.2.3"},
		{"uncommitted changes", "", "v1.2.3+dirty", "", "1.2.3+dirty"},
		{"commits after a tag", "", "v1.2.4-0.20261102-abcdef", "", "1.2.4-0.20261102-abcdef"},
		{"go run", "", "(devel)", "1.2.3-dirty", "1.2.3-dirty"},
		{"no build info", "", "", "1.2.3", "1.2.3"},
		{"no git either", "", "(devel)", "", "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveVersion(tc.stamped, tc.module, func() string { return tc.git })
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGitDescribe(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	file := filepath.Join(dir, "f")
	edit := func(content string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got := gitDescribe(dir); got != "" {
		t.Errorf("outside a repo: %q", got)
	}
	if got := gitDescribe(""); got != "" {
		t.Errorf("no folder: %q", got)
	}

	git("init", "-q")
	edit("1")
	git("add", "f")
	git("commit", "-qm", "one")
	git("tag", "v1.2.3")
	if got := gitDescribe(dir); got != "1.2.3" {
		t.Errorf("on the tag: %q", got)
	}

	edit("2")
	if got := gitDescribe(dir); got != "1.2.3-dirty" {
		t.Errorf("uncommitted change: %q", got)
	}
}

// The version must come from katbackup's own source, not whatever repository
// the command happens to be run in.
func TestSourceDirIsThisPackage(t *testing.T) {
	if _, err := os.Stat(filepath.Join(sourceDir(), "root.go")); err != nil {
		t.Errorf("sourceDir() = %q: %v", sourceDir(), err)
	}
}
