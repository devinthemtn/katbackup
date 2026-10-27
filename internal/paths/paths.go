// Package paths handles home-relative path conversion and glob matching.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	BackupDirName = ".katConfigBackup"
	StagingName   = "staging"
)

// Home returns the current user's home directory.
func Home() (string, error) {
	return os.UserHomeDir()
}

// Expand replaces a leading "~" with home and makes relative paths home-relative.
func Expand(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	default:
		return filepath.Join(home, p)
	}
}

// RelToHome converts an absolute path into a slash-separated path relative to home.
func RelToHome(abs, home string) (string, error) {
	rel, err := filepath.Rel(home, abs)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is not inside %s", abs, home)
	}
	return filepath.ToSlash(rel), nil
}

// Normalize turns a user-supplied pattern ("~/.config/nvim", "/home/me/.zshrc",
// ".config/**/x") into a home-relative slash pattern.
func Normalize(pattern, home string) string {
	p := filepath.ToSlash(pattern)
	switch {
	case p == "~":
		return ""
	case strings.HasPrefix(p, "~/"):
		p = p[2:]
	case filepath.IsAbs(pattern):
		if rel, err := RelToHome(pattern, home); err == nil {
			p = rel
		}
	}
	return strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
}

// Match reports whether a home-relative path matches a normalized pattern.
// A pattern matches the path itself and everything beneath it, so ".ssh"
// matches ".ssh/id_ed25519".
func Match(pattern, rel string) bool {
	if pattern == "" {
		return false
	}
	if ok, _ := doublestar.Match(pattern, rel); ok {
		return true
	}
	if ok, _ := doublestar.Match(pattern+"/**", rel); ok {
		return true
	}
	return false
}

// MatchAny reports whether rel matches any of the normalized patterns.
func MatchAny(patterns []string, rel string) bool {
	for _, p := range patterns {
		if Match(p, rel) {
			return true
		}
	}
	return false
}

// BackupDir is where snapshots are stored.
func BackupDir(home string) string {
	return filepath.Join(home, BackupDirName)
}

// ConfigFile is the manifest location.
func ConfigFile(home string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "katbackup", "config.toml")
}
