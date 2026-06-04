package steps

import (
	"context"
	"fmt"
	"strings"
)

// Recorder is a Runner that captures every call. Tests assert against
// recorder.Calls / .Copies / .Unzips.
type Recorder struct {
	Calls    []string
	Copies   []string
	Unzips   []string
	ExitMap  map[string]int   // name -> exit code (default 0)
	ErrorMap map[string]error // name -> error
	// UnzipError, if non-nil, is returned by Unzip. Used to assert
	// abort-on-error behaviour for unzip steps.
	UnzipError error
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

func (r *Recorder) RunScript(_ context.Context, shell, body string) (int, error) {
	r.Calls = append(r.Calls, shell+" (script) "+body)
	if r.ErrorMap != nil {
		if err, ok := r.ErrorMap[shell]; ok {
			return -1, err
		}
	}
	if r.ExitMap != nil {
		if c, ok := r.ExitMap[shell]; ok {
			return c, nil
		}
	}
	return 0, nil
}

func (r *Recorder) Copy(_ context.Context, src, dst string) error {
	r.Copies = append(r.Copies, fmt.Sprintf("%s -> %s", src, dst))
	return nil
}

func (r *Recorder) Unzip(_ context.Context, src, dst string) error {
	r.Unzips = append(r.Unzips, fmt.Sprintf("%s -> %s", src, dst))
	return r.UnzipError
}

// Dump prints calls for failing-test diagnostics.
func (r *Recorder) Dump() string {
	return fmt.Sprintf("calls:\n  - %s\ncopies:\n  - %s\nunzips:\n  - %s",
		strings.Join(r.Calls, "\n  - "),
		strings.Join(r.Copies, "\n  - "),
		strings.Join(r.Unzips, "\n  - "))
}
