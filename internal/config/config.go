// Package config loads and saves the katbackup manifest.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Keep    int      `toml:"keep"`
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
	if cfg.Keep < 0 {
		cfg.Keep = 0
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# katbackup manifest\n")
	buf.WriteString("# keep: number of snapshots to retain (0 = unlimited)\n")
	buf.WriteString("# secrets: only backed up when encrypting (encrypt = true or --encrypt)\n\n")
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
