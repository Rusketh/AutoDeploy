// Package httpc is the Boot Client's HTTP layer. It talks to the AutoDeploy
// server's JSON API and pulls payload files. The server is the sole
// authority; this package is a request/response shim, not a decision maker.
package httpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// Client wraps net/http with a base URL and the SMBIOS UUID header.
type Client struct {
	BaseURL    string
	UUID       string
	Site       string
	HTTPClient *http.Client
}

// New returns a Client targeting baseURL. In dev environments callers can
// set InsecureSkipVerify by passing a custom HTTPClient; otherwise the
// system trust store is used.
func New(baseURL, uuid string, insecureTLS bool) *Client {
	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecureTLS},
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &Client{
		BaseURL: baseURL,
		UUID:    uuid,
		HTTPClient: &http.Client{
			Transport: tr,
			Timeout:   0, // long payload downloads — rely on context for cancellation
		},
	}
}

// WithSite tags every request with the X-AutoDeploy-Site header so the
// server can route payload downloads to a site-local mirror.
func (c *Client) WithSite(site string) *Client {
	c.Site = site
	return c
}

// PostJSON sends a JSON body and decodes a JSON response into out.
func (c *Client) PostJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	if c.Site != "" {
		req.Header.Set("X-AutoDeploy-Site", c.Site)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, body)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetJSON GETs path and decodes the response.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	if c.Site != "" {
		req.Header.Set("X-AutoDeploy-Site", c.Site)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Download streams a URL to dst, optionally with a progress callback.
func (c *Client) Download(ctx context.Context, url string, dst io.Writer, progress func(written int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	if c.Site != "" {
		req.Header.Set("X-AutoDeploy-Site", c.Site)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s: %s: %s", url, resp.Status, body)
	}
	if progress == nil {
		_, err = io.Copy(dst, resp.Body)
		return err
	}
	// Periodically report progress.
	pr := &progressReader{r: resp.Body, cb: progress, last: time.Now()}
	_, err = io.Copy(dst, pr)
	return err
}

type progressReader struct {
	r       io.Reader
	cb      func(int64)
	written int64
	last    time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.written += int64(n)
	if time.Since(p.last) >= time.Second {
		p.cb(p.written)
		p.last = time.Now()
	}
	return n, err
}

// Resumable-download retry tuning. Package vars (not consts) so tests can
// shrink the waits; production uses the defaults.
var (
	retryBaseBackoff = 1 * time.Second
	retryMaxBackoff  = 30 * time.Second
	// retryIdleTimeout bounds how long a single download attempt may sit with
	// ZERO bytes arriving before it aborts the read and lets DownloadFile
	// retry (resuming via Range). This is what makes maxStall enforceable on a
	// SILENT stall -- a USB-NIC black-hole where the TCP connection stays open
	// but no data flows. Without it the body Read blocks forever, so the copy
	// never returns, never retries, never reports "failed" and never ships its
	// logs: the deploy just freezes. 30s is far longer than any healthy gap.
	retryIdleTimeout = 30 * time.Second
)

// permanentError wraps an HTTP status retrying won't fix (a 4xx) so
// DownloadFile fails fast instead of waiting out retryMaxStall on, say, a 404.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// DownloadFile downloads url into the file at path, resuming from whatever is
// already on disk (via an HTTP Range request) and surviving transient network
// outages by retrying with capped exponential backoff. It returns nil once
// the file is fully fetched; it gives up only when maxStall elapses with
// no forward progress, the server returns a 4xx, or ctx is cancelled.
//
// This is what lets a deploy ride out a mid-copy network drop: each retry
// picks up where the last left off, so when the network returns the copy
// continues instead of restarting (or aborting onto a half-written disk).
//
// wantSize is the file's expected final size (e.g. from the media index);
// when > 0 it lets DownloadFile recognise an already-complete file and catch
// a short read. Pass 0 when the size is unknown. maxStall bounds how long it
// keeps retrying with no forward progress before giving up (so a truly dead
// network still fails safe). progress, if non-nil, reports the cumulative
// bytes present for this file. onRetry, if non-nil, is called before each
// backoff wait with the attempt number and the error -- the UI uses it to
// surface "network interrupted".
func (c *Client) DownloadFile(ctx context.Context, url, path string, wantSize int64, maxStall time.Duration, progress func(int64), onRetry func(attempt int, err error)) error {
	attempt := 0
	lastProgress := time.Now()
	for {
		have := fileSize(path)
		if wantSize > 0 && have >= wantSize {
			if progress != nil {
				progress(have)
			}
			return nil // already complete (resumed deploy, or finished last loop)
		}
		n, err := c.appendFrom(ctx, url, path, have, retryIdleTimeout, progress)
		if err == nil {
			if wantSize <= 0 || fileSize(path) >= wantSize {
				return nil
			}
			// A 200 that closed early (e.g. a proxy) leaves us short: retry.
			err = fmt.Errorf("short read: have %d of %d bytes", fileSize(path), wantSize)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var perm *permanentError
		if errors.As(err, &perm) {
			return err // a 4xx won't be fixed by waiting
		}
		if n > 0 {
			lastProgress = time.Now() // made headway -- reset the stall clock
		}
		if time.Since(lastProgress) > maxStall {
			return fmt.Errorf("download %s stalled for %s: %w", path, maxStall, err)
		}
		attempt++
		if onRetry != nil {
			onRetry(attempt, err)
		}
		shift := attempt - 1
		if shift > 5 {
			shift = 5
		}
		backoff := retryBaseBackoff << uint(shift)
		if backoff > retryMaxBackoff {
			backoff = retryMaxBackoff
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// appendFrom GETs url starting at byte offset `from` and appends the body to
// path, returning the bytes written this call. A 206 is appended; a 200
// (server ignored Range) restarts the file from zero; a 416 means we already
// hold all of it. progress reports cumulative bytes (from + written).
//
// idle, when > 0, arms a watchdog that aborts the attempt if no bytes arrive
// for that long, so a silently stalled connection returns an error promptly
// instead of blocking io.Copy forever. The caller then retries (resuming via
// Range), which is what lets the deploy survive a wedged USB NIC.
func (c *Client) appendFrom(ctx context.Context, url, path string, from int64, idle time.Duration, progress func(int64)) (int64, error) {
	// Derive a cancellable context for THIS attempt so the idle watchdog can
	// abort a stalled read without disturbing the parent ctx (which governs
	// the whole deploy). defer cancel() also tears the request down on return.
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	if c.Site != "" {
		req.Header.Set("X-AutoDeploy-Site", c.Site)
	}
	if from > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	appendMode := false
	switch {
	case from > 0 && resp.StatusCode == http.StatusPartialContent:
		appendMode = true // 206: body is the tail starting at `from`
	case from > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return 0, nil // we already hold the whole file
	case resp.StatusCode == http.StatusOK:
		appendMode = false // 200: full body -- restart from zero
	case resp.StatusCode/100 == 2:
		appendMode = false
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		e := fmt.Errorf("GET %s: %s: %s", url, resp.Status, body)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return 0, &permanentError{e}
		}
		return 0, e
	}

	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	startWritten := int64(0)
	if appendMode {
		startWritten = from
	}

	// Idle watchdog: idleReader stamps the time of every non-empty read; a
	// ticker cancels reqCtx if no data has arrived within `idle`, unblocking
	// the io.Copy below with a context error so the attempt fails fast.
	body := io.Reader(resp.Body)
	if idle > 0 {
		lastRead := time.Now().UnixNano()
		body = &idleReader{r: resp.Body, last: &lastRead}
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(idle)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if time.Since(time.Unix(0, atomic.LoadInt64(&lastRead))) >= idle {
						cancel() // stalled -- abort the read; DownloadFile retries
						return
					}
				}
			}
		}()
	}

	pr := &progressReader{r: body, cb: func(total int64) {
		if progress != nil {
			progress(startWritten + total)
		}
	}, last: time.Now()}
	n, err := io.Copy(f, pr)
	if progress != nil {
		progress(startWritten + n)
	}
	return n, err
}

// idleReader stamps *last (Unix nanos, accessed atomically) on every read that
// returns data, so an idle watchdog can tell a slow-but-progressing transfer
// from a silently stalled one and abort only the latter.
type idleReader struct {
	r    io.Reader
	last *int64
}

func (ir *idleReader) Read(b []byte) (int, error) {
	n, err := ir.r.Read(b)
	if n > 0 {
		atomic.StoreInt64(ir.last, time.Now().UnixNano())
	}
	return n, err
}

// fileSize returns the size of path, or 0 if it does not exist.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
