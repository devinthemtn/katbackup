package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	for _, tc := range []struct {
		in   string
		want Keep
		err  bool
	}{
		{`keep = 5`, 5, false},
		{`keep = "all"`, KeepAll, false},
		{`keep = "ALL"`, KeepAll, false},
		{`keep = 0`, KeepAll, false},
		{`keep = "7"`, 7, false},
		{`keep = -1`, 0, true},
		{`keep = "some"`, 0, true},
		{``, 10, false},
	} {
		os.WriteFile(path, []byte(tc.in), 0o644)
		cfg, err := Load(path)
		if (err != nil) != tc.err {
			t.Errorf("%q: err = %v", tc.in, err)
			continue
		}
		if !tc.err && cfg.Keep != tc.want {
			t.Errorf("%q: keep = %d, want %d", tc.in, cfg.Keep, tc.want)
		}
	}
}

func TestSaveKeepRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, k := range []Keep{KeepAll, 3} {
		cfg := Default()
		cfg.Keep = k
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if k == KeepAll && !strings.Contains(string(data), `keep = "all"`) {
			t.Errorf("saved config:\n%s", data)
		}
		got, err := Load(path)
		if err != nil || got.Keep != k {
			t.Errorf("round trip %d: got %d, %v", k, got.Keep, err)
		}
	}
}
