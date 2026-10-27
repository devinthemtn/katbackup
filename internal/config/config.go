// Package config loads and saves the katbackup manifest.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Keep    Keep     `toml:"keep"`
	Encrypt bool     `toml:"encrypt"`
	Include []string `toml:"include"`
	Exclude []string `toml:"exclude"`
	// Secrets are only backed up when the snapshot is encrypted.
	Secrets []string `toml:"secrets"`
}

var ErrNotFound = errors.New("config not found")

// DefaultExcludes skips build artifacts, caches and VCS metadata inside included folders.
var DefaultExcludes = []string{
	"**/node_modules",
	"**/.git",
	"**/__pycache__",
	"**/*.log",
	"**/Cache",
	"**/cache",
	"**/CachedData",
	"**/GPUCache",
	"**/*.sock",
}

// Keep is how many snapshots to retain. KeepAll (0) keeps every snapshot and is
// written as "all" in the config file.
type Keep int

const KeepAll Keep = 0

// ParseKeep accepts a non-negative number or "all".
func ParseKeep(s string) (Keep, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "all") {
		return KeepAll, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("keep must be a number or \"all\", got %q", s)
	}
	return Keep(n), nil
}

func (k *Keep) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case int64:
		if v < 0 {
			return fmt.Errorf("keep must not be negative, got %d", v)
		}
		*k = Keep(v)
		return nil
	case string:
		n, err := ParseKeep(v)
		*k = n
		return err
	}
	return fmt.Errorf("keep must be a number or \"all\", got %v", v)
}

func (k Keep) MarshalTOML() ([]byte, error) {
	if k == KeepAll {
		return []byte(`"all"`), nil
	}
	return []byte(strconv.Itoa(int(k))), nil
}

// String, Set and Type let Keep be used as a command-line flag.
func (k Keep) String() string {
	if k == KeepAll {
		return "all"
	}
	return strconv.Itoa(int(k))
}

func (k *Keep) Set(s string) error {
	n, err := ParseKeep(s)
	if err == nil {
		*k = n
	}
	return err
}

func (k *Keep) Type() string { return "n|all" }

func Default() Config {
	return Config{
		Keep:    10,
		Exclude: append([]string(nil), DefaultExcludes...),
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, fmt.Errorf("%w at %s (run `katbackup init`)", ErrNotFound, path)
	}
	if err != nil {
		return cfg, err
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# katbackup manifest\n")
	buf.WriteString("# keep: number of snapshots to retain, or \"all\" to never delete any\n")
	buf.WriteString("# secrets: only backed up when encrypting (encrypt = true or --encrypt)\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
