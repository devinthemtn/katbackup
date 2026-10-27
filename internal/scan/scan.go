// Package scan finds candidate config files in a home directory.
package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/devinthemtn/katbackup/internal/paths"
)

type Flag string

const (
	FlagSecret Flag = "secret" // credentials; only backed up encrypted
	FlagNoise  Flag = "noise"  // caches, toolchains, history, app state
	FlagLarge  Flag = "large"  // over LargeBytes
)

const LargeBytes = 50 << 20

type Candidate struct {
	Path      string // home-relative
	IsDir     bool
	Size      int64 // -1 when not measured (noise)
	Flags     []Flag
	Suggested bool // include in a fresh manifest
}

func (c Candidate) Has(f Flag) bool {
	for _, x := range c.Flags {
		if x == f {
			return true
		}
	}
	return false
}

// SecretPatterns are home-relative globs for credentials. Unless a backup is
// encrypted, anything matching these is left out.
var SecretPatterns = []string{
	".ssh",
	".gnupg",
	".netrc",
	".aws",
	".kube",
	".docker/config.json",
	".npmrc",
	".pypirc",
	".git-credentials",
	".config/gh/hosts.yml",
	".claude.json",
	".claude/.credentials.json",
	"**/*.pem",
	"**/*.key",
	"**/id_rsa*",
	"**/id_ed25519*",
}

// noiseNames are top-level home entries that are not worth backing up.
var noiseNames = []string{
	".cache", ".npm", ".nvm", ".cargo", ".rustup", ".local", ".oh-my-zsh",
	".vscode-server", ".zed_server", ".cursor-server", ".expo", ".gradle", ".m2",
	".var", ".Trash", ".mozilla", ".copilot", ".gemini", ".claude", ".storybook",
	".histfile", ".lesshst", ".viminfo", ".wget-hsts", ".motd_shown",
	".sudo_as_admin_successful", ".Xauthority", ".xsession-errors",
	".zcompdump*", "*_history", ".bash_logout", "go", ".katConfigBackup",
}

// noiseConfig are ~/.config entries that are state rather than config.
var noiseConfig = []string{
	"go", "pulse", "procps", "dconf", "google-chrome", "chromium", "BraveSoftware",
	"Code", "discord", "Slack", "*.7z", "*.zip", "*.tar*",
}

// IsSecret reports whether a home-relative path matches a secret pattern,
// or is a directory containing one.
func IsSecret(rel string) bool {
	return paths.MatchAny(SecretPatterns, rel)
}

// Scan lists ~/.* and ~/.config/* with flags and a suggestion.
func Scan(home string) ([]Candidate, error) {
	var out []Candidate
	top, err := os.ReadDir(home)
	if err != nil {
		return nil, err
	}
	for _, d := range top {
		name := d.Name()
		if !strings.HasPrefix(name, ".") || name == ".config" {
			continue
		}
		out = append(out, candidate(home, name, d, matchName(noiseNames, name)))
	}
	cfgDir := filepath.Join(home, ".config")
	if sub, err := os.ReadDir(cfgDir); err == nil {
		for _, d := range sub {
			out = append(out, candidate(home, ".config/"+d.Name(), d, matchName(noiseConfig, d.Name())))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func candidate(home, rel string, d fs.DirEntry, noise bool) Candidate {
	c := Candidate{Path: rel, IsDir: d.IsDir(), Size: -1}
	if noise {
		c.Flags = append(c.Flags, FlagNoise)
	} else {
		c.Size = size(filepath.Join(home, rel))
		if c.Size > LargeBytes {
			c.Flags = append(c.Flags, FlagLarge)
		}
	}
	if IsSecret(rel) {
		c.Flags = append(c.Flags, FlagSecret)
	}
	c.Suggested = len(c.Flags) == 0
	return c
}

func matchName(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

func size(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// SecretsInside returns secret patterns that apply below the given candidates,
// e.g. ".config/gh/hosts.yml" for a ".config/gh" include.
func SecretsInside(includes []string) []string {
	var out []string
	for _, p := range SecretPatterns {
		if strings.Contains(p, "*") {
			continue
		}
		for _, inc := range includes {
			if strings.HasPrefix(p, inc+"/") {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
