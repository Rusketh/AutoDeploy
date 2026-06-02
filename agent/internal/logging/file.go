package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	defaultMaxBytes = 5 * 1024 * 1024 // 5 MB
	defaultKeep     = 1               // keep 1 rotated file (.1)
)

// RotatingFile is a size-capped file writer. When the active log
// exceeds maxBytes the file is rotated: agent.log → agent.log.1
// (overwriting any previous .1) and a fresh agent.log is opened.
// Only one old file is kept to bound disk use to ~10 MB.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	written  int64
}

// OpenRotatingFile opens (or creates) path for append-mode writes.
// maxBytes caps the active file; 0 uses the 5 MB default.
func OpenRotatingFile(path string, maxBytes int64) (*RotatingFile, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, _ := f.Stat()
	var size int64
	if info != nil {
		size = info.Size()
	}
	return &RotatingFile{path: path, maxBytes: maxBytes, f: f, written: size}, nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.written+int64(len(p)) > r.maxBytes {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.written += int64(n)
	return n, err
}

func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

func (r *RotatingFile) rotate() {
	_ = r.f.Close()
	old := r.path + ".1"
	_ = os.Remove(old)
	_ = os.Rename(r.path, old)
	r.f, _ = os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	r.written = 0
}
