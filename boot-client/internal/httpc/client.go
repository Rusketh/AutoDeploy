// Package httpc is the Boot Client's HTTP layer. It talks to the AutoDeploy
// server's JSON API and pulls payload files. The server is the sole
// authority; this package is a request/response shim, not a decision maker.
package httpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client wraps net/http with a base URL and the SMBIOS UUID header.
type Client struct {
	BaseURL    string
	UUID       string
	HTTPClient *http.Client
}

// New returns a Client targeting baseURL. In dev environments callers can
// set InsecureSkipVerify by passing a custom HTTPClient; otherwise the
// system trust store is used.
func New(baseURL, uuid string, insecureTLS bool) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureTLS},
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
