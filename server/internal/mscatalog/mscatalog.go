// Package mscatalog searches the Microsoft Update Catalog
// (catalog.update.microsoft.com) and resolves update download URLs, so
// operators can import an update — payload plus metadata — straight from
// Microsoft instead of downloading and re-uploading the .msu by hand.
//
// The catalog has no public API; like every tool in this space we drive
// the same two endpoints the website uses: Search.aspx for the results
// table and DownloadDialog.aspx for the file URLs. Parsing is anchored on
// the stable GUID-prefixed element IDs in the markup. Everything here is
// operator-driven — nothing searches or downloads on a schedule.
package mscatalog

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Result is one row of a catalog search.
type Result struct {
	UpdateID       string `json:"update_id"` // catalog GUID
	Title          string `json:"title"`
	KBNumber       string `json:"kb_number,omitempty"` // parsed from the title, may be empty
	Products       string `json:"products"`
	Classification string `json:"classification"`
	LastUpdated    string `json:"last_updated"`
	SizeText       string `json:"size"`
}

// Searcher is the catalog surface the API handlers depend on; tests swap
// in a fake so no test touches the network.
type Searcher interface {
	Search(ctx context.Context, query string) ([]Result, error)
	DownloadLinks(ctx context.Context, updateID string) ([]string, error)
}

// Client talks to the Microsoft Update Catalog over HTTP.
type Client struct {
	HTTP    *http.Client
	BaseURL string // overridable for tests
}

// New returns a catalog client with sane defaults. The default transport
// honours HTTPS_PROXY and friends.
func New() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		BaseURL: "https://www.catalog.update.microsoft.com",
	}
}

// Search runs a catalog query (typically a KB number) and returns the
// first page of results (the catalog serves 25 per page; for KB queries
// that is the complete relevant set).
func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	u := c.BaseURL + "/Search.aspx?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "autodeploy-server")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog search: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("catalog search: %w", err)
	}
	return parseSearchHTML(body), nil
}

// DownloadLinks resolves the file URLs for a catalog update GUID via the
// DownloadDialog endpoint.
func (c *Client) DownloadLinks(ctx context.Context, updateID string) ([]string, error) {
	// The dialog takes a JSON array of update descriptors in a form field.
	payload := fmt.Sprintf(`[{"size":0,"updateID":%q,"uidInfo":%q}]`, updateID, updateID)
	form := url.Values{"updateIDs": {payload}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/DownloadDialog.aspx", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "autodeploy-server")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog download dialog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog download dialog: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("catalog download dialog: %w", err)
	}
	links := parseDownloadLinks(body)
	if len(links) == 0 {
		return nil, fmt.Errorf("catalog download dialog: no file URLs for update %s", updateID)
	}
	return links, nil
}

var (
	// Result rows carry a GUID-prefixed id: <tr id="<guid>_R3" ...>. Other
	// attributes may surround id, so anchor on the id alone.
	reResultRow = regexp.MustCompile(`(?s)<tr[^>]*\bid="([0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12})_R\d+".*?</tr>`)
	// The title anchor inside a row: <a id="<guid>_link" ...>title</a>.
	reTitleLink = regexp.MustCompile(`(?s)id="[0-9a-fA-F-]{36}_link"[^>]*>(.*?)</a>`)
	// The display size span: <span id="<guid>_size">634.2 MB</span>.
	reSizeSpan = regexp.MustCompile(`(?s)id="[0-9a-fA-F-]{36}_size"[^>]*>(.*?)</span>`)
	reCell     = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	reTag      = regexp.MustCompile(`<[^>]*>`)
	reSpace    = regexp.MustCompile(`\s+`)
	reKB       = regexp.MustCompile(`(?i)\bKB ?(\d{4,})\b`)
	// File URLs in the DownloadDialog response JS.
	reFileURL = regexp.MustCompile(`https?://[^'"\s]+?\.(?:msu|cab|exe|esd)`)
)

// parseSearchHTML extracts result rows from a Search.aspx page. Cells are
// positional within each GUID-anchored row: checkbox, title, products,
// classification, last updated, version, size, download button.
func parseSearchHTML(body []byte) []Result {
	var out []Result
	for _, m := range reResultRow.FindAllSubmatch(body, -1) {
		guid, row := strings.ToLower(string(m[1])), m[0]
		r := Result{UpdateID: guid}
		if t := reTitleLink.FindSubmatch(row); t != nil {
			r.Title = cleanText(string(t[1]))
		}
		if s := reSizeSpan.FindSubmatch(row); s != nil {
			r.SizeText = cleanText(string(s[1]))
		}
		cells := reCell.FindAllSubmatch(row, -1)
		cellText := func(i int) string {
			if i < len(cells) {
				return cleanText(string(cells[i][1]))
			}
			return ""
		}
		r.Products = cellText(2)
		r.Classification = cellText(3)
		r.LastUpdated = cellText(4)
		r.KBNumber = ParseKB(r.Title)
		if r.Title != "" {
			out = append(out, r)
		}
	}
	return out
}

// parseDownloadLinks pulls file URLs out of a DownloadDialog response.
func parseDownloadLinks(body []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range reFileURL.FindAllString(string(body), -1) {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// cleanText strips tags, unescapes entities, and collapses whitespace.
func cleanText(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

// ParseKB extracts a KB number ("KB5034441") from an update title; empty
// when the title carries none.
func ParseKB(title string) string {
	m := reKB.FindStringSubmatch(title)
	if m == nil {
		return ""
	}
	return "KB" + m[1]
}

// GuessOSFilter maps a catalog Products column to the portal's OS filter
// values; empty when no confident match.
func GuessOSFilter(products string) string {
	p := strings.ToLower(products)
	switch {
	case strings.Contains(p, "windows 11"):
		return "windows-11"
	case strings.Contains(p, "windows 10"):
		return "windows-10"
	case strings.Contains(p, "server 2025"):
		return "server-2025"
	case strings.Contains(p, "server 2022"):
		return "server-2022"
	case strings.Contains(p, "server 2019"):
		return "server-2019"
	default:
		return ""
	}
}

// GuessSeverity maps a catalog Classification to the portal's severity
// values; empty (operator's call) when unclear.
func GuessSeverity(classification string) string {
	c := strings.ToLower(classification)
	switch {
	case strings.Contains(c, "critical"):
		return "critical"
	case strings.Contains(c, "security"):
		return "important"
	default:
		return ""
	}
}

// PickFile chooses which of an update's file URLs to import: the first
// .msu (the format the agent installs via wusa), else the first .cab,
// else the first link. Empty when there are none.
func PickFile(links []string) string {
	for _, ext := range []string{".msu", ".cab"} {
		for _, l := range links {
			if strings.HasSuffix(strings.ToLower(l), ext) {
				return l
			}
		}
	}
	if len(links) > 0 {
		return links[0]
	}
	return ""
}
