package wingetsrc

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (same as the main store)
)

// The winget "pre-indexed" source is an MSIX (a ZIP) served from the CDN,
// containing Public/index.db — a SQLite database mapping package
// identifiers, names and monikers to their available versions. It carries
// no installer URLs; those live in the per-version YAML manifests (see
// manifest.go). We download the index, cache the extracted db, and query
// it for search and version discovery. This mirrors how the winget client
// itself consumes the community source.

// indexEntryNames are the candidate source-index MSIX filenames, newest
// schema first. Both are served for client-compat, but they carry *different*
// SQLite schemas, so we detect which one we got before querying (see
// indexIsV2):
//
//   - source2.msix holds the v2 ("SQLite Index 2.0") schema: a single
//     denormalised `packages` table (id, name, moniker, latest_version, ...)
//     storing one row per package with only its latest version.
//   - source.msix holds the v1 normalised schema: ids/names/monikers/versions
//     interned by rowid and joined through a `manifest` table, with one
//     manifest row per package *version*.
var indexEntryNames = []string{"source2.msix", "source.msix"}

// ensureIndex makes sure a fresh-enough index db is cached on disk and
// returns its path. It downloads and extracts the source MSIX at most once
// per IndexTTL; concurrent callers serialise on the mutex.
func (c *Client) ensureIndex(ctx context.Context) (string, error) {
	c.idx.Lock()
	defer c.idx.Unlock()

	if c.idxPath != "" && time.Since(c.idxFetchedAt) < c.indexTTL() {
		if _, err := os.Stat(c.idxPath); err == nil {
			return c.idxPath, nil
		}
	}

	dir := c.CacheDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "autodeploy-winget")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("winget index: cache dir: %w", err)
	}

	msixData, srcName, err := c.downloadIndexMSIX(ctx)
	if err != nil {
		return "", err
	}
	dbPath := filepath.Join(dir, "index.db")
	if err := extractIndexDB(msixData, dbPath); err != nil {
		return "", fmt.Errorf("winget index (%s): %w", srcName, err)
	}
	c.idxPath = dbPath
	c.idxFetchedAt = time.Now()
	return dbPath, nil
}

// downloadIndexMSIX fetches the source index MSIX, trying each candidate
// filename until one returns 200.
func (c *Client) downloadIndexMSIX(ctx context.Context) ([]byte, string, error) {
	var lastErr error
	for _, name := range indexEntryNames {
		u := c.indexBaseURL() + "/" + name
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", "autodeploy-server")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("winget index: download %s: %w", name, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("winget index: download %s: status %s", name, resp.Status)
			continue
		}
		// The index MSIX is a few MB to low tens of MB; cap generously.
		data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("winget index: read %s: %w", name, err)
			continue
		}
		return data, name, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("winget index: no source index available")
	}
	return nil, "", lastErr
}

// extractIndexDB writes the Public/index.db entry of an MSIX (ZIP) to
// dbPath. The write is atomic (temp + rename) so a concurrent open never
// sees a half-written db.
func extractIndexDB(msix []byte, dbPath string) error {
	zr, err := zip.NewReader(bytes.NewReader(msix), int64(len(msix)))
	if err != nil {
		return fmt.Errorf("open msix: %w", err)
	}
	var entry *zip.File
	for _, f := range zr.File {
		// Entry names use forward slashes; match case-insensitively on the
		// well-known path the winget source uses.
		if strings.EqualFold(f.Name, "Public/index.db") {
			entry = f
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("Public/index.db not found in source index")
	}
	rc, err := entry.Open()
	if err != nil {
		return fmt.Errorf("open index.db: %w", err)
	}
	defer rc.Close()

	tmp := dbPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dbPath)
}

// openIndexDB opens the cached index database read-only.
func openIndexDB(path string) (*sql.DB, error) {
	// immutable=1: we never write, and the file may be read-only; this also
	// skips WAL/journal setup for a plain query workload.
	dsn := "file:" + path + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return db, nil
}

// indexIsV2 reports whether the opened index uses the v2 "packages" schema
// (from source2.msix) rather than the v1 normalised manifest schema (from
// source.msix). We branch on the presence of the `packages` table; a v1 index
// has no such table, a v2 index has no `manifest` table.
func indexIsV2(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='packages'`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// v1 search: the manifest table references ids/names/monikers/versions by
// rowid. A LEFT JOIN on monikers keeps rows whose moniker is unset.
const searchQueryV1 = `
	SELECT ids.id, names.name, versions.version
	FROM manifest
	JOIN ids ON manifest.id = ids.rowid
	JOIN names ON manifest.name = names.rowid
	JOIN versions ON manifest.version = versions.rowid
	LEFT JOIN monikers ON manifest.moniker = monikers.rowid
	WHERE ids.id LIKE ?1 ESCAPE '\'
	   OR names.name LIKE ?1 ESCAPE '\'
	   OR monikers.moniker LIKE ?1 ESCAPE '\'`

// v2 search: the packages table stores id/name/moniker/latest_version
// directly as text (moniker is nullable, so LIKE against NULL is simply
// false — no join needed).
const searchQueryV2 = `
	SELECT id, name, latest_version
	FROM packages
	WHERE id LIKE ?1 ESCAPE '\'
	   OR name LIKE ?1 ESCAPE '\'
	   OR moniker LIKE ?1 ESCAPE '\'`

// searchIndex queries the index for packages whose id, name or moniker
// contains query (case-insensitive substring), grouped by identifier with
// the newest version chosen. Results are capped at limit.
func (c *Client) searchIndex(ctx context.Context, dbPath, query string, limit int) ([]Result, error) {
	db, err := openIndexDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("winget index: open: %w", err)
	}
	defer db.Close()

	v2, err := indexIsV2(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("winget index: detect schema: %w", err)
	}
	q := searchQueryV1
	if v2 {
		q = searchQueryV2
	}

	like := "%" + escapeLike(query) + "%"
	rows, err := db.QueryContext(ctx, q, like)
	if err != nil {
		return nil, fmt.Errorf("winget index: query (schema may have changed): %w", err)
	}
	defer rows.Close()

	// Group by identifier, keeping the display name and the newest version.
	byID := map[string]*indexAgg{}
	var seq int
	for rows.Next() {
		var id, name, version string
		if err := rows.Scan(&id, &name, &version); err != nil {
			return nil, err
		}
		a, ok := byID[id]
		if !ok {
			byID[id] = &indexAgg{id: id, name: name, version: version, order: seq}
			seq++
			continue
		}
		if name != "" {
			a.name = name
		}
		if isNewerVersion(version, a.version) {
			a.version = version
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Stable-ish ordering: first-seen order, capped.
	ordered := make([]*indexAgg, 0, len(byID))
	for _, a := range byID {
		ordered = append(ordered, a)
	}
	sortByOrder(ordered)

	out := make([]Result, 0, len(ordered))
	for _, a := range ordered {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, Result{
			PackageIdentifier: a.id,
			Name:              a.name,
			LatestVersion:     a.version,
		})
	}
	return out, nil
}

// v1 lists every interned version row for an identifier.
const versionsQueryV1 = `
	SELECT versions.version
	FROM manifest
	JOIN ids ON manifest.id = ids.rowid
	JOIN versions ON manifest.version = versions.rowid
	WHERE ids.id = ?1 COLLATE NOCASE`

// v2 only stores each package's latest version, so this yields a single row.
const versionsQueryV2 = `
	SELECT latest_version
	FROM packages
	WHERE id = ?1 COLLATE NOCASE`

// indexVersions returns the versions of an exact package identifier held in
// the index, newest first. Used to resolve "latest" when a caller doesn't pin
// a version (imports and dependency resolution). A v1 index carries every
// published version; a v2 index carries only the latest, so this returns that
// single version there — still the right answer for resolving "latest".
func (c *Client) indexVersions(ctx context.Context, dbPath, id string) ([]string, error) {
	db, err := openIndexDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("winget index: open: %w", err)
	}
	defer db.Close()

	v2, err := indexIsV2(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("winget index: detect schema: %w", err)
	}
	q := versionsQueryV1
	if v2 {
		q = versionsQueryV2
	}
	rows, err := db.QueryContext(ctx, q, id)
	if err != nil {
		return nil, fmt.Errorf("winget index: version query: %w", err)
	}
	defer rows.Close()

	var vers []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		vers = append(vers, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortVersionsDesc(vers)
	return vers, nil
}

// escapeLike escapes LIKE wildcards in a user query so a literal % or _
// isn't treated as a wildcard (paired with ESCAPE '\' in the SQL).
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// indexAgg groups a package's index rows during search.
type indexAgg struct {
	id, name, version string
	order             int
}

func sortByOrder(a []*indexAgg) {
	// small insertion sort by first-seen order
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j].order < a[j-1].order; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func sortVersionsDesc(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && isNewerVersion(v[j], v[j-1]); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
