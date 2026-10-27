// Package archive writes and reads snapshot archives: tar → zstd → optional age.
//
// The first tar entry is always IndexName, a JSON description of every other
// entry, so a snapshot's contents can be listed without extracting it.
package archive

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

const (
	IndexName    = "katbackup-index.json"
	IndexVersion = 1
)

type EntryType string

const (
	TypeFile    EntryType = "file"
	TypeDir     EntryType = "dir"
	TypeSymlink EntryType = "symlink"
)

type Entry struct {
	Path       string      `json:"path"` // home-relative, slash separated
	Type       EntryType   `json:"type"`
	Mode       fs.FileMode `json:"mode"` // permission bits
	Size       int64       `json:"size"`
	ModTime    time.Time   `json:"mtime"`
	SHA256     string      `json:"sha256,omitempty"`
	LinkTarget string      `json:"linkTarget,omitempty"`
}

type Index struct {
	Version   int       `json:"version"`
	Hostname  string    `json:"hostname"`
	Created   time.Time `json:"created"`
	Encrypted bool      `json:"encrypted"`
	Entries   []Entry   `json:"entries"`
}

var (
	ErrBadPassphrase = errors.New("wrong passphrase")
	ErrNoIndex       = errors.New("archive has no katbackup index")
)

// IsEncrypted reports whether the archive at path is age-encrypted.
func IsEncrypted(path string) bool {
	return strings.HasSuffix(path, ".age")
}

// Write streams the files described by idx, read from srcRoot, into dst.
// A non-empty passphrase encrypts the archive.
func Write(dst io.Writer, srcRoot string, idx Index, passphrase string) (err error) {
	out := dst
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return err
		}
		aw, err := age.Encrypt(dst, r)
		if err != nil {
			return err
		}
		defer closeInto(aw, &err)
		out = aw
	}
	zw, err := zstd.NewWriter(out)
	if err != nil {
		return err
	}
	defer closeInto(zw, &err)
	tw := tar.NewWriter(zw)
	defer closeInto(tw, &err)

	idx.Version = IndexVersion
	idx.Encrypted = passphrase != ""
	data, err := json.MarshalIndent(idx, "", " ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: IndexName, Typeflag: tar.TypeReg, Mode: 0o644,
		Size: int64(len(data)), ModTime: idx.Created,
	}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}

	for _, e := range idx.Entries {
		if err := writeEntry(tw, srcRoot, e); err != nil {
			return fmt.Errorf("%s: %w", e.Path, err)
		}
	}
	return nil
}

func writeEntry(tw *tar.Writer, srcRoot string, e Entry) error {
	hdr := &tar.Header{Name: e.Path, Mode: int64(e.Mode.Perm()), ModTime: e.ModTime}
	switch e.Type {
	case TypeDir:
		hdr.Typeflag = tar.TypeDir
		hdr.Name += "/"
		return tw.WriteHeader(hdr)
	case TypeSymlink:
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = e.LinkTarget
		return tw.WriteHeader(hdr)
	case TypeFile:
		f, err := os.Open(filepath.Join(srcRoot, filepath.FromSlash(e.Path)))
		if err != nil {
			return err
		}
		defer f.Close()
		hdr.Typeflag = tar.TypeReg
		hdr.Size = e.Size
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.CopyN(tw, f, e.Size)
		return err
	default:
		return fmt.Errorf("unknown entry type %q", e.Type)
	}
}

func closeInto(c io.Closer, err *error) {
	if cerr := c.Close(); cerr != nil && *err == nil {
		*err = cerr
	}
}

// Reader iterates over a snapshot archive. The index is read on Open.
type Reader struct {
	Index   Index
	f       *os.File
	zr      *zstd.Decoder
	tr      *tar.Reader
	entries map[string]Entry
}

// Open opens an archive and reads its index. passphrase is required for
// encrypted archives and ignored otherwise.
func Open(path, passphrase string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var in io.Reader = f
	if IsEncrypted(path) {
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			f.Close()
			return nil, err
		}
		in, err = age.Decrypt(f, id)
		if err != nil {
			f.Close()
			var nomatch *age.NoIdentityMatchError
			if errors.As(err, &nomatch) {
				return nil, ErrBadPassphrase
			}
			return nil, err
		}
	}
	zr, err := zstd.NewReader(in)
	if err != nil {
		f.Close()
		return nil, err
	}
	r := &Reader{f: f, zr: zr, tr: tar.NewReader(zr)}
	if err := r.readIndex(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *Reader) readIndex() error {
	hdr, err := r.tr.Next()
	if err != nil || hdr.Name != IndexName {
		return ErrNoIndex
	}
	if err := json.NewDecoder(r.tr).Decode(&r.Index); err != nil {
		return fmt.Errorf("decode index: %w", err)
	}
	r.entries = make(map[string]Entry, len(r.Index.Entries))
	for _, e := range r.Index.Entries {
		r.entries[e.Path] = e
	}
	return nil
}

// Next returns the next entry and, for files, a reader over its content.
// It returns io.EOF when the archive is exhausted.
func (r *Reader) Next() (Entry, io.Reader, error) {
	for {
		hdr, err := r.tr.Next()
		if err != nil {
			return Entry{}, nil, err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if e, ok := r.entries[name]; ok {
			return e, r.tr, nil
		}
	}
}

func (r *Reader) Close() error {
	r.zr.Close()
	return r.f.Close()
}

// ReadIndex opens an archive just long enough to read its index.
func ReadIndex(path, passphrase string) (Index, error) {
	r, err := Open(path, passphrase)
	if err != nil {
		return Index{}, err
	}
	defer r.Close()
	return r.Index, nil
}
