// Package httpc is the agent's HTTP layer. Mirrors the boot-client shim.
package httpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	UUID    string
	// AgentID, when set, is sent as X-AutoDeploy-Agent-ID on every request.
	// The server serves software and Tool Kit definitions and files only to
	// an identified agent whose machine needs them.
	AgentID string
	// Credentials, when set, supplies the agent id and secret for every
	// request (it wins over AgentID). Called per request so a rotated
	// secret takes effect immediately. The secret is never logged.
	Credentials func() (agentID, secret string)
	HTTPClient  *http.Client
}

func New(baseURL, uuid string, insecureTLS bool) *Client {
	return &Client{
		BaseURL: baseURL, UUID: uuid,
		HTTPClient: &http.Client{
			Timeout: 2 * time.Minute,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureTLS},
			},
		},
	}
}

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
	c.identify(req)
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

// GetJSON fetches path and decodes a JSON response into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.identify(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, body)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Download(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	c.identify(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GET %s: %s: %s", url, resp.Status, body)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

// identify sets the machine identity headers on req.
func (c *Client) identify(req *http.Request) {
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	id, secret := c.AgentID, ""
	if c.Credentials != nil {
		if cid, s := c.Credentials(); cid != "" {
			id, secret = cid, s
		}
	}
	if id != "" {
		req.Header.Set("X-AutoDeploy-Agent-ID", id)
	}
	if secret != "" {
		req.Header.Set("X-AutoDeploy-Agent-Secret", secret)
	}
}

// IsUnauthorized reports whether err is the server's 401 "agent credential
// required" answer.
func IsUnauthorized(err error) bool {
	return err != nil && strings.Contains(err.Error(), ": 401 ")
}

// IsStatus reports whether err is a non-2xx answer with the given status.
func IsStatus(err error, code int) bool {
	return err != nil && strings.Contains(err.Error(), fmt.Sprintf(": %d ", code))
}

// IsUnknownMachine reports a 404 whose body says the server doesn't know the
// machine (as opposed to a server without the endpoint at all).
func IsUnknownMachine(err error) bool {
	return IsStatus(err, 404) && strings.Contains(err.Error(), "unknown machine")
}
