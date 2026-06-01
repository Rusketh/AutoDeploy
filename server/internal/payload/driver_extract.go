// Driver-package extract: unpack the uploaded zip of an SCCM driver
// package into a structured tree of .inf-based driver files, and walk
// the tree to surface metadata operators care about (Class, Provider,
// DriverVer). The Boot Client downloads the original zip blob; this
// endpoint exists to validate the package is well-formed and to give
// the portal something to show beyond "1.2 GB uploaded".
//
// The format we accept is a zip archive containing a tree of files,
// the SCCM "Source" directory layout being the common case. Any
// .inf file anywhere in the tree counts as one driver definition.
//
// Open question #3 (design Section 15) asks what driver format
// AutoDeploy should ingest. This is the pragmatic answer: SCCM
// exports tend to ship as zips of the driver source tree; we
// validate that shape, walk it, and let the Boot Client do the
// offline-servicing equivalent of dism /Add-Driver by extracting the
// same tree into %WINDIR%\INF\AutoDeploy\<id>\ on the target.

package payload

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// INFEntry describes one discovered .inf file inside a driver package.
type INFEntry struct {
	Path     string `json:"path"`     // relative path inside the package
	Class    string `json:"class"`
	Provider string `json:"provider"`
	Version  string `json:"version"`
	Date     string `json:"date"`
}

// DriverExtractResult is the response from POST /api/v1/drivers/{id}/extract.
type DriverExtractResult struct {
	Bytes     int64      `json:"bytes_extracted"`
	FileCount int        `json:"file_count"`
	INFCount  int        `json:"inf_count"`
	INFs      []INFEntry `json:"inf_entries"`
}

// extractDriver is the POST /api/v1/drivers/{id}/extract handler.
// Reads data/drivers/{id}/payload.bin (the uploaded zip), unpacks it
// under data/drivers/{id}/files/, scans .inf files, and writes a
// metadata.json summary alongside.
func (s *Service) extractDriver(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := ExtractDriverPackage(r.Context(), s.Drivers, s.Blobs, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusOK, res)
}

// ExtractDriverPackage runs the extract pipeline for a driver row id:
// reads the previously uploaded zip from data/drivers/{id}/payload.bin,
// unpacks it into data/drivers/{id}/files/, scans .inf metadata, and
// writes data/drivers/{id}/files/metadata.json so subsequent reads
// don't have to re-walk the tree. Callable from any package that has
// the repo + blob store.
func ExtractDriverPackage(ctx context.Context, drivers *model.DriverPackageRepo, blobs *storage.BlobStore, id model.ID) (DriverExtractResult, error) {
	pkg, err := drivers.Get(ctx, id)
	if err != nil {
		return DriverExtractResult{}, err
	}
	srcRel := filepath.ToSlash(filepath.Join("drivers", fmt.Sprint(int64(id)), "payload.bin"))
	srcAbs, err := blobs.Resolve(srcRel)
	if err != nil {
		return DriverExtractResult{}, err
	}
	if _, err := os.Stat(srcAbs); os.IsNotExist(err) {
		return DriverExtractResult{}, fmt.Errorf("driver payload not uploaded; upload a zip first")
	}
	destAbs, err := blobs.EnsureDir(filepath.ToSlash(filepath.Join("drivers", fmt.Sprint(int64(id)), "files")))
	if err != nil {
		return DriverExtractResult{}, err
	}
	if err := os.RemoveAll(destAbs); err != nil {
		return DriverExtractResult{}, err
	}
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return DriverExtractResult{}, err
	}
	res, err := extractDriverZip(srcAbs, destAbs)
	if err != nil {
		return DriverExtractResult{}, fmt.Errorf("extract: %w", err)
	}
	metaPath := filepath.Join(destAbs, "metadata.json")
	if mf, mferr := os.Create(metaPath); mferr == nil {
		_ = json.NewEncoder(mf).Encode(res)
		_ = mf.Close()
	}
	pkg.SizeBytes = res.Bytes
	_ = drivers.Update(ctx, pkg)
	return res, nil
}

// ReadDriverMetadata reads the previously-written metadata.json for
// a driver package. Returns ok=false (no error) when the file doesn't
// exist (package not yet extracted, or extraction failed). The portal
// uses this on the edit form to show what's inside without having to
// re-extract on every render.
func ReadDriverMetadata(blobs *storage.BlobStore, id model.ID) (DriverExtractResult, bool, error) {
	rel := filepath.ToSlash(filepath.Join("drivers", fmt.Sprint(int64(id)), "files", "metadata.json"))
	abs, err := blobs.Resolve(rel)
	if err != nil {
		return DriverExtractResult{}, false, err
	}
	f, err := os.Open(abs)
	if os.IsNotExist(err) {
		return DriverExtractResult{}, false, nil
	}
	if err != nil {
		return DriverExtractResult{}, false, err
	}
	defer f.Close()
	var res DriverExtractResult
	if err := json.NewDecoder(f).Decode(&res); err != nil {
		return DriverExtractResult{}, false, err
	}
	return res, true, nil
}

// extractDriverZip unpacks srcZip into destDir, refusing zip-slip
// paths and capping the total size to limit damage from a malicious
// upload (in this codebase, only authenticated operators can upload
// in the first place, but defence in depth is cheap).
func extractDriverZip(srcZip, destDir string) (DriverExtractResult, error) {
	const maxTotalBytes = int64(8 << 30) // 8 GiB
	zr, err := zip.OpenReader(srcZip)
	if err != nil {
		return DriverExtractResult{}, fmt.Errorf("not a zip: %w", err)
	}
	defer zr.Close()
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return DriverExtractResult{}, err
	}
	var total int64
	var fileCount int
	var infs []INFEntry
	for _, zf := range zr.File {
		// Block path traversal.
		rel := filepath.ToSlash(zf.Name)
		if strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
			return DriverExtractResult{}, fmt.Errorf("refusing suspect path %q", rel)
		}
		target := filepath.Join(absDest, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, absDest+string(os.PathSeparator)) && target != absDest {
			return DriverExtractResult{}, fmt.Errorf("zip-slip path %q", rel)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return DriverExtractResult{}, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return DriverExtractResult{}, err
		}
		rc, err := zf.Open()
		if err != nil {
			return DriverExtractResult{}, err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return DriverExtractResult{}, err
		}
		n, err := io.Copy(out, io.LimitReader(rc, maxTotalBytes-total+1))
		rc.Close()
		_ = out.Close()
		if err != nil {
			return DriverExtractResult{}, err
		}
		total += n
		fileCount++
		if total > maxTotalBytes {
			return DriverExtractResult{}, fmt.Errorf("extraction exceeded %d bytes", maxTotalBytes)
		}
		if strings.EqualFold(filepath.Ext(rel), ".inf") {
			meta, _ := parseINF(target)
			meta.Path = rel
			infs = append(infs, meta)
		}
	}
	return DriverExtractResult{
		Bytes:     total,
		FileCount: fileCount,
		INFCount:  len(infs),
		INFs:      infs,
	}, nil
}

// parseINF reads the minimal [Version] section keys we care about
// from a Windows .inf file. INF files are simple key=value text with
// section headers in [brackets]; values may be quoted, may use
// continuation lines, and the encoding is sometimes UTF-16 LE.
// We handle ASCII and UTF-16 LE (the two encodings Microsoft tooling
// emits) and ignore everything else gracefully -- failure here is
// non-fatal because the .inf is still installed even without the
// metadata.
func parseINF(path string) (INFEntry, error) {
	var out INFEntry
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	// Sniff for UTF-16 LE BOM (FF FE).
	br := bufio.NewReader(f)
	bom, _ := br.Peek(2)
	utf16le := len(bom) >= 2 && bom[0] == 0xFF && bom[1] == 0xFE
	var lines []string
	if utf16le {
		raw, _ := io.ReadAll(br)
		// Strip BOM, decode pairs.
		if len(raw) >= 2 {
			raw = raw[2:]
		}
		var sb strings.Builder
		for i := 0; i+1 < len(raw); i += 2 {
			r := uint16(raw[i]) | uint16(raw[i+1])<<8
			if r == '\r' {
				continue
			}
			if r == '\n' {
				lines = append(lines, sb.String())
				sb.Reset()
				continue
			}
			sb.WriteRune(rune(r))
		}
		if sb.Len() > 0 {
			lines = append(lines, sb.String())
		}
	} else {
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
	}
	inVersion := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, ";") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			inVersion = strings.EqualFold(t, "[Version]")
			continue
		}
		if !inVersion {
			continue
		}
		k, v := splitKV(t)
		v = strings.Trim(v, `"`)
		switch strings.ToLower(k) {
		case "class":
			out.Class = v
		case "provider":
			out.Provider = v
		case "driverver":
			// Format: "MM/DD/YYYY,version"
			if comma := strings.Index(v, ","); comma >= 0 {
				out.Date = strings.TrimSpace(v[:comma])
				out.Version = strings.TrimSpace(v[comma+1:])
			} else {
				out.Version = v
			}
		}
	}
	return out, nil
}

func splitKV(s string) (k, v string) {
	if eq := strings.Index(s, "="); eq >= 0 {
		return strings.TrimSpace(s[:eq]), strings.TrimSpace(s[eq+1:])
	}
	return s, ""
}
