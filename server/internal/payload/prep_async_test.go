package payload

import (
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func TestPrepTrackerStateMachine(t *testing.T) {
	const id model.ID = 999999

	// Idle by default.
	if s := PrepareStatus(id); s.Phase != PrepIdle {
		t.Fatalf("fresh id phase = %q, want idle", s.Phase)
	}

	// begin marks it extracting and blocks a second begin while active.
	if !prepJobs.begin(id, 1000) {
		t.Fatal("first begin should succeed")
	}
	if prepJobs.begin(id, 1000) {
		t.Error("second begin while active should be rejected")
	}
	if s := PrepareStatus(id); s.Phase != PrepExtracting || s.TotalBytes != 1000 || !s.Active() {
		t.Errorf("after begin: %+v", s)
	}

	// Drive it to done; begin should be allowed again afterwards.
	prepJobs.update(id, func(s *PrepStatus) {
		s.Phase, s.Finished, s.Percent, s.SWMParts = PrepDone, true, 100, 2
	})
	if s := PrepareStatus(id); !s.Finished || s.Active() || s.SWMParts != 2 {
		t.Errorf("after done: %+v", s)
	}
	if !prepJobs.begin(id, 2000) {
		t.Error("begin after finish should succeed")
	}
	// Leave it finished so the singleton map isn't left active for other tests.
	prepJobs.update(id, func(s *PrepStatus) { s.Phase, s.Finished = PrepDone, true })
}
