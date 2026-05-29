package portal

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/branding"
)

func init() {
	registerSettingsRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/settings", settingsIndex(r))

		// Access PIN
		get("/portal/settings/access-pin", accessPINForm(r))
		post("/portal/settings/access-pin", accessPINSubmit(r))

		// Branding
		get("/portal/settings/branding", brandingForm(r))
		post("/portal/settings/branding", brandingSubmit(r))

		// Accounts
		get("/portal/settings/accounts", accountsList(r))
		post("/portal/settings/accounts", accountCreate(r))
		post("/portal/settings/accounts/{id}/disable", accountSetDisabled(r, true))
		post("/portal/settings/accounts/{id}/enable", accountSetDisabled(r, false))
		post("/portal/settings/accounts/{id}/password", accountSetPassword(r))
		post("/portal/settings/accounts/{id}/delete", accountDelete(r))

		// Active Directory (runtime-managed, no env restart needed)
		get("/portal/settings/active-directory", adForm(r))
		post("/portal/settings/active-directory", adSubmit(r))
		post("/portal/settings/active-directory/test", adTest(r))

		// Operational (retention, throttle)
		get("/portal/settings/operational", opsForm(r))
		post("/portal/settings/operational", opsSubmit(r))

		// Storage (per-category path overrides)
		get("/portal/settings/storage", storageForm(r))
		post("/portal/settings/storage", storageSubmit(r))

		// Updates (server + agent)
		get("/portal/settings/updates", updatesPage(r))

		// PXE setup central hub
		get("/portal/settings/pxe", pxePage(r))
		get("/portal/settings/pxe/download/{name}", pxeDownload(r))
	}
}

func settingsIndex(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		brand, _ := r.Branding.Get(req.Context())
		users, _ := r.Users.ListUsers(req.Context())
		render(w, req, r, "settings_index.html", "Settings", map[string]any{
			"Brand":     brand,
			"UserCount": len(users),
			"ADEnabled": r.Runtime != nil && r.Runtime.ADEnabled(),
		})
	}
}

func accessPINForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		on, _ := r.Settings.AccessPINEnabled(req.Context())
		render(w, req, r, "settings_pin.html", "Access PIN", map[string]any{"On": on})
	}
}

func accessPINSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		pin := req.FormValue("pin")
		if err := r.Settings.SetAccessPIN(req.Context(), pin); err != nil {
			flash(w, "err", err.Error())
		} else if pin == "" {
			flash(w, "ok", "Access PIN cleared. Boot menu is now open to any PXE client.")
		} else {
			flash(w, "ok", "Access PIN set. Boot menu now requires it.")
		}
		http.Redirect(w, req, "/portal/settings/access-pin", http.StatusFound)
	}
}

func brandingForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := r.Branding.Get(req.Context())
		render(w, req, r, "settings_branding.html", "Branding", map[string]any{"B": b})
	}
}

func brandingSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b := branding.Brand{
			ProductName:      strings.TrimSpace(req.FormValue("product_name")),
			OrganisationName: strings.TrimSpace(req.FormValue("organisation_name")),
			SupportURL:       strings.TrimSpace(req.FormValue("support_url")),
			SupportPhone:     strings.TrimSpace(req.FormValue("support_phone")),
			LogoDataURL:      strings.TrimSpace(req.FormValue("logo_data_url")),
			PrimaryColor:     strings.TrimSpace(req.FormValue("primary_color")),
			OEMManufacturer:  strings.TrimSpace(req.FormValue("oem_manufacturer")),
		}
		if err := r.Branding.Set(req.Context(), b); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Branding saved.")
		}
		http.Redirect(w, req, "/portal/settings/branding", http.StatusFound)
	}
}

func accountsList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		users, _ := r.Users.ListUsers(req.Context())
		me, _ := sessionUser(req, r)
		render(w, req, r, "settings_accounts.html", "Accounts", map[string]any{
			"Users": users, "Me": me,
		})
	}
}

func accountCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u := strings.TrimSpace(req.FormValue("username"))
		p := req.FormValue("password")
		if _, err := r.Users.CreateUser(req.Context(), u, p); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Account created.")
		}
		http.Redirect(w, req, "/portal/settings/accounts", http.StatusFound)
	}
}

func accountSetDisabled(r Repos, disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		if err := r.Users.SetDisabled(req.Context(), id, disabled); err != nil {
			flash(w, "err", err.Error())
		} else if disabled {
			flash(w, "ok", "Account disabled.")
		} else {
			flash(w, "ok", "Account enabled.")
		}
		http.Redirect(w, req, "/portal/settings/accounts", http.StatusFound)
	}
}

func accountSetPassword(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		if err := r.Users.SetPassword(req.Context(), id, req.FormValue("password")); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Password updated.")
		}
		http.Redirect(w, req, "/portal/settings/accounts", http.StatusFound)
	}
}

func accountDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(req.PathValue("id"), 10, 64)
		if err := r.Users.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Account deleted.")
		}
		http.Redirect(w, req, "/portal/settings/accounts", http.StatusFound)
	}
}
