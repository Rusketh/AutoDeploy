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

	"github.com/rusketh/autodeploy/server/internal/storage"
)

const settingKey = "branding"

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
