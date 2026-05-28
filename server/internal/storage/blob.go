// blob.go owns the on-disk payload tree under AUTODEPLOY_DATA_DIR. Storage
// rows hold paths RELATIVE to that root; this code joins them with the root
// when reading or writing. Keeping the root out of the database means the
// data directory can move between environments without rewriting rows.
package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BlobStore is a filesystem-backed payload store rooted at root. It serves
// reads, accepts streamed writes, and refuses to escape its root.
type BlobStore struct {
	root string
}

// NewBlobStore returns a store rooted at root, creating the directory if
// needed. root must be an absolute or relative directory path.
func NewBlobStore(root string) (*BlobStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", abs, err)
	}
	return &BlobStore{root: abs}, nil
}

// Root returns the absolute root directory. Useful for tests and logging.
func (b *BlobStore) Root() string { return b.root }

// Resolve joins relative with the store root and refuses paths that try to
// escape the root via "..". Returns the absolute path.
func (b *BlobStore) Resolve(relative string) (string, error) {
	clean := filepath.Clean("/" + relative)            // normalise; leading / makes Clean drop ".."
	abs := filepath.Join(b.root, clean)                 // root + cleaned
	rel, err := filepath.Rel(b.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes blob root", relative)
	}
	return abs, nil
}

// Open opens a blob for reading.
func (b *BlobStore) Open(relative string) (*os.File, error) {
	abs, err := b.Resolve(relative)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

// WriteStream writes to relative atomically: stream to a sibling .tmp file,
// fsync, rename into place. Returns the number of bytes written.
func (b *BlobStore) WriteStream(relative string, src io.Reader) (int64, error) {
	abs, err := b.Resolve(relative)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return 0, fmt.Errorf("mkdir %s: %w", filepath.Dir(abs), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), filepath.Base(abs)+".tmp.*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup if rename didn't happen.
		_ = os.Remove(tmpName)
	}()
	n, err := io.Copy(tmp, src)
	if err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return n, err
	}
	if err := tmp.Close(); err != nil {
		return n, err
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return n, err
	}
	return n, nil
}

// Remove deletes the file (and an empty parent directory if applicable).
func (b *BlobStore) Remove(relative string) error {
	abs, err := b.Resolve(relative)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	return nil
}

// EnsureDir creates the directory (and parents) for a given relative path.
func (b *BlobStore) EnsureDir(relative string) (string, error) {
	abs, err := b.Resolve(relative)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
}
