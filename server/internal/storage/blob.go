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
	"sync"
	"time"
)

// BlobStore is a filesystem-backed payload store rooted at root. It
// serves reads, accepts streamed writes, and refuses to escape its
// root. Per-category overrides let operators relocate large payload
// trees (iso, drivers, software) to a different filesystem at runtime
// via the portal without rewriting the DB-stored relative paths.
type BlobStore struct {
	root string

	mu        sync.RWMutex
	overrides map[string]string // category prefix -> absolute root
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
	return &BlobStore{root: abs, overrides: map[string]string{}}, nil
}

// Root returns the absolute base directory. Useful for tests and
// logging. Per-category overrides (see SetCategoryRoot) shift the
// resolution for individual prefixes; this remains the fallback.
func (b *BlobStore) Root() string { return b.root }

// SetCategoryRoot configures an absolute path to use as the root for
// the given category prefix (the first slash-separated segment of a
// relative path, e.g. "iso", "drivers", "software"). Empty path
// clears the override and falls back to root/<category>. The path is
// created if it doesn't already exist.
//
// Callable at runtime. Existing files are NOT moved -- the operator
// is expected to relocate them before flipping the override.
func (b *BlobStore) SetCategoryRoot(category, absPath string) error {
	category = strings.Trim(category, "/")
	if category == "" {
		return fmt.Errorf("category must be non-empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	absPath = strings.TrimSpace(absPath)
	if absPath == "" {
		delete(b.overrides, category)
		return nil
	}
	if !filepath.IsAbs(absPath) {
		return fmt.Errorf("category root must be absolute: %q", absPath)
	}
	clean := filepath.Clean(absPath)
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", clean, err)
	}
	b.overrides[category] = clean
	return nil
}

// CategoryRoot returns the resolved root for a category. Returns the
// configured override if one is set, otherwise root/<category>.
// Useful for callers that operate on the raw filesystem (the TFTP
// listener, the ipxe static handler, the Downloads page scanner).
func (b *BlobStore) CategoryRoot(category string) string {
	category = strings.Trim(category, "/")
	b.mu.RLock()
	override, ok := b.overrides[category]
	b.mu.RUnlock()
	if ok && override != "" {
		return override
	}
	return filepath.Join(b.root, category)
}

// Resolve joins relative with the appropriate root (per-category
// override when configured, otherwise the store base) and refuses
// paths that try to escape it via "..".
func (b *BlobStore) Resolve(relative string) (string, error) {
	clean := filepath.Clean("/" + relative) // normalise; leading / makes Clean drop ".."
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", fmt.Errorf("empty relative path")
	}
	// Split off the category prefix.
	category, rest, _ := strings.Cut(clean, "/")
	base := b.CategoryRoot(category)
	abs := filepath.Join(base, rest)
	relCheck, err := filepath.Rel(base, abs)
	if err != nil || strings.HasPrefix(relCheck, "..") {
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

// Size returns the size in bytes of the blob at relative. It propagates
// os.ErrNotExist (via os.Stat) when the blob is absent, so callers can
// distinguish "no such blob" and fall back to a recorded value.
func (b *BlobStore) Size(relative string) (int64, error) {
	abs, err := b.Resolve(relative)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
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

// ListTree recursively enumerates every regular file under relative,
// returning each one's path RELATIVE TO relative (forward-slashed) plus
// size and modtime. Used to index an extracted ISO's media tree so a
// client can mirror the whole tree file-by-file. Returns an empty slice
// (not an error) when the directory doesn't exist yet. Symlinks are
// skipped. The walk is confined to the resolved root, so it cannot
// escape the blob store.
func (b *BlobStore) ListTree(relative string) ([]DirEntry, error) {
	root, err := b.Resolve(relative)
	if err != nil {
		return nil, err
	}
	var out []DirEntry
	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, DirEntry{
			Name:    filepath.ToSlash(rel),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// ListDir enumerates the regular files directly under relative,
// returning their (filename, size, modtime) without recursing. Used
// by the multi-file software-package UI to surface what's been
// uploaded; callers join the relative + filename to get the resolved
// path. Returns an empty slice (not an error) when the directory
// doesn't exist yet.
func (b *BlobStore) ListDir(relative string) ([]DirEntry, error) {
	abs, err := b.Resolve(relative)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		out = append(out, DirEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return out, nil
}

// DirEntry is one row returned by ListDir.
type DirEntry struct {
	Name    string
	Size    int64
	ModTime time.Time
}
