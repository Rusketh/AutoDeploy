package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/httpc"
	"github.com/rusketh/autodeploy/agent/internal/steps"
)

type postedResult struct {
	path, status, result, agentID string
}

func resultServer(t *testing.T, code int) (*httptest.Server, *[]postedResult) {
	t.Helper()
	var mu sync.Mutex
	var got []postedResult
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status  string `json:"status"`
			Result  string `json:"result_json"`
			AgentID string `json:"agent_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, postedResult{r.URL.Path, body.Status, body.Result, body.AgentID})
		mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestInterruptedJobsReportedNotRerun(t *testing.T) {
	f := agentFlags{workDir: t.TempDir()}
	writeJournal(f, journalEntry{JobID: 11, Action: "toolkit"})                                         // died mid-run
	writeJournal(f, journalEntry{JobID: 12, Action: "script", Status: "ok", Result: `{"exit_code":0}`}) // result POST failed
	writeJournal(f, journalEntry{JobID: 13, Action: "rename"})                                          // restarted itself
	srv, got := resultServer(t, http.StatusNoContent)
	reportInterruptedJobs(context.Background(), quietLog(), httpc.New(srv.URL, "u", false), f)

	byPath := map[string]postedResult{}
	for _, p := range *got {
		byPath[p.path] = p
	}
	if p := byPath["/api/v1/agent/jobs/11/result"]; p.status != "failed" || !strings.Contains(p.result, "not run again") {
		t.Errorf("interrupted job: %+v", p)
	}
	if p := byPath["/api/v1/agent/jobs/12/result"]; p.status != "ok" || p.result != `{"exit_code":0}` {
		t.Errorf("saved result not re-sent: %+v", p)
	}
	if p := byPath["/api/v1/agent/jobs/13/result"]; p.status != "ok" {
		t.Errorf("rename restart should not count as failure: %+v", p)
	}
	if entries, _ := os.ReadDir(journalDir(f)); len(entries) != 0 {
		t.Errorf("journal not cleared: %d left", len(entries))
	}
}

func TestInterruptedJobsKeptWhenServerUnreachable(t *testing.T) {
	f := agentFlags{workDir: t.TempDir()}
	writeJournal(f, journalEntry{JobID: 5, Action: "toolkit"})
	srv, _ := resultServer(t, http.StatusBadGateway)
	reportInterruptedJobs(context.Background(), quietLog(), httpc.New(srv.URL, "u", false), f)
	if _, err := os.Stat(journalPath(f, 5)); err != nil {
		t.Fatal("marker dropped although the server didn't take the result")
	}
	// A 404 (job gone) drops it.
	srv404, _ := resultServer(t, http.StatusNotFound)
	reportInterruptedJobs(context.Background(), quietLog(), httpc.New(srv404.URL, "u", false), f)
	if _, err := os.Stat(journalPath(f, 5)); !os.IsNotExist(err) {
		t.Fatal("marker kept after a 404")
	}
}

func TestRunAndReportJobJournal(t *testing.T) {
	f := agentFlags{workDir: t.TempDir(), dryRun: true}
	srv, got := resultServer(t, http.StatusNoContent)
	c := httpc.New(srv.URL, "u", false)
	runAndReportJob(context.Background(), quietLog(), c, f, &steps.OSRunner{DryRun: true}, 21, "script", `{"shell":"cmd","body":"echo"}`)
	if len(*got) != 1 || (*got)[0].status != "ok" {
		t.Fatalf("posted = %+v", *got)
	}
	if _, err := os.Stat(journalPath(f, 21)); !os.IsNotExist(err) {
		t.Fatal("marker left after a reported job")
	}
	// Result POST fails: the outcome is kept for the next start.
	bad, _ := resultServer(t, http.StatusBadGateway)
	runAndReportJob(context.Background(), quietLog(), httpc.New(bad.URL, "u", false), f, &steps.OSRunner{DryRun: true}, 22, "script", `{"shell":"cmd","body":"echo"}`)
	b, err := os.ReadFile(journalPath(f, 22))
	if err != nil || !strings.Contains(string(b), `"status":"ok"`) {
		t.Fatalf("saved outcome = %s, %v", b, err)
	}
}

func TestAbandonClaimedJobs(t *testing.T) {
	srv, got := resultServer(t, http.StatusNoContent)
	abandonClaimedJobs(context.Background(), quietLog(), httpc.New(srv.URL, "u", false), agentFlags{agentID: "a-1"}, []int64{3, 4}, "to apply its new name")
	if len(*got) != 2 || (*got)[0].status != "failed" || !strings.Contains((*got)[0].result, "to apply its new name") || (*got)[0].agentID != "a-1" {
		t.Fatalf("posted = %+v", *got)
	}
}
