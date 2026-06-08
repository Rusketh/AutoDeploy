package httpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["uuid_header"] = r.Header.Get("X-AutoDeploy-Machine-UUID")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "uuid-1234", false)
	var out map[string]any
	if err := c.PostJSON(context.Background(), "/echo",
		map[string]any{"hello": "world"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["hello"] != "world" {
		t.Errorf("server didn't see request body: %+v", out)
	}
	if out["uuid_header"] != "uuid-1234" {
		t.Errorf("UUID header missing: %+v", out)
	}
}

func TestPostJSONErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	c := New(srv.URL, "x", false)
	err := c.PostJSON(context.Background(), "/anything", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("expected 400 error, got %v", err)
	}
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello-payload"))
	}))
	defer srv.Close()
	c := New(srv.URL, "x", false)
	var buf strings.Builder
	if err := c.Download(context.Background(), srv.URL+"/x", &buf, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello-payload" {
		t.Errorf("body = %q", buf.String())
	}
}

// swapBackoff shrinks the retry waits for fast tests; the returned func
// restores them.
func swapBackoff(base, max time.Duration) func() {
	ob, om := retryBaseBackoff, retryMaxBackoff
	retryBaseBackoff, retryMaxBackoff = base, max
	return func() { retryBaseBackoff, retryMaxBackoff = ob, om }
}

// swapIdleTimeout shrinks the per-attempt idle watchdog for fast tests.
func swapIdleTimeout(d time.Duration) func() {
	old := retryIdleTimeout
	retryIdleTimeout = d
	return func() { retryIdleTimeout = old }
}

// TestDownloadFileRecoversFromStalledRead is the regression guard for the
// "deploy freezes mid-download" bug: the first attempt streams a few bytes
// then goes silent (TCP open, no data -- a wedged USB NIC), which used to
// block io.Copy forever. The idle watchdog must abort that read so the copy
// retries (resuming via Range) and completes -- without hanging.
func TestDownloadFileRecoversFromStalledRead(t *testing.T) {
	defer swapBackoff(time.Millisecond, 2*time.Millisecond)()
	defer swapIdleTimeout(50 * time.Millisecond)()
	const full = "STREAM-THAT-STALLS-THEN-RESUMES-OK"
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// First attempt: a few bytes, flush, then go silent for far
			// longer than the idle timeout. The client must abort, not wait.
			_, _ = w.Write([]byte(full[:8]))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			time.Sleep(500 * time.Millisecond)
			return
		}
		// Retry carries a Range header; honour it and serve the rest.
		http.ServeContent(w, r, "blob", time.Time{}, strings.NewReader(full))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "blob")
	c := New(srv.URL, "x", false)
	done := make(chan error, 1)
	go func() {
		done <- c.DownloadFile(context.Background(), srv.URL+"/blob", path,
			int64(len(full)), time.Minute, nil, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DownloadFile hung on a stalled read (idle watchdog did not fire)")
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Errorf("expected a retry after the stall, got %d call(s)", calls)
	}
	if got, _ := os.ReadFile(path); string(got) != full {
		t.Errorf("recovered file = %q, want %q", got, full)
	}
}

// TestDownloadFileResumesViaRange seeds a correct partial file and asserts
// DownloadFile resumes (sends a Range) and appends only the tail.
func TestDownloadFileResumesViaRange(t *testing.T) {
	const full = "0123456789abcdefghijABCDEFGHIJ" // 30 bytes
	var gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		http.ServeContent(w, r, "blob", time.Time{}, strings.NewReader(full))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, []byte(full[:10]), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(srv.URL, "x", false)
	if err := c.DownloadFile(context.Background(), srv.URL+"/blob", path, int64(len(full)), time.Minute, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotRange, "bytes=10-") {
		t.Errorf("expected a resume Range request, got %q", gotRange)
	}
	if got, _ := os.ReadFile(path); string(got) != full {
		t.Errorf("resumed file = %q, want %q", got, full)
	}
}

// TestDownloadFileRestartsWhenServerIgnoresRange asserts a stale partial is
// discarded (truncated) when the server replies 200 instead of honouring Range.
func TestDownloadFileRestartsWhenServerIgnoresRange(t *testing.T) {
	const full = "FULL-CONTENT-RESTARTED"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(full)) // ignore Range: always 200 + full body
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, []byte("XXXXX"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(srv.URL, "x", false)
	if err := c.DownloadFile(context.Background(), srv.URL+"/blob", path, int64(len(full)), time.Minute, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != full {
		t.Errorf("restarted file = %q, want %q", got, full)
	}
}

// TestDownloadFileRetriesThenSucceeds simulates a transient outage: the first
// two requests fail, the third serves the file. The copy must survive it.
func TestDownloadFileRetriesThenSucceeds(t *testing.T) {
	defer swapBackoff(time.Millisecond, 2*time.Millisecond)()
	const full = "payload-after-retries"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		http.ServeContent(w, r, "blob", time.Time{}, strings.NewReader(full))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "blob")
	c := New(srv.URL, "x", false)
	var retries int
	err := c.DownloadFile(context.Background(), srv.URL+"/blob", path, int64(len(full)), time.Minute,
		nil, func(attempt int, _ error) { retries = attempt })
	if err != nil {
		t.Fatal(err)
	}
	if retries < 2 {
		t.Errorf("expected >=2 retries, got %d", retries)
	}
	if got, _ := os.ReadFile(path); string(got) != full {
		t.Errorf("file = %q, want %q", got, full)
	}
}

// TestDownloadFileFailsFastOn404 asserts a 4xx is not retried for retryMaxStall.
func TestDownloadFileFailsFastOn404(t *testing.T) {
	defer swapBackoff(time.Millisecond, 2*time.Millisecond)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "x", false)
	start := time.Now()
	err := c.DownloadFile(context.Background(), srv.URL+"/missing", filepath.Join(t.TempDir(), "x"), 10, time.Minute, nil, nil)
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if time.Since(start) > time.Second {
		t.Errorf("404 should fail fast, took %s", time.Since(start))
	}
}

// TestDownloadFileSkipsCompleteFile asserts an already-complete file is a
// no-op that never touches the network.
func TestDownloadFileSkipsCompleteFile(t *testing.T) {
	const full = "already-have-this"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be hit for a complete file (Range=%q)", r.Header.Get("Range"))
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(srv.URL, "x", false)
	if err := c.DownloadFile(context.Background(), srv.URL+"/blob", path, int64(len(full)), time.Minute, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// TestDownloadFileGivesUpAfterStall asserts a sustained outage fails (safely)
// once the stall budget elapses, rather than hanging forever.
func TestDownloadFileGivesUpAfterStall(t *testing.T) {
	defer swapBackoff(time.Millisecond, time.Millisecond)()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, "x", false)
	start := time.Now()
	err := c.DownloadFile(context.Background(), srv.URL+"/x", filepath.Join(t.TempDir(), "x"),
		10, 80*time.Millisecond, nil, nil)
	if err == nil {
		t.Fatal("expected give-up error after the stall budget")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("gave up too slowly: %s", d)
	}
}
