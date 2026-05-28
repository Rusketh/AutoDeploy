package steps

import (
	"context"
	"fmt"
	"strings"
)

// Recorder is a Runner that captures every call. Tests assert against
// recorder.Calls and recorder.Copies.
type Recorder struct {
	Calls    []string
	Copies   []string
	ExitMap  map[string]int   // name -> exit code (default 0)
	ErrorMap map[string]error // name -> error
}

func (r *Recorder) Run(_ context.Context, name string, args []string, stdin string) (int, error) {
	r.Calls = append(r.Calls, name+" "+strings.Join(args, " "))
	if r.ErrorMap != nil {
		if err, ok := r.ErrorMap[name]; ok {
			return -1, err
		}
	}
	if r.ExitMap != nil {
		if c, ok := r.ExitMap[name]; ok {
			return c, nil
		}
	}
	return 0, nil
}

func (r *Recorder) Copy(_ context.Context, src, dst string) error {
	r.Copies = append(r.Copies, fmt.Sprintf("%s -> %s", src, dst))
	return nil
}

// Dump prints calls for failing-test diagnostics.
func (r *Recorder) Dump() string {
	return fmt.Sprintf("calls:\n  - %s\ncopies:\n  - %s",
		strings.Join(r.Calls, "\n  - "),
		strings.Join(r.Copies, "\n  - "))
}
