package archive_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/devinthemtn/katbackup/internal/archive"
)

// failLast succeeds until it has seen failAt writes, then fails. A negative
// failAt never fails, which is how the total number of writes gets counted.
type failLast struct {
	writes int
	failAt int
}

var errDiskFull = errors.New("disk full")

func (w *failLast) Write(p []byte) (int, error) {
	w.writes++
	if w.failAt >= 0 && w.writes >= w.failAt {
		return 0, errDiskFull
	}
	return len(p), nil
}

// A failure writing the final encrypted chunk must not be reported as success:
// the caller would go on to prune older, good snapshots.
func TestWriteReportsEncryptedCloseError(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx := archive.Index{Entries: []archive.Entry{{Path: "f", Type: archive.TypeFile, Mode: 0o600, Size: 4}}}

	count := &failLast{failAt: -1}
	if err := archive.Write(count, src, idx, "pass"); err != nil {
		t.Fatal(err)
	}
	err := archive.Write(&failLast{failAt: count.writes}, src, idx, "pass")
	if !errors.Is(err, errDiskFull) {
		t.Fatalf("failed final write was not reported: %v", err)
	}
}

// writeRaw builds an unencrypted archive around an arbitrary index, as a
// hand-crafted file would be, without any of Write's normalization.
func writeRaw(t *testing.T, idx archive.Index) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "2026-01-01_000000.tar.zst")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: archive.IndexName, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenRejectsUnsafePaths(t *testing.T) {
	tests := map[string][]archive.Entry{
		"parent traversal": {{Path: "../evil", Type: archive.TypeFile}},
		"nested traversal": {{Path: "a/../../evil", Type: archive.TypeFile}},
		"absolute":         {{Path: "/etc/evil", Type: archive.TypeFile}},
		"empty":            {{Path: "", Type: archive.TypeFile}},
		"root":             {{Path: ".", Type: archive.TypeDir}},
		"not canonical":    {{Path: "a/../b", Type: archive.TypeFile}},
		"child of symlink": {
			{Path: "link", Type: archive.TypeSymlink, LinkTarget: "/etc"},
			{Path: "link/evil", Type: archive.TypeFile},
		},
		"deep child of symlink": {
			{Path: "link", Type: archive.TypeSymlink, LinkTarget: "/etc"},
			{Path: "link/a/evil", Type: archive.TypeFile},
		},
		"child listed before its symlink": {
			{Path: "link/evil", Type: archive.TypeFile},
			{Path: "link", Type: archive.TypeSymlink, LinkTarget: "/etc"},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			p := writeRaw(t, archive.Index{Version: archive.IndexVersion, Entries: entries})
			if _, err := archive.ReadIndex(p, ""); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

func TestOpenAcceptsOrdinaryPaths(t *testing.T) {
	p := writeRaw(t, archive.Index{Version: archive.IndexVersion, Entries: []archive.Entry{
		{Path: ".config", Type: archive.TypeDir},
		{Path: ".config/nvim/init.lua", Type: archive.TypeFile},
		{Path: ".config/link", Type: archive.TypeSymlink, LinkTarget: "../../outside"},
		{Path: ".config/link-sibling/file..name", Type: archive.TypeFile},
	}})
	if _, err := archive.ReadIndex(p, ""); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsNewerFormat(t *testing.T) {
	p := writeRaw(t, archive.Index{Version: archive.IndexVersion + 1})
	_, err := archive.ReadIndex(p, "")
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("want an error about a newer format, got %v", err)
	}
}
