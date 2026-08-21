package payload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Importing an app from winget means downloading one or more real vendor
// installers, which can run to hundreds of megabytes. As with catalog
// update imports (update_import.go), the portal kicks the download off in
// the background and polls SoftwareImportStatusFor to drive a progress
// bar. This tracker is separate from the update importer's so software
// and update IDs (both model.ID) can't collide.

// SoftwareImportPhase is the coarse stage of a winget software import.
type SoftwareImportPhase string

const (
	SoftwareImportIdle        SoftwareImportPhase = "idle"
	SoftwareImportResolving   SoftwareImportPhase = "resolving"
	SoftwareImportDownloading SoftwareImportPhase = "downloading"
	SoftwareImportDone        SoftwareImportPhase = "done"
	SoftwareImportError       SoftwareImportPhase = "error"
)

// SoftwareImportStatus is a snapshot of one package's background import,
// suitable for JSON polling by the portal. DoneBytes/TotalBytes aggregate
// across all files; FileIndex/FileCount let the UI say "file 2 of 3".
type SoftwareImportStatus struct {
	Phase      SoftwareImportPhase `json:"phase"`
	Filename   string              `json:"filename,omitempty"`
	FileIndex  int                 `json:"file_index"`
	FileCount  int                 `json:"file_count"`
	DoneBytes  int64               `json:"done_bytes"`
	TotalBytes int64               `json:"total_bytes"`
	Percent    int                 `json:"percent"`
	Error      string              `json:"error,omitempty"`
	Finished   bool                `json:"finished"`
}

// Active reports whether an import is currently running.
func (s SoftwareImportStatus) Active() bool {
	return s.Phase == SoftwareImportResolving || s.Phase == SoftwareImportDownloading
}

// SoftwareDownloadFile is one installer the importer must fetch. It
// mirrors wingetsrc.DownloadFile but is redeclared here so the payload
// package doesn't import wingetsrc (keeping the dependency one-way).
type SoftwareDownloadFile struct {
	URL      string
	Filename string
	SHA256   string
}

type softwareImportTracker struct {
	mu sync.Mutex
	m  map[model.ID]*SoftwareImportStatus
}

var softwareImportJobs = &softwareImportTracker{m: map[model.ID]*SoftwareImportStatus{}}

func (t *softwareImportTracker) snapshot(id model.ID) SoftwareImportStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.m[id]; ok {
		return *s
	}
	return SoftwareImportStatus{Phase: SoftwareImportIdle}
}

// begin marks an import running iff one isn't already; finished entries
// for other IDs are evicted to bound the map.
func (t *softwareImportTracker) begin(id model.ID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.m[id]; ok && s.Active() {
		return false
	}
	for k, v := range t.m {
		if v.Finished && k != id {
			delete(t.m, k)
		}
	}
	t.m[id] = &SoftwareImportStatus{Phase: SoftwareImportResolving}
	return true
}

func (t *softwareImportTracker) update(id model.ID, mutate func(*SoftwareImportStatus)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.m[id]
	if !ok {
		s = &SoftwareImportStatus{}
		t.m[id] = s
	}
	mutate(s)
}

// SoftwareImportStatusFor returns the current background-import status for
// a software package.
func SoftwareImportStatusFor(id model.ID) SoftwareImportStatus {
	return softwareImportJobs.snapshot(id)
}

// softwareImportHTTPClient downloads installer payloads. Generous timeout:
// some installers are large. Overridable in tests.
var softwareImportHTTPClient = &http.Client{Timeout: 30 * time.Minute}

// softwareCountingReader feeds download progress into the tracker as the
// blob store consumes the body, and hashes the bytes for SHA256
// verification. base is the DoneBytes total from already-completed files
// so the aggregate percentage advances across a multi-file import.
type softwareCountingReader struct {
	r    io.Reader
	id   model.ID
	base int64
	n    int64
	h    hash.Hash
}

func (c *softwareCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		if c.h != nil {
			c.h.Write(p[:n])
		}
		done := c.base + c.n
		softwareImportJobs.update(c.id, func(s *SoftwareImportStatus) {
			s.DoneBytes = done
			if s.TotalBytes > 0 {
				pct := int(done * 100 / s.TotalBytes)
				if pct > 99 {
					pct = 99 // reserve 100 for "finished"
				}
				s.Percent = pct
			}
		})
	}
	return n, err
}

// softwarePackageFileRel returns the blob-relative path a package's
// uploaded file lives at. Kept in lock-step with
// portal.softwarePackageFilesDir (software/<id>/files/<name>) so an
// imported file is indistinguishable from a manually uploaded one.
func softwarePackageFileRel(id model.ID, filename string) string {
	return path.Join("software", fmt.Sprint(int64(id)), "files", filename)
}

// StartSoftwareImportAsync downloads each installer in files into the
// package's files/ directory (software/<id>/files/<filename>), the same
// place a manual multi-file upload writes to, so the resulting package is
// a standard one. Returns immediately; progress is observable via
// SoftwareImportStatusFor(id). A no-op if an import for id is already
// running. When a file carries a SHA256 it is verified during download
// and a mismatch fails the whole import.
func StartSoftwareImportAsync(blobs *storage.BlobStore, id model.ID, files []SoftwareDownloadFile) {
	if !softwareImportJobs.begin(id) {
		return // already running
	}
	fail := func(msg string) {
		softwareImportJobs.update(id, func(s *SoftwareImportStatus) {
			s.Phase, s.Error, s.Finished = SoftwareImportError, msg, true
		})
	}
	if len(files) == 0 {
		fail("nothing to download")
		return
	}

	softwareImportJobs.update(id, func(s *SoftwareImportStatus) {
		s.FileCount = len(files)
	})

	go func() {
		ctx := context.Background()
		var completedBytes int64

		for i, f := range files {
			softwareImportJobs.update(id, func(s *SoftwareImportStatus) {
				s.Phase = SoftwareImportDownloading
				s.Filename = f.Filename
				s.FileIndex = i + 1
			})

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
			if err != nil {
				fail(err.Error())
				return
			}
			req.Header.Set("User-Agent", "autodeploy-server")
			resp, err := softwareImportHTTPClient.Do(req)
			if err != nil {
				fail("download " + f.Filename + ": " + err.Error())
				return
			}
			if resp.StatusCode != http.StatusOK {
				status := resp.Status
				_ = resp.Body.Close()
				fail("download " + f.Filename + ": unexpected status " + status)
				return
			}
			if resp.ContentLength > 0 {
				add := resp.ContentLength
				softwareImportJobs.update(id, func(s *SoftwareImportStatus) {
					// TotalBytes grows as each file's length is learned; the
					// prior files' totals are already reflected in completedBytes.
					s.TotalBytes = completedBytes + add
				})
			}

			var h hash.Hash
			if f.SHA256 != "" {
				h = sha256.New()
			}
			rel := softwarePackageFileRel(id, f.Filename)
			cr := &softwareCountingReader{r: resp.Body, id: id, base: completedBytes, h: h}
			n, werr := blobs.WriteStream(rel, cr)
			_ = resp.Body.Close()
			if werr != nil {
				fail("store " + f.Filename + ": " + werr.Error())
				return
			}
			if h != nil {
				got := hex.EncodeToString(h.Sum(nil))
				if !strings.EqualFold(got, f.SHA256) {
					_ = blobs.Remove(rel)
					fail(fmt.Sprintf("checksum mismatch for %s: expected %s, got %s",
						f.Filename, f.SHA256, got))
					return
				}
			}
			completedBytes += n
			softwareImportJobs.update(id, func(s *SoftwareImportStatus) {
				if s.TotalBytes < completedBytes {
					s.TotalBytes = completedBytes
				}
			})
		}

		softwareImportJobs.update(id, func(s *SoftwareImportStatus) {
			s.Phase, s.Percent, s.DoneBytes, s.Finished = SoftwareImportDone, 100, completedBytes, true
			if s.TotalBytes == 0 {
				s.TotalBytes = completedBytes
			}
			s.Filename = ""
		})
	}()
}
