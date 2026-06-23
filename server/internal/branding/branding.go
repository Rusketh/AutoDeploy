// Package branding stores and serves the system-wide organisational
// brand: product name, organisation name, support URL, logo, colour
// scheme. One brand, applied everywhere — portal, boot screen, deployed
// machine OEM info. Per-image or multi-tenant branding is explicitly
// out of scope (design §12).
package branding

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

const settingKey = "branding"

// maxLogoBytes caps the stored logo. The data URL is inlined into every portal
// page (the nav) and shipped to clients, so an oversized image would bloat
// every render — keep it small.
const maxLogoBytes = 1 << 20 // 1 MiB

// SafeLogoURL trims s and reports whether it is an acceptable logo reference:
// empty, an image data URL (data:image/...), or an http(s) URL, and within the
// size cap. The portal validates uploads with it and the templates gate the
// <img src> on it, so a stored brand can never carry a non-image or unsafe URL
// (e.g. a javascript: scheme) into a rendered page. Returns the trimmed value
// when acceptable.
func SafeLogoURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	if len(s) > maxLogoBytes {
		return "", false
	}
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "data:image/"),
		strings.HasPrefix(low, "https://"),
		strings.HasPrefix(low, "http://"):
		return s, true
	default:
		return "", false
	}
}

// Brand is the operator-configured identity.
type Brand struct {
	ProductName      string `json:"product_name"` // "AutoDeploy" by default
	OrganisationName string `json:"organisation_name"`
	SupportURL       string `json:"support_url"`
	SupportPhone     string `json:"support_phone"`
	LogoDataURL      string `json:"logo_data_url"`    // data: URI of a small SVG/PNG
	PrimaryColor     string `json:"primary_color"`    // CSS color string
	OEMManufacturer  string `json:"oem_manufacturer"` // written to HKLM\...\OEMInformation\Manufacturer
}

// Defaults returns the baseline brand (used when nothing is configured).
func Defaults() Brand {
	return Brand{
		ProductName:      "AutoDeploy",
		OrganisationName: "",
		PrimaryColor:     "#0b65c2",
	}
}

// Repo persists Brand into system_setting.
type Repo struct{ DB *storage.DB }

func New(db *storage.DB) *Repo { return &Repo{DB: db} }

// Get returns the stored brand, falling back to Defaults when none is set.
func (r *Repo) Get(ctx context.Context) (Brand, error) {
	var raw string
	err := r.DB.QueryRowContext(ctx,
		`SELECT value FROM system_setting WHERE key=?`, settingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Defaults(), nil
	}
	if err != nil {
		return Brand{}, err
	}
	if raw == "" {
		return Defaults(), nil
	}
	var b Brand
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return Brand{}, fmt.Errorf("parse stored brand: %w", err)
	}
	if b.ProductName == "" {
		b.ProductName = "AutoDeploy"
	}
	return b, nil
}

// Set replaces the brand.
func (r *Repo) Set(ctx context.Context, b Brand) error {
	if b.ProductName == "" {
		b.ProductName = "AutoDeploy"
	}
	// Defence in depth: never persist a logo the templates would refuse to
	// render (or an oversized one). An unacceptable value is dropped rather
	// than stored; the portal validates up front and surfaces a clear error.
	b.LogoDataURL, _ = SafeLogoURL(b.LogoDataURL)
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = r.DB.ExecContext(ctx, `
		INSERT INTO system_setting(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		settingKey, string(raw))
	return err
}
