package payload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Authoring a bootable ISO from a multi-GB Windows media tree takes minutes
// (xorriso streams the whole tree into the image), so the portal kicks it off
// in the background and polls ExportStatusFor to drive a progress bar — the
// same shape as the ISO-prep async job.
//
// The ISO is written to "<final>.partial" and renamed into place only once
// xorriso succeeded and the result looks like an ISO, so the download endpoint
// (which serves only the final path) can never hand out a half-written or
// failed image. A failed, timed-out or cancelled export removes its partial
// file; a previous good ISO at the final path is left untouched until a new
// one replaces it atomically.

// ExportTimeout bounds one export. xorriso over a ~6 GB tree normally takes a
// few minutes; the generous ceiling only exists so a wedged build (a stuck
// disk, a hung NFS-backed data dir) eventually frees the job slot.
var ExportTimeout = 2 * time.Hour

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
	// Warnings are non-fatal problems found while building (e.g. the media
	// only covers one firmware type).
	Warnings []string `json:"warnings,omitempty"`
	// BuiltAt is when the finished ISO was written (its mtime).
	BuiltAt *time.Time `json:"built_at,omitempty"`

	cancel context.CancelFunc
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
		out := *s
		out.cancel = nil
		out.Warnings = append([]string(nil), s.Warnings...)
		return out
	}
	return ExportStatus{Phase: ExportIdle}
}

// begin marks an export running iff one isn't already in flight for id. It
// evicts other finished entries so the map stays bounded.
func (t *exportTracker) begin(id model.ID, cancel context.CancelFunc) bool {
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
	t.m[id] = &ExportStatus{Phase: ExportRunning, Stage: "Starting", cancel: cancel}
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

// CancelExport stops a running export for the image. It reports whether one
// was running; the job then finishes with an "Export cancelled" error and its
// partial file is removed.
func CancelExport(id model.ID) bool {
	exportJobs.mu.Lock()
	defer exportJobs.mu.Unlock()
	s, ok := exportJobs.m[id]
	if !ok || !s.Active() || s.cancel == nil {
		return false
	}
	s.cancel()
	s.Stage = "Cancelling"
	return true
}

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

// partialSuffix marks an export still being written.
const partialSuffix = ".partial"

// iso9660Magic is the Primary Volume Descriptor signature: byte 1..5 of
// sector 16 (offset 0x8001) of every ISO9660 image.
var iso9660Magic = []byte("CD001")

// FinishedExport reports whether path holds a complete-looking exported ISO:
// a regular, non-empty file carrying the ISO9660 volume descriptor. It is the
// download endpoint's gate, and also accepts ISOs a previous server version
// wrote straight to the final path.
func FinishedExport(path string) (os.FileInfo, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 0x8006 {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, len(iso9660Magic))
	if _, err := f.ReadAt(buf, 0x8001); err != nil && !errors.Is(err, io.EOF) {
		return nil, false
	}
	if !bytes.Equal(buf, iso9660Magic) {
		return nil, false
	}
	return fi, true
}

// StartExportAsync authors the ISO for opts.ImageID in the background and
// returns immediately; progress is observable via ExportStatusFor(ImageID).
// Returns false (a no-op) if an export for that image is already running.
// Errors are reported through the status, not returned.
//
// The caller supplies deps and a fully-formed opts (BaseURL, OutPath,
// AgentBinaryPath, VolumeLabel); this owns the scratch WorkDir lifecycle,
// writes via OutPath+".partial" and renames on success, and records the
// finished ISO's size.
func StartExportAsync(deps ISOExportDeps, opts ISOExportOptions) bool {
	id := opts.ImageID
	ctx, cancel := context.WithTimeout(context.Background(), ExportTimeout)
	if !exportJobs.begin(id, cancel) {
		cancel()
		return false
	}
	final := opts.OutPath
	partial := final + partialSuffix
	go func() {
		defer cancel()
		fail := func(msg string) {
			_ = os.Remove(partial)
			exportJobs.update(id, func(s *ExportStatus) {
				s.Phase, s.Error, s.Percent, s.Finished, s.cancel = ExportError, msg, 0, true, nil
			})
		}
		work, err := os.MkdirTemp("", fmt.Sprintf("adexport-%d-*", int64(id)))
		if err != nil {
			fail("scratch dir: " + err.Error())
			return
		}
		defer os.RemoveAll(work)
		// A leftover partial from a crashed or interrupted run would make
		// xorriso refuse or append; start clean.
		_ = os.Remove(partial)
		opts.WorkDir = work
		opts.OutPath = partial
		opts.Progress = func(stage string, pct int) {
			exportJobs.update(id, func(s *ExportStatus) {
				if s.Stage != "Cancelling" {
					s.Stage, s.Percent = stage, pct
				}
			})
		}
		opts.Warn = func(msg string) {
			exportJobs.update(id, func(s *ExportStatus) { s.Warnings = append(s.Warnings, msg) })
		}
		err = deps.ExportImageISO(ctx, opts)
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			fail("Export cancelled.")
			return
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			fail(fmt.Sprintf("Export timed out after %s and was stopped.", ExportTimeout))
			return
		case err != nil:
			fail(err.Error())
			return
		}
		fi, ok := FinishedExport(partial)
		if !ok {
			fail("iso export: the builder finished but wrote no valid ISO image")
			return
		}
		if err := os.Rename(partial, final); err != nil {
			fail("iso export: move finished ISO into place: " + err.Error())
			return
		}
		built := fi.ModTime()
		exportJobs.update(id, func(s *ExportStatus) {
			s.Finished, s.cancel = true, nil
			s.Phase, s.Percent, s.Stage = ExportDone, 100, "Done"
			s.OutputPath = final
			s.SizeBytes = fi.Size()
			s.BuiltAt = &built
		})
	}()
	return true
}
