package payload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Authoring a bootable ISO from a multi-GB Windows media tree takes minutes
// (xorriso streams the whole tree into the image), so the portal kicks it off
// in the background and polls ExportStatusFor to drive a progress bar — the
// same shape as the ISO-prep async job.

// ExportPhase is the coarse stage of a background image export.
type ExportPhase string

const (
	ExportIdle    ExportPhase = "idle"
	ExportRunning ExportPhase = "running"
	ExportDone    ExportPhase = "done"
	ExportError   ExportPhase = "error"
)

// ExportStatus is a snapshot of an image's background ISO export, for JSON
// polling by the portal.
type ExportStatus struct {
	Phase    ExportPhase `json:"phase"`
	Stage    string      `json:"stage,omitempty"`
	Percent  int         `json:"percent"`
	Error    string      `json:"error,omitempty"`
	Finished bool        `json:"finished"`
	// OutputPath is the absolute path of the finished ISO (server-internal;
	// not serialised). SizeBytes is its size, shown in the portal.
	OutputPath string `json:"-"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
}

// Active reports whether an export is currently running.
func (s ExportStatus) Active() bool { return s.Phase == ExportRunning }

type exportTracker struct {
	mu sync.Mutex
	m  map[model.ID]*ExportStatus
}

var exportJobs = &exportTracker{m: map[model.ID]*ExportStatus{}}

func (t *exportTracker) snapshot(id model.ID) ExportStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.m[id]; ok {
		return *s
	}
	return ExportStatus{Phase: ExportIdle}
}

// begin marks an export running iff one isn't already in flight for id. It
// evicts other finished entries so the map stays bounded.
func (t *exportTracker) begin(id model.ID) bool {
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
	t.m[id] = &ExportStatus{Phase: ExportRunning, Stage: "Starting"}
	return true
}

func (t *exportTracker) update(id model.ID, mutate func(*ExportStatus)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.m[id]
	if !ok {
		s = &ExportStatus{}
		t.m[id] = s
	}
	mutate(s)
}

// ExportStatusFor returns the current background-export status for an image.
func ExportStatusFor(id model.ID) ExportStatus { return exportJobs.snapshot(id) }

// ExportOutputPath is the stable location an image's exported ISO is written
// to (one per image; a re-export overwrites it). Callers use it both to write
// the ISO and to stream it back for download.
func ExportOutputPath(blobs *storage.BlobStore, imageID model.ID) (string, error) {
	dir, err := blobs.EnsureDir(filepath.ToSlash(filepath.Join("export", fmt.Sprint(int64(imageID)))))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("autodeploy-image-%d.iso", int64(imageID))), nil
}

// StartExportAsync authors the ISO for opts.ImageID in the background and
// returns immediately; progress is observable via ExportStatusFor(ImageID).
// Returns false (a no-op) if an export for that image is already running.
// Errors are reported through the status, not returned.
//
// The caller supplies deps and a fully-formed opts (BaseURL, OutPath,
// AgentBinaryPath, VolumeLabel); this owns the scratch WorkDir lifecycle and
// records the finished ISO's size on success.
func StartExportAsync(deps ISOExportDeps, opts ISOExportOptions) bool {
	id := opts.ImageID
	if !exportJobs.begin(id) {
		return false
	}
	go func() {
		work, err := os.MkdirTemp("", fmt.Sprintf("adexport-%d-*", int64(id)))
		if err != nil {
			exportJobs.update(id, func(s *ExportStatus) {
				s.Phase, s.Error, s.Finished = ExportError, "scratch dir: "+err.Error(), true
			})
			return
		}
		defer os.RemoveAll(work)
		opts.WorkDir = work
		opts.Progress = func(stage string, pct int) {
			exportJobs.update(id, func(s *ExportStatus) {
				s.Stage, s.Percent = stage, pct
			})
		}
		err = deps.ExportImageISO(context.Background(), opts)
		exportJobs.update(id, func(s *ExportStatus) {
			s.Finished = true
			if err != nil {
				s.Phase, s.Error, s.Percent = ExportError, err.Error(), 0
				return
			}
			s.Phase, s.Percent, s.Stage = ExportDone, 100, "Done"
			s.OutputPath = opts.OutPath
			if fi, statErr := os.Stat(opts.OutPath); statErr == nil {
				s.SizeBytes = fi.Size()
			}
		})
	}()
	return true
}
