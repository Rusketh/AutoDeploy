package payload

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Extracting a real Windows (UDF) ISO with 7z and splitting a multi-GB
// install.wim into .swm parts takes minutes. Doing that inside the upload
// or Re-prepare request blocks the browser with no feedback (and risks
// proxy timeouts). The portal instead kicks the work off in the
// background and polls PrepareStatus to drive a progress bar.

// PrepPhase is the coarse stage of an ISO prepare.
type PrepPhase string

const (
	PrepIdle       PrepPhase = "idle"
	PrepExtracting PrepPhase = "extracting"
	PrepSplitting  PrepPhase = "splitting"
	PrepDone       PrepPhase = "done"
	PrepError      PrepPhase = "error"
)

// PrepStatus is a snapshot of an ISO's background prepare, suitable for
// JSON polling by the portal.
type PrepStatus struct {
	Phase      PrepPhase `json:"phase"`
	DoneBytes  int64     `json:"done_bytes"`
	TotalBytes int64     `json:"total_bytes"`
	Percent    int       `json:"percent"`
	Error      string    `json:"error,omitempty"`
	Finished   bool      `json:"finished"`
	SWMParts   int       `json:"swm_parts,omitempty"`
}

// Active reports whether a prepare is currently running.
func (s PrepStatus) Active() bool { return s.Phase == PrepExtracting || s.Phase == PrepSplitting }

type prepTracker struct {
	mu sync.Mutex
	m  map[model.ID]*PrepStatus
}

var prepJobs = &prepTracker{m: map[model.ID]*PrepStatus{}}

func (t *prepTracker) snapshot(id model.ID) PrepStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.m[id]; ok {
		return *s
	}
	return PrepStatus{Phase: PrepIdle}
}

// begin marks a job running iff one isn't already; returns false if a
// prepare for id is already in flight. Finished entries for other IDs
// are evicted to prevent the map from growing without bound.
func (t *prepTracker) begin(id model.ID, total int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.m[id]; ok && s.Active() {
		return false
	}
	// Evict finished entries to bound memory.
	for k, v := range t.m {
		if v.Finished && k != id {
			delete(t.m, k)
		}
	}
	t.m[id] = &PrepStatus{Phase: PrepExtracting, TotalBytes: total}
	return true
}

func (t *prepTracker) update(id model.ID, mutate func(*PrepStatus)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.m[id]
	if !ok {
		s = &PrepStatus{}
		t.m[id] = s
	}
	mutate(s)
}

// PrepareStatus returns the current background-prepare status for an ISO.
func PrepareStatus(id model.ID) PrepStatus { return prepJobs.snapshot(id) }

// StartPrepareAsync extracts and prepares an ISO in the background and
// returns immediately. Progress is observable via PrepareStatus(id). If a
// prepare for id is already running, it is a no-op. Errors are reported
// through the status, not returned.
func StartPrepareAsync(blobs *storage.BlobStore, isos *model.ISORepo, id model.ID) {
	srcAbs, destAbs, err := prepPaths(blobs, id)
	total := int64(0)
	if err == nil {
		if fi, statErr := os.Stat(srcAbs); statErr == nil {
			total = fi.Size()
		}
	}
	if !prepJobs.begin(id, total) {
		return // already running
	}
	if err != nil {
		prepJobs.update(id, func(s *PrepStatus) {
			s.Phase, s.Error, s.Finished = PrepError, err.Error(), true
		})
		return
	}

	go func() {
		ctx := context.Background()
		// Monitor the growing files/ tree as a progress proxy: 7z gives no
		// machine-readable progress, but bytes-on-disk vs the ISO size is a
		// good estimate.
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(750 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					done := treeBytes(destAbs)
					prepJobs.update(id, func(s *PrepStatus) {
						if s.Phase != PrepExtracting {
							return
						}
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
			}
		}()

		_, _, extractErr := ExtractISO(srcAbs, destAbs)
		close(stop)
		if extractErr != nil {
			prepJobs.update(id, func(s *PrepStatus) {
				s.Phase, s.Error, s.Finished = PrepError, "extract: "+extractErr.Error(), true
			})
			return
		}

		prepJobs.update(id, func(s *PrepStatus) {
			s.Phase, s.Percent, s.DoneBytes = PrepSplitting, 99, s.TotalBytes
		})
		prep, recErr := applyPrep(ctx, isos, id, destAbs)
		prepJobs.update(id, func(s *PrepStatus) {
			s.Finished, s.Percent, s.SWMParts = true, 100, prep.SWMParts
			switch {
			case recErr != nil:
				s.Phase, s.Error = PrepError, recErr.Error()
			case prep.Err != "":
				s.Phase, s.Error = PrepError, prep.Err
			default:
				s.Phase = PrepDone
			}
		})
	}()
}
