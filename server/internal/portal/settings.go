package portal

import (
	"errors"
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

		// Network (listen address, TLS, reverse proxy, external URL)
		get("/portal/settings/network", networkForm(r))
		post("/portal/settings/network", networkSubmit(r))
		post("/portal/settings/network/upload-cert", networkUploadCert(r))

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
		cards := hubCards(gatherHubState(req, r))
		attention := 0
		for _, c := range cards {
			if c.Tone == "warn" || c.Tone == "bad" {
				attention++
			}
		}
		render(w, req, r, "settings_index.html", "Settings", map[string]any{
			"Cards":     cards,
			"Attention": attention,
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
		var err error
		if len(pin) > 72 {
			err = errors.New("use at most 72 characters")
		} else {
			err = r.Settings.SetAccessPIN(req.Context(), pin)
		}
		if err != nil {
			// The PIN itself is never echoed back.
			on, _ := r.Settings.AccessPINEnabled(req.Context())
			renderForm(w, req, r, "settings_pin.html", "Access PIN",
				fieldFormError("pin", friendlyError("", http.StatusUnprocessableEntity, err)),
				map[string]any{"On": on})
			return
		}
		if pin == "" {
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
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		b := branding.Brand{
			ProductName:      strings.TrimSpace(req.FormValue("product_name")),
			OrganisationName: strings.TrimSpace(req.FormValue("organisation_name")),
			SupportURL:       strings.TrimSpace(req.FormValue("support_url")),
			SupportPhone:     strings.TrimSpace(req.FormValue("support_phone")),
			LogoDataURL:      req.FormValue("logo_data_url"),
			PrimaryColor:     strings.TrimSpace(req.FormValue("primary_color")),
			OEMManufacturer:  strings.TrimSpace(req.FormValue("oem_manufacturer")),
		}
		logo, ok := branding.SafeLogoURL(b.LogoDataURL)
		if !ok {
			renderForm(w, req, r, "settings_branding.html", "Branding",
				fieldFormError("logo_data_url", "Use an image data URL (data:image/…) or an http(s) address, under 1 MB. A small PNG or SVG works best."),
				map[string]any{"B": b})
			return
		}
		b.LogoDataURL = logo
		if err := r.Branding.Set(req.Context(), b); err != nil {
			renderForm(w, req, r, "settings_branding.html", "Branding", newFormError(req, "", err), map[string]any{"B": b})
			return
		}
		flash(w, "ok", "Branding saved.")
		http.Redirect(w, req, "/portal/settings/branding", http.StatusFound)
	}
}

func accountsList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		accountsRender(w, req, r, nil, "")
	}
}

// accountsRender renders the accounts page; with fe set it is a rejected
// create or password change (422). newUsername refills the add-account
// form; passwords are never echoed.
func accountsRender(w http.ResponseWriter, req *http.Request, r Repos, fe *FormError, newUsername string) {
	users, _ := r.Users.ListUsers(req.Context())
	me, _ := sessionUser(req, r)
	renderForm(w, req, r, "settings_accounts.html", "Accounts", fe, map[string]any{
		"Users": users, "Me": me, "NewUsername": newUsername,
	})
}

func accountCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u := strings.TrimSpace(req.FormValue("username"))
		p := req.FormValue("password")
		switch {
		case u == "":
			accountsRender(w, req, r, fieldFormError("username", "Enter a username."), u)
			return
		case p == "":
			accountsRender(w, req, r, fieldFormError("password", "Enter a password for the new account."), u)
			return
		}
		if _, err := r.Users.CreateUser(req.Context(), u, p); err != nil {
			var fe *FormError
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				fe = fieldFormError("username", "Another account already uses this username.")
			} else {
				fe = newFormError(req, "", err)
			}
			keepSecretNote(fe, req, "password")
			accountsRender(w, req, r, fe, u)
			return
		}
		flash(w, "ok", "Account created.")
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
		p := req.FormValue("password")
		err := errors.New("enter a new password")
		if p != "" {
			err = r.Users.SetPassword(req.Context(), id, p)
		}
		if err != nil {
			who := "the account"
			if u, gerr := r.Users.GetUser(req.Context(), id); gerr == nil {
				who = u.Username
			}
			msg := strings.TrimSuffix(friendlyError("", http.StatusUnprocessableEntity, err), ".")
			accountsRender(w, req, r, &FormError{
				Message: "Couldn't change the password for " + who + ": " + msg + ". Nothing was changed.",
			}, "")
			return
		}
		flash(w, "ok", "Password updated.")
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
