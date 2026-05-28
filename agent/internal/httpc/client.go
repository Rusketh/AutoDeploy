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
)

type Client struct {
	BaseURL    string
	UUID       string
	HTTPClient *http.Client
	// DeployToken is the per-deploy bearer token issued by the agent
	// report endpoint at the start of a deploy. When non-empty, the
	// client sends it as X-AutoDeploy-Deploy-Token on every request,
	// which secret-returning endpoints (BitLocker config) require.
	DeployToken string
}

func New(baseURL, uuid string, insecureTLS bool) *Client {
	return &Client{
		BaseURL: baseURL, UUID: uuid,
		HTTPClient: &http.Client{
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
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	if c.DeployToken != "" {
		req.Header.Set("X-AutoDeploy-Deploy-Token", c.DeployToken)
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

// GetJSON fetches path and decodes a JSON response into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	if c.DeployToken != "" {
		req.Header.Set("X-AutoDeploy-Deploy-Token", c.DeployToken)
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
	req.Header.Set("X-AutoDeploy-Machine-UUID", c.UUID)
	if c.DeployToken != "" {
		req.Header.Set("X-AutoDeploy-Deploy-Token", c.DeployToken)
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
	_, err = io.Copy(dst, resp.Body)
	return err
}
