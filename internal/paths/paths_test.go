package paths

import "testing"

func TestNormalize(t *testing.T) {
	home := "/home/kat"
	cases := map[string]string{
		"~/.zshrc":             ".zshrc",
		"/home/kat/.config/x/": ".config/x",
		"./.config":            ".config",
		"**/node_modules":      "**/node_modules",
	}
	for in, want := range cases {
		if got := Normalize(in, home); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{".ssh", ".ssh", true},
		{".ssh", ".ssh/id_ed25519", true},
		{".ssh", ".sshrc", false},
		{"**/node_modules", ".config/a/node_modules", true},
		{"**/node_modules", ".config/a/node_modules/x/y.js", true},
		{".config/**/*.toml", ".config/zellij/config.toml", true},
		{"**/*.log", ".config/x/debug.log", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.pattern, c.path, got)
		}
	}
}
