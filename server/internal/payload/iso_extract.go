// Package payload owns upload, extraction and HTTP delivery of the binary
// blobs that AutoDeploy serves to clients: ISO contents (WIM/ESD and
// supporting files), driver packages and software installers, plus the
// generated unattend file (Phase 5).
//
// Reading a Windows install ISO is a real piece of work. AutoDeploy stores
// the uploaded ISO once, then extracts its files individually so the WIM
// or ESD can be served by HTTP range request rather than peeled out of a
// disc image on every client. Extraction uses the pure-Go iso9660 reader
// so the server has no CGO dependency.
package payload

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"
)

// ExtractISO walks the ISO at srcPath and writes every file into destDir,
// preserving the directory layout. Returns the total bytes written and the
// relative path of an install.wim or install.esd if either is found, so the
// caller can record it in the ISO row for the Boot Client to fetch later.
func ExtractISO(srcPath, destDir string) (totalBytes int64, wimRelPath string, err error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	img, err := iso9660.OpenImage(f)
	if err != nil {
		return 0, "", fmt.Errorf("open iso: %w", err)
	}
	root, err := img.RootDir()
	if err != nil {
		return 0, "", fmt.Errorf("read iso root: %w", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, "", err
	}

	written, wim, err := walk(root, destDir, "")
	if err != nil {
		return written, wim, err
	}
	return written, wim, nil
}

func walk(node *iso9660.File, destRoot, relPath string) (int64, string, error) {
	var total int64
	var wim string

	if node.IsDir() {
		children, err := node.GetChildren()
		if err != nil {
			return 0, "", err
		}
		for _, c := range children {
			name := c.Name()
			if name == "." || name == ".." {
				continue
			}
			childRel := filepath.Join(relPath, sanitiseISOName(name))
			if c.IsDir() {
				if err := os.MkdirAll(filepath.Join(destRoot, childRel), 0o755); err != nil {
					return total, wim, err
				}
				n, w, err := walk(c, destRoot, childRel)
				total += n
				if w != "" && wim == "" {
					wim = w
				}
				if err != nil {
					return total, wim, err
				}
				continue
			}
			n, w, err := writeFile(c, destRoot, childRel)
			total += n
			if w != "" && wim == "" {
				wim = w
			}
			if err != nil {
				return total, wim, err
			}
		}
		return total, wim, nil
	}
	return 0, "", errors.New("walk called on non-directory node")
}

func writeFile(node *iso9660.File, destRoot, relPath string) (int64, string, error) {
	abs := filepath.Join(destRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return 0, "", err
	}
	out, err := os.Create(abs)
	if err != nil {
		return 0, "", err
	}
	r := node.Reader()
	n, copyErr := io.Copy(out, r)
	closeErr := out.Close()
	if copyErr != nil {
		return n, "", copyErr
	}
	if closeErr != nil {
		return n, "", closeErr
	}

	low := strings.ToLower(filepath.Base(relPath))
	if low == "install.wim" || low == "install.esd" {
		return n, relPath, nil
	}
	return n, "", nil
}

// sanitiseISOName drops the ";1" file-version suffix some ISO9660
// implementations append.
func sanitiseISOName(name string) string {
	if i := strings.Index(name, ";"); i >= 0 {
		return name[:i]
	}
	return name
}
