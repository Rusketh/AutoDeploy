package payload

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Importing an update from the Microsoft Update Catalog means downloading
// a payload that can run past a gigabyte. As with ISO prepares
// (prep_async.go), the portal kicks the download off in the background
// and polls ImportStatusFor to drive a progress bar.

// ImportPhase is the coarse stage of a catalog import.
type ImportPhase string

const (
	ImportIdle        ImportPhase = "idle"
	ImportResolving   ImportPhase = "resolving" // DownloadDialog round-trip
	ImportDownloading ImportPhase = "downloading"
	ImportDone        ImportPhase = "done"
	ImportError       ImportPhase = "error"
)

// ImportStatus is a snapshot of one update's background import, suitable
// for JSON polling by the portal.
type ImportStatus struct {
	Phase      ImportPhase `json:"phase"`
	Filename   string      `json:"filename,omitempty"`
	DoneBytes  int64       `json:"done_bytes"`
	TotalBytes int64       `json:"total_bytes"`
	Percent    int         `json:"percent"`
	Error      string      `json:"error,omitempty"`
	Finished   bool        `json:"finished"`
}

// Active reports whether an import is currently running.
func (s ImportStatus) Active() bool {
	return s.Phase == ImportResolving || s.Phase == ImportDownloading
}

type importTracker struct {
	mu sync.Mutex
	m  map[model.ID]*ImportStatus
}

var importJobs = &importTracker{m: map[model.ID]*ImportStatus{}}

func (t *importTracker) snapshot(id model.ID) ImportStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.m[id]; ok {
		return *s
	}
	return ImportStatus{Phase: ImportIdle}
}

// begin marks an import running iff one isn't already; finished entries
// for other IDs are evicted to bound the map.
func (t *importTracker) begin(id model.ID) bool {
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
	t.m[id] = &ImportStatus{Phase: ImportResolving}
	return true
}

func (t *importTracker) update(id model.ID, mutate func(*ImportStatus)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.m[id]
	if !ok {
		s = &ImportStatus{}
		t.m[id] = s
	}
	mutate(s)
}

// ImportStatusFor returns the current background-import status for an
// update.
func ImportStatusFor(id model.ID) ImportStatus { return importJobs.snapshot(id) }

// importHTTPClient downloads update payloads. Generous timeout: catalog
// payloads regularly exceed a gigabyte. Overridable in tests.
var importHTTPClient = &http.Client{Timeout: 30 * time.Minute}

// countingReader feeds download progress into the tracker as the blob
// store consumes the body.
type countingReader struct {
	r  io.Reader
	id model.ID
	n  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		done := c.n
		importJobs.update(c.id, func(s *ImportStatus) {
			s.DoneBytes = done
			if s.TotalBytes > 0 {
				p := int(done * 100 / s.TotalBytes)
				if p > 99 {
					p = 99 // reserve 100 for "finished"
				}
				s.Percent = p
			}
		})
	}
	return n, err
}

// importFilename derives a safe on-disk name from a download URL,
// falling back to update.msu.
func importFilename(fileURL string) string {
	u, err := url.Parse(fileURL)
	if err != nil {
		return "update.msu"
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" ||
		strings.ContainsAny(name, `\`) || strings.Contains(name, "..") {
		return "update.msu"
	}
	return name
}

// StartUpdateImportAsync resolves an update's download URL and streams the
// payload into the blob store as updates/<id>/<filename>, recording it via
// SetPayload — the same end state as a manual browser upload. Returns
// immediately; progress is observable via ImportStatusFor(id). A no-op if
// an import for id is already running. linkFn performs the catalog
// download-link resolution (injected so tests stay off the network);
// pickFn chooses among multiple file URLs.
func StartUpdateImportAsync(blobs *storage.BlobStore, updates *model.WindowsUpdateRepo,
	id model.ID,
	linkFn func(ctx context.Context) ([]string, error),
	pickFn func([]string) string) {
	if !importJobs.begin(id) {
		return // already running
	}
	fail := func(msg string) {
		importJobs.update(id, func(s *ImportStatus) {
			s.Phase, s.Error, s.Finished = ImportError, msg, true
		})
	}

	go func() {
		ctx := context.Background()

		links, err := linkFn(ctx)
		if err != nil {
			fail(err.Error())
			return
		}
		fileURL := pickFn(links)
		if fileURL == "" {
			fail("no downloadable file for this update")
			return
		}
		filename := importFilename(fileURL)
		importJobs.update(id, func(s *ImportStatus) {
			s.Filename = filename
		})

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
		if err != nil {
			fail(err.Error())
			return
		}
		req.Header.Set("User-Agent", "autodeploy-server")
		resp, err := importHTTPClient.Do(req)
		if err != nil {
			fail("download: " + err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fail("download: unexpected status " + resp.Status)
			return
		}
		importJobs.update(id, func(s *ImportStatus) {
			s.Phase = ImportDownloading
			if resp.ContentLength > 0 {
				s.TotalBytes = resp.ContentLength
			}
		})

		// Same layout and bookkeeping as a manual upload (serve.go
		// uploadUpdate): updates/<id>/<filename> + SetPayload.
		rel := fmt.Sprintf("updates/%d/%s", int64(id), filename)
		n, err := blobs.WriteStream(rel, &countingReader{r: resp.Body, id: id})
		if err != nil {
			fail("store: " + err.Error())
			return
		}
		if err := updates.SetPayload(ctx, id, rel, filename, n); err != nil {
			fail("record payload: " + err.Error())
			return
		}
		importJobs.update(id, func(s *ImportStatus) {
			s.Phase, s.Percent, s.DoneBytes, s.Finished = ImportDone, 100, n, true
			if s.TotalBytes == 0 {
				s.TotalBytes = n
			}
		})
	}()
}
