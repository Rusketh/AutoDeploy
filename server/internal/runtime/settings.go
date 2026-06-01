// Package runtime is the operator-facing settings layer. Settings the
// portal can change live here; settings that must be known before the
// server can listen (bind addresses, data dir, secrets key, dev mode)
// stay in the boot-time config.
//
// Precedence: portal value (system_setting row) > env value (from the
// boot config) > built-in default. The env value seeds the table on
// first start; once an operator saves a value through the portal, the
// stored value wins and env-var changes are ignored (predictable
// behaviour — config drift between the env file and the portal is
// avoided because the portal is the single source of truth for the
// runtime-changeable values).
package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Setting keys. Stable; do not rename without a migration.
const (
	keyADURL              = "ad.url"
	keyADBindDN           = "ad.bind_dn"
	keyADBindPasswordEnc  = "ad.bind_password_enc" // AES-256-GCM ciphertext
	keyADSearchBase       = "ad.search_base"
	keyADSkipTLSVerify    = "ad.skip_tls_verify"
	keyLogRetentionDays   = "retention.log_days"
	keyPayloadMaxInFlight = "payload.max_in_flight"
	keyAgentPollSeconds   = "agent.poll_interval_seconds"
	keyStorageISO         = "storage.iso"
	keyStorageDrivers     = "storage.drivers"
	keyStorageSoftware    = "storage.software"
	keyStorageIPXE        = "storage.ipxe"
	keyStorageDownloads   = "storage.downloads"
)

// StorageCategories is the closed set of relocatable category prefixes
// the portal lets operators override. The keys are the user-facing
// names; the values are the underlying setting key.
var StorageCategories = []struct {
	Category, Key, Description string
}{
	{"iso", keyStorageISO, "Extracted ISO trees + the uploaded source.iso per ISO row."},
	{"drivers", keyStorageDrivers, "Uploaded driver zips + extracted .inf trees per driver package."},
	{"software", keyStorageSoftware, "Uploaded software installer payloads."},
	{"ipxe", keyStorageIPXE, "iPXE bootstrap binaries + Boot Client kernel/initrd (TFTP + static)."},
	{"downloads", keyStorageDownloads, "Operator-distributable binaries served by the portal Downloads page."},
}

// Settings is the runtime-changeable configuration façade.
type Settings struct {
	db  *storage.DB
	bx  *secrets.Box
	env config.Config

	mu sync.RWMutex
	// cache holds the last-read values; gen increments on every write
	// so callers that want immediate consistency can re-fetch. Reads
	// are otherwise served from this cache to avoid hitting SQLite on
	// every manifest build.
	cache map[string]string
	gen   uint64
}

// New constructs the Settings facade. On first call it seeds the table
// from env vars where appropriate (so an existing env-var deployment
// keeps working unchanged).
func New(ctx context.Context, db *storage.DB, bx *secrets.Box, env config.Config) (*Settings, error) {
	s := &Settings{db: db, bx: bx, env: env, cache: map[string]string{}}
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	if err := s.seedFromEnv(ctx); err != nil {
		return nil, err
	}
	// Refresh again so the cache reflects any seeded rows.
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// refresh re-reads every known key from system_setting into cache.
func (s *Settings) refresh(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM system_setting`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fresh := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		fresh[k] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = fresh
	s.mu.Unlock()
	return nil
}

// seedFromEnv writes env-supplied AD / retention / throttle values
// into system_setting if no value is stored yet. This is a one-shot
// migration so existing env-driven installs come up with their
// settings visible in the portal.
func (s *Settings) seedFromEnv(ctx context.Context) error {
	type seed struct {
		key, value string
		// encrypt indicates the value should be encrypted via bx
		// before storage. Empty bx skips encryption (and the seed).
		encrypt bool
	}
	pending := []seed{
		{keyADURL, s.env.ADLDAPURL, false},
		{keyADBindDN, s.env.ADBindDN, false},
		{keyADBindPasswordEnc, s.env.ADBindPassword, true},
		{keyADSearchBase, s.env.ADSearchBase, false},
		{keyADSkipTLSVerify, boolStr(s.env.ADSkipTLSVerify), false},
	}
	if s.env.LogRetentionDays > 0 {
		pending = append(pending, seed{keyLogRetentionDays, strconv.Itoa(s.env.LogRetentionDays), false})
	}
	if s.env.PayloadMaxInFlight > 0 {
		pending = append(pending, seed{keyPayloadMaxInFlight, strconv.Itoa(s.env.PayloadMaxInFlight), false})
	}
	for _, p := range pending {
		if p.value == "" {
			continue
		}
		// Skip if a row already exists — portal value wins.
		if _, exists := s.cache[p.key]; exists {
			continue
		}
		value := p.value
		if p.encrypt {
			if s.bx == nil {
				continue
			}
			ct, err := s.bx.Encrypt(value)
			if err != nil {
				return fmt.Errorf("encrypt seed %s: %w", p.key, err)
			}
			value = ct
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO system_setting (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO NOTHING`, p.key, value); err != nil {
			return err
		}
	}
	return nil
}

func (s *Settings) get(key string) string {
	s.mu.RLock()
	v := s.cache[key]
	s.mu.RUnlock()
	return v
}

func (s *Settings) setRaw(ctx context.Context, key, value string) error {
	if value == "" {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM system_setting WHERE key=?`, key); err != nil {
			return err
		}
	} else {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO system_setting (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value,
			    updated_at=CURRENT_TIMESTAMP`, key, value); err != nil {
			return err
		}
	}
	return s.refresh(ctx)
}

// setRawTx is like setRaw but executes within the given transaction and
// does NOT reload the settings cache (the caller must refresh once after
// committing).
func (s *Settings) setRawTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	if value == "" {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM system_setting WHERE key=?`, key)
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO system_setting (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,
		    updated_at=CURRENT_TIMESTAMP`, key, value)
	return err
}

// ADConfig returns the current AD configuration. Empty URL means
// disabled — callers MUST short-circuit before dialing.
func (s *Settings) ADConfig(ctx context.Context) addomain.LDAPConfig {
	cfg := addomain.LDAPConfig{
		URL:           s.get(keyADURL),
		BindDN:        s.get(keyADBindDN),
		SearchBase:    s.get(keyADSearchBase),
		SkipTLSVerify: s.get(keyADSkipTLSVerify) == "true",
	}
	if enc := s.get(keyADBindPasswordEnc); enc != "" && s.bx != nil {
		pt, err := s.bx.Decrypt(enc)
		if err == nil {
			cfg.BindPassword = pt
		}
	}
	return cfg
}

// ADEnabled is a tiny convenience for "is there enough configured to
// even try?".
func (s *Settings) ADEnabled() bool {
	return s.get(keyADURL) != ""
}

// SetADConfig writes a new AD configuration. An empty URL fully
// disables AD (removes all the AD keys). All writes are batched into a
// single SQLite transaction so the cache is refreshed only once.
func (s *Settings) SetADConfig(ctx context.Context, cfg addomain.LDAPConfig) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // harmless after commit

	if cfg.URL == "" {
		// Disable: remove every AD key.
		for _, k := range []string{
			keyADURL, keyADBindDN, keyADBindPasswordEnc,
			keyADSearchBase, keyADSkipTLSVerify,
		} {
			if err := s.setRawTx(ctx, tx, k, ""); err != nil {
				return err
			}
		}
	} else {
		if err := s.setRawTx(ctx, tx, keyADURL, cfg.URL); err != nil {
			return err
		}
		if err := s.setRawTx(ctx, tx, keyADBindDN, cfg.BindDN); err != nil {
			return err
		}
		// Password is only written when supplied; "" preserves existing.
		if cfg.BindPassword != "" {
			if s.bx == nil {
				return errors.New("no secrets box configured; cannot store AD bind password")
			}
			ct, err := s.bx.Encrypt(cfg.BindPassword)
			if err != nil {
				return fmt.Errorf("encrypt AD bind password: %w", err)
			}
			if err := s.setRawTx(ctx, tx, keyADBindPasswordEnc, ct); err != nil {
				return err
			}
		}
		if err := s.setRawTx(ctx, tx, keyADSearchBase, cfg.SearchBase); err != nil {
			return err
		}
		if err := s.setRawTx(ctx, tx, keyADSkipTLSVerify, boolStr(cfg.SkipTLSVerify)); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit AD config: %w", err)
	}
	// Single cache refresh after all writes are committed.
	return s.refresh(ctx)
}

// LogRetentionDays returns the effective retention. 0 = disabled.
func (s *Settings) LogRetentionDays() int {
	v := s.get(keyLogRetentionDays)
	if v == "" {
		return s.env.LogRetentionDays
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return s.env.LogRetentionDays
	}
	return n
}

func (s *Settings) SetLogRetentionDays(ctx context.Context, days int) error {
	if days < 0 {
		return errors.New("retention days must be >= 0")
	}
	return s.setRaw(ctx, keyLogRetentionDays, strconv.Itoa(days))
}

// PayloadMaxInFlight returns the effective concurrency cap. 0 =
// unlimited.
func (s *Settings) PayloadMaxInFlight() int {
	v := s.get(keyPayloadMaxInFlight)
	if v == "" {
		return s.env.PayloadMaxInFlight
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return s.env.PayloadMaxInFlight
	}
	return n
}

func (s *Settings) SetPayloadMaxInFlight(ctx context.Context, n int) error {
	if n < 0 {
		return errors.New("max in-flight must be >= 0")
	}
	return s.setRaw(ctx, keyPayloadMaxInFlight, strconv.Itoa(n))
}

// defaultAgentPollSeconds is the resident agent's check-in cadence when the
// operator hasn't set one. 5 minutes balances responsiveness (re-image
// flags, bulk jobs, rename/AD-move reporting) against load on a large fleet.
const defaultAgentPollSeconds = 300

// AgentPollIntervalSeconds returns the resident-agent check-in cadence the
// server advertises in /api/v1/agent/self. Always >= 5s.
func (s *Settings) AgentPollIntervalSeconds() int {
	v := s.get(keyAgentPollSeconds)
	if v == "" {
		return defaultAgentPollSeconds
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 5 {
		return defaultAgentPollSeconds
	}
	return n
}

// SetAgentPollIntervalSeconds stores the agent check-in cadence. Agents pick
// it up on their next poll, so a change can take up to the OLD interval to
// apply. Minimum 5s so a misconfiguration can't hammer the server.
func (s *Settings) SetAgentPollIntervalSeconds(ctx context.Context, n int) error {
	if n < 5 {
		return errors.New("check-in interval must be >= 5 seconds")
	}
	return s.setRaw(ctx, keyAgentPollSeconds, strconv.Itoa(n))
}

// ApplyStorageOverrides pushes every configured storage path into
// the given BlobStore. Empty entries are cleared (the BlobStore
// falls back to its default subdirectory). Errors are accumulated
// into the returned map keyed by category; the caller decides
// whether to surface them to the operator.
func (s *Settings) ApplyStorageOverrides(blobs *storage.BlobStore) map[string]error {
	if blobs == nil {
		return nil
	}
	errs := map[string]error{}
	for _, c := range StorageCategories {
		p := s.get(c.Key)
		if err := blobs.SetCategoryRoot(c.Category, p); err != nil {
			errs[c.Category] = err
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// StoragePaths returns the operator-configured absolute paths for
// each relocatable category. Empty value means "use the default
// (<DATA_DIR>/<category>)". The portal Settings page edits these via
// SetStoragePath; the BlobStore consumes them via SetCategoryRoot.
func (s *Settings) StoragePaths() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	for _, c := range StorageCategories {
		out[c.Category] = s.cache[c.Key]
	}
	return out
}

// StoragePath returns the configured path for one category, or "" if
// the default applies. The path is operator input -- validate before
// using.
func (s *Settings) StoragePath(category string) string {
	for _, c := range StorageCategories {
		if c.Category == category {
			return s.get(c.Key)
		}
	}
	return ""
}

// SetStoragePath stores an operator-configured absolute path for one
// category. Empty clears (revert to default). The caller is expected
// to update the live BlobStore router after a successful set.
func (s *Settings) SetStoragePath(ctx context.Context, category, absPath string) error {
	for _, c := range StorageCategories {
		if c.Category == category {
			return s.setRaw(ctx, c.Key, absPath)
		}
	}
	return fmt.Errorf("unknown storage category %q", category)
}

// EnvDefaults returns the (immutable) env-time defaults the Settings
// was constructed with. The portal shows these alongside the
// effective values so an operator can see whether a value came from
// env or portal.
func (s *Settings) EnvDefaults() config.Config { return s.env }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
