package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/httpc"
	"github.com/rusketh/autodeploy/agent/internal/runas"
	"github.com/rusketh/autodeploy/agent/internal/steps"
	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// fakeSession records what a Tool Kit asks of its Run As account.
type fakeSession struct {
	mu       sync.Mutex
	launched []string
	granted  []string
	closed   bool
	// exit maps a substring of the launched command line to an exit code.
	exit  map[string]int
	block bool // Launch waits for ctx (timeout tests)
}

func (s *fakeSession) Launch(ctx context.Context, name string, args []string, dir, stdin string, stdout, stderr io.Writer) (int, error) {
	line := name + " " + strings.Join(args, " ")
	s.mu.Lock()
	s.launched = append(s.launched, line)
	s.mu.Unlock()
	if s.block {
		<-ctx.Done()
		return 1, ctx.Err()
	}
	// A script step runs its body from a file: echo the file so the test can
	// see the parameter-expanded body came through.
	for _, a := range args {
		if strings.HasSuffix(a, ".ps1") || strings.HasSuffix(a, ".cmd") {
			if b, err := os.ReadFile(a); err == nil {
				_, _ = stdout.Write(b)
			}
		}
	}
	_, _ = io.WriteString(stderr, "\nwarning from "+name)
	for k, code := range s.exit {
		if strings.Contains(line, k) {
			return code, nil
		}
	}
	return 0, nil
}

func (s *fakeSession) Expand(str string) string {
	return strings.ReplaceAll(str, "%APPDATA%", `C:\Users\ops\AppData\Roaming`)
}
func (s *fakeSession) GrantDir(dir string) error { s.granted = append(s.granted, dir); return nil }
func (s *fakeSession) Close()                    { s.closed = true }

type toolkitServer struct {
	srv          *httptest.Server
	def          toolkitJobDef
	credCalls    int
	credStatus   int
	resultFiles  map[string]string
	lastAgentReq map[string]any
}

func newToolkitServer(t *testing.T, def toolkitJobDef) *toolkitServer {
	t.Helper()
	ts := &toolkitServer{def: def, credStatus: http.StatusOK, resultFiles: map[string]string{}}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/toolkit-job":
			_ = json.NewDecoder(r.Body).Decode(&ts.lastAgentReq)
			_ = json.NewEncoder(w).Encode(ts.def)
		case r.URL.Path == "/api/v1/agent/job-credential":
			ts.credCalls++
			if ts.credStatus != http.StatusOK {
				http.Error(w, "no password is stored", ts.credStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"user": ts.def.RunAsUser, "password": "agent-test-pw"})
		case strings.HasPrefix(r.URL.Path, "/payload/toolkit/"):
			if r.Header.Get("X-AutoDeploy-Agent-ID") == "" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/files/")+len("/files/"):]
			_, _ = io.WriteString(w, ts.resultFiles[name])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func withFakeRunAs(t *testing.T, sess *fakeSession, wantPassword string) *[]string {
	t.Helper()
	var calls []string
	saved, savedReboot := openRunAs, rebootScheduler
	openRunAs = func(mode, account, password string) (runAsSession, string, error) {
		calls = append(calls, mode+"|"+account)
		if wantPassword != "" && password != wantPassword {
			t.Errorf("openRunAs got the wrong password")
		}
		if mode == runAsLoggedOnUser && sess == nil {
			return nil, "", runas.ErrNoInteractiveUser
		}
		label := account
		if mode == runAsLoggedOnUser {
			label = `PC01\alice`
		}
		return sess, label, nil
	}
	rebootScheduler = func(ctx context.Context, r steps.Runner, name string) error {
		calls = append(calls, "reboot|"+name)
		return nil
	}
	savedDelay := credentialRetryDelay
	credentialRetryDelay = 0
	t.Cleanup(func() { openRunAs, rebootScheduler, credentialRetryDelay = saved, savedReboot, savedDelay })
	return &calls
}

func decodeToolkitResult(t *testing.T, s string) toolkitResult {
	t.Helper()
	var r toolkitResult
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("result JSON: %v\n%s", err, s)
	}
	return r
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestToolkitJobRunsAsAccount(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out", "cfg.xml")
	ts := newToolkitServer(t, toolkitJobDef{
		JobID: 7, ToolkitID: 3, Name: "Printers", Revision: 4,
		RunAs: runAsUser, RunAsUser: `CORP\ops`, NeedsPassword: true,
		TimeoutMinutes: 5, RebootPolicy: rebootIfRequested,
		Item: softwareItem{
			PackageID: 3, Name: "Printers",
			Files: []packageFile{{Name: "cfg.xml", URL: "/payload/toolkit/3/files/cfg.xml"}},
		},
	})
	ts.def.Item.InstallSteps = mustSteps(t, `[
		{"type":"powershell","script_body":"Add-Printer \\\\ps02\\floor3","description":"map"},
		{"type":"copy","source_path":"cfg.xml","destination_path":"`+jsonEsc(dest)+`","run_as_system":true},
		{"type":"exe","exe_path":"%APPDATA%\\tool.exe","exe_args":["/x"],"success_codes":[0,3010]}
	]`)
	ts.resultFiles["cfg.xml"] = "<cfg/>"
	sess := &fakeSession{exit: map[string]int{"tool.exe": 3010}}
	calls := withFakeRunAs(t, sess, "agent-test-pw")

	f := agentFlags{server: ts.srv.URL, agentID: "agent-1", workDir: t.TempDir()}
	status, result := runToolkitJob(context.Background(), quietLog(), httpc.New(ts.srv.URL, "u", false), f, 7)
	if status != "ok" {
		t.Fatalf("status = %s\n%s", status, result)
	}
	if strings.Contains(result, "agent-test-pw") {
		t.Fatal("result contains the password")
	}
	r := decodeToolkitResult(t, result)
	if r.ToolkitID != 3 || r.Revision != 4 || r.RunAs != runAsUser || r.RunAsUser != `CORP\ops` {
		t.Fatalf("result header: %+v", r)
	}
	if len(r.Steps) != 3 {
		t.Fatalf("steps = %+v", r.Steps)
	}
	if !strings.Contains(r.Steps[0].Output, `Add-Printer \\ps02\floor3`) || !strings.Contains(r.Steps[0].Output, "warning from powershell") {
		t.Errorf("step 1 output not captured: %q", r.Steps[0].Output)
	}
	if !r.Steps[1].AsSystem || r.Steps[0].AsSystem {
		t.Errorf("as_system flags wrong: %+v", r.Steps)
	}
	if b, err := os.ReadFile(dest); err != nil || string(b) != "<cfg/>" {
		t.Errorf("SYSTEM copy step didn't run: %v %q", err, b)
	}
	if r.Steps[2].ExitCode != 3010 || r.Reboot == "" {
		t.Errorf("3010 should request the reboot: %+v reboot=%q", r.Steps[2], r.Reboot)
	}
	if ts.credCalls != 1 || ts.lastAgentReq["agent_id"] != "agent-1" {
		t.Errorf("credential calls = %d, req = %v", ts.credCalls, ts.lastAgentReq)
	}
	if len(sess.launched) != 2 || !strings.HasPrefix(sess.launched[0], "powershell") ||
		!strings.Contains(sess.launched[1], `C:\Users\ops\AppData\Roaming\tool.exe`) {
		t.Errorf("launched = %q", sess.launched)
	}
	if len(sess.granted) != 1 || !sess.closed {
		t.Errorf("granted = %v closed = %v", sess.granted, sess.closed)
	}
	if got := strings.Join(*calls, ","); got != `user|CORP\ops,reboot|Printers` {
		t.Errorf("calls = %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.workDir, "toolkit-job-7")); !os.IsNotExist(err) {
		t.Error("work folder not removed")
	}
}

func TestToolkitJobFailingStepAborts(t *testing.T) {
	ts := newToolkitServer(t, toolkitJobDef{
		ToolkitID: 1, Name: "kit", RunAs: runAsLoggedOnUser, RebootPolicy: rebootAlways,
	})
	ts.def.Item.InstallSteps = mustSteps(t, `[
		{"type":"cmd","script_body":"bad"},
		{"type":"cmd","script_body":"never"}
	]`)
	sess := &fakeSession{exit: map[string]int{"cmd": 5}}
	calls := withFakeRunAs(t, sess, "")
	f := agentFlags{server: ts.srv.URL, agentID: "a", workDir: t.TempDir()}
	status, result := runToolkitJob(context.Background(), quietLog(), httpc.New(ts.srv.URL, "u", false), f, 1)
	r := decodeToolkitResult(t, result)
	if status != "failed" || len(r.Steps) != 1 || r.Steps[0].ExitCode != 5 || r.Steps[0].Error == "" {
		t.Fatalf("status=%s result=%+v", status, r)
	}
	if r.RunAsUser != `PC01\alice` || ts.credCalls != 0 {
		t.Errorf("logged-on user: account=%q credCalls=%d", r.RunAsUser, ts.credCalls)
	}
	// "always" restarts even after a failed step.
	if r.Reboot == "" || !strings.Contains(strings.Join(*calls, ","), "reboot|kit") {
		t.Errorf("reboot always: %q %v", r.Reboot, *calls)
	}
}

func TestToolkitJobNoSignedInUser(t *testing.T) {
	ts := newToolkitServer(t, toolkitJobDef{ToolkitID: 1, Name: "kit", RunAs: runAsLoggedOnUser})
	ts.def.Item.InstallSteps = mustSteps(t, `[{"type":"cmd","script_body":"x"}]`)
	withFakeRunAs(t, nil, "")
	f := agentFlags{server: ts.srv.URL, agentID: "a", workDir: t.TempDir()}
	status, result := runToolkitJob(context.Background(), quietLog(), httpc.New(ts.srv.URL, "u", false), f, 1)
	if status != "failed" || !strings.Contains(result, "nobody is signed in") {
		t.Fatalf("%s %s", status, result)
	}
}

func TestToolkitJobCredentialRefused(t *testing.T) {
	ts := newToolkitServer(t, toolkitJobDef{ToolkitID: 1, Name: "kit", RunAs: runAsUser, RunAsUser: "svc", NeedsPassword: true})
	ts.credStatus = http.StatusNotFound
	sess := &fakeSession{}
	calls := withFakeRunAs(t, sess, "")
	f := agentFlags{server: ts.srv.URL, agentID: "a", workDir: t.TempDir()}
	status, result := runToolkitJob(context.Background(), quietLog(), httpc.New(ts.srv.URL, "u", false), f, 1)
	if status != "failed" || !strings.Contains(result, "Run As password") || len(*calls) != 0 || ts.credCalls != 2 {
		t.Fatalf("%s %s %v", status, result, *calls)
	}
}

func TestToolkitJobTimeout(t *testing.T) {
	ts := newToolkitServer(t, toolkitJobDef{ToolkitID: 1, Name: "kit", RunAs: runAsUser, RunAsUser: "svc",
		TimeoutMinutes: 1, RebootPolicy: rebootAlways})
	ts.def.Item.InstallSteps = mustSteps(t, `[{"type":"exe","exe_path":"hang.exe"},{"type":"exe","exe_path":"next.exe"}]`)
	sess := &fakeSession{block: true}
	calls := withFakeRunAs(t, sess, "")
	f := agentFlags{server: ts.srv.URL, agentID: "a", workDir: t.TempDir()}
	// A parent context with a short deadline stands in for the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 200e6)
	defer cancel()
	status, result := runToolkitJob(ctx, quietLog(), httpc.New(ts.srv.URL, "u", false), f, 1)
	r := decodeToolkitResult(t, result)
	if status != "failed" || !r.TimedOut || len(r.Steps) != 1 {
		t.Fatalf("status=%s %+v", status, r)
	}
	if r.Reboot != "" || strings.Contains(strings.Join(*calls, ","), "reboot") {
		t.Error("must not restart after a timeout")
	}
}

func TestToolkitJobSystemDryRun(t *testing.T) {
	ts := newToolkitServer(t, toolkitJobDef{ToolkitID: 2, Name: "kit", RunAs: runAsSystem, RebootPolicy: rebootAlways})
	ts.def.Item.InstallSteps = mustSteps(t, `[{"type":"cmd","script_body":"echo"}]`)
	calls := withFakeRunAs(t, &fakeSession{}, "")
	f := agentFlags{server: ts.srv.URL, agentID: "a", workDir: t.TempDir(), dryRun: true}
	status, result := runToolkitJob(context.Background(), quietLog(), httpc.New(ts.srv.URL, "u", false), f, 1)
	r := decodeToolkitResult(t, result)
	if status != "ok" || r.RunAs != runAsSystem || r.Steps[0].AsSystem || r.Reboot != "skipped (dry run)" || len(*calls) != 0 {
		t.Fatalf("status=%s %+v calls=%v", status, r, *calls)
	}
}

func TestToolkitJobNeedsAgentID(t *testing.T) {
	status, result := runToolkitJob(context.Background(), quietLog(), httpc.New("http://x", "u", false), agentFlags{}, 1)
	if status != "failed" || !strings.Contains(result, "agent_id") {
		t.Fatalf("%s %s", status, result)
	}
}

func TestOutputTail(t *testing.T) {
	tail := newOutputTail(10)
	_, _ = tail.Write([]byte("hello\r\n"))
	if tail.String() != "hello" {
		t.Fatalf("%q", tail.String())
	}
	_, _ = tail.Write([]byte("0123456789abc\n"))
	if got := tail.String(); got != "…456789abc" {
		t.Fatalf("%q", got)
	}
	// A cut through a multi-byte rune is trimmed to whole characters.
	t2 := newOutputTail(4)
	_, _ = t2.Write([]byte("xé€"))
	if got := t2.String(); got != "…€" {
		t.Fatalf("%q", got)
	}
}

func mustSteps(t *testing.T, raw string) []swspec.InstallStep {
	t.Helper()
	var out []swspec.InstallStep
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonEsc(s string) string { return strings.ReplaceAll(s, `\`, `\\`) }
