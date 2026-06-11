// Package mscatalog searches the Microsoft Update Catalog
// (catalog.update.microsoft.com) and resolves update download URLs, so
// operators can import an update — payload plus metadata — straight from
// Microsoft instead of downloading and re-uploading the .msu by hand.
//
// The catalog has no public API; like every tool in this space (e.g. the
// MSCatalogLTS PowerShell module, whose request/parse behavior this
// mirrors) we drive the same two endpoints the website uses: Search.aspx
// for the results table and DownloadDialog.aspx for the file URLs.
// Parsing is positional over the results table's cells, with the update
// GUID taken from the row's Download button (with anchor/row-id
// fallbacks for older markup vintages). Everything here is
// operator-driven — nothing searches or downloads on a schedule.
package mscatalog

import (
	"bytes"
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

// catalogHeaders applies the headers the catalog is known to be happy
// with (mirrors the working PowerShell tooling).
func catalogHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AutoDeploy)")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
}

// Search runs a catalog query (typically a KB number) and returns the
// first page of results (the catalog serves 25 per page; for KB queries
// that is the complete relevant set). A query Microsoft knows but has no
// matches for returns an empty slice and no error.
func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	u := c.BaseURL + "/Search.aspx?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	catalogHeaders(req)
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
	results, err := parseSearchHTML(body)
	if err != nil {
		return nil, fmt.Errorf("catalog search: %w", err)
	}
	return results, nil
}

// DownloadLinks resolves the file URLs for a catalog update GUID via the
// DownloadDialog endpoint.
func (c *Client) DownloadLinks(ctx context.Context, updateID string) ([]string, error) {
	// The dialog takes a JSON array of update descriptors in a form
	// field. Field and key casing mirror what the catalog accepts today.
	payload := fmt.Sprintf(`[{"size":0,"UpdateID":%q,"UpdateIDInfo":%q}]`, updateID, updateID)
	form := url.Values{"UpdateIDs": {payload}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/DownloadDialog.aspx", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	catalogHeaders(req)
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

const guidPattern = `[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}`

var (
	// The results table wraps every data row; its id has been stable for
	// years and is what the maintained scrapers anchor on.
	reUpdatesTable = regexp.MustCompile(`(?s)<table[^>]*\bid="ctl00_catalogBody_updateMatches".*?</table>`)
	reRow          = regexp.MustCompile(`(?s)<tr[^>]*>.*?</tr>`)
	reCell         = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	// GUID sources, most reliable first: the Download button input in the
	// last cell, the title anchor id "<guid>_link", a row id "<guid>_R3",
	// and a goToDetails("<guid>") onclick (possibly entity-encoded).
	reInputGUID = regexp.MustCompile(`<input[^>]*\bid="(` + guidPattern + `)"`)
	reLinkGUID  = regexp.MustCompile(`\bid="(` + guidPattern + `)_link"`)
	reRowGUID   = regexp.MustCompile(`\bid="(` + guidPattern + `)_R\d+"`)
	reGoTo      = regexp.MustCompile(`goToDetails\((?:&quot;|\\?["'])?(` + guidPattern + `)`)
	reFirstSpan = regexp.MustCompile(`(?s)<span[^>]*>(.*?)</span>`)
	// Markers the catalog uses for "no matches" and hard errors.
	reNoResults = regexp.MustCompile(`\bid="ctl00_catalogBody_noResultText"`)
	reErrorPage = regexp.MustCompile(`\bid="errorPageDisplayedError"`)
	reTag       = regexp.MustCompile(`<[^>]*>`)
	reSpace     = regexp.MustCompile(`\s+`)
	reKB        = regexp.MustCompile(`(?i)\bKB ?(\d{4,})\b`)
	// File URLs in the DownloadDialog response JS, anchored on the
	// assignment the dialog emits; the bare-URL form is the fallback.
	reDownloadURL = regexp.MustCompile(`downloadInformation\[\d+\]\.files\[\d+\]\.url\s*=\s*'([^']+)'`)
	reFileURL     = regexp.MustCompile(`https?://[^'"\s]+?\.(?:msu|cab|exe|esd)`)
)

// parseSearchHTML extracts result rows from a Search.aspx page. Cells are
// positional within the results table: checkbox, title, products,
// classification, last updated, version, size, download button. A page
// that is recognizably "no results" yields (nil, nil); a page with no
// recognizable results table is an error so markup drift surfaces as a
// message instead of a silent empty list.
func parseSearchHTML(body []byte) ([]Result, error) {
	if reNoResults.Match(body) {
		return nil, nil
	}
	if reErrorPage.Match(body) {
		return nil, fmt.Errorf("the catalog returned an error page; try again later")
	}
	table := reUpdatesTable.Find(body)
	if table == nil {
		return nil, fmt.Errorf("no results table in the catalog response (markup may have changed)")
	}
	var out []Result
	for _, row := range reRow.FindAll(table, -1) {
		if bytes.Contains(row, []byte(`id="headerRow"`)) {
			continue
		}
		cells := reCell.FindAllSubmatch(row, -1)
		if len(cells) < 8 {
			continue
		}
		guid := rowGUID(row, cells[len(cells)-1][1])
		if guid == "" {
			continue
		}
		r := Result{
			UpdateID:       guid,
			Title:          cleanText(string(cells[1][1])),
			Products:       cleanText(string(cells[2][1])),
			Classification: cleanText(string(cells[3][1])),
			LastUpdated:    cleanText(string(cells[4][1])),
		}
		// Size cell: first span is the display size ("634.2 MB"); a
		// second hidden span carries raw bytes. Fall back to the whole
		// cell when the spans are absent.
		if m := reFirstSpan.FindSubmatch(cells[6][1]); m != nil {
			r.SizeText = cleanText(string(m[1]))
		} else {
			r.SizeText = cleanText(string(cells[6][1]))
		}
		r.KBNumber = ParseKB(r.Title)
		if r.Title != "" {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("results table present but no rows parsed (markup may have changed)")
	}
	return out, nil
}

// rowGUID extracts the update GUID for a result row, trying the Download
// button in the last cell first, then older anchor/row-id forms.
func rowGUID(row, lastCell []byte) string {
	if m := reInputGUID.FindSubmatch(lastCell); m != nil {
		return strings.ToLower(string(m[1]))
	}
	for _, re := range []*regexp.Regexp{reLinkGUID, reRowGUID, reGoTo} {
		if m := re.FindSubmatch(row); m != nil {
			return strings.ToLower(string(m[1]))
		}
	}
	return ""
}

// parseDownloadLinks pulls file URLs out of a DownloadDialog response:
// the downloadInformation[i].files[j].url assignments, falling back to
// any bare update-file URL in the body.
func parseDownloadLinks(body []byte) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, m := range reDownloadURL.FindAllSubmatch(body, -1) {
		add(string(m[1]))
	}
	if len(out) == 0 {
		for _, u := range reFileURL.FindAllString(string(body), -1) {
			add(u)
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
