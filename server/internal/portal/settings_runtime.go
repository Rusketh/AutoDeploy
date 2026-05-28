package portal

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/addomain"
)

// settings_runtime.go owns the portal pages for AD and operational
// settings that the operator can edit at runtime.

func adForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		cfg := addomain.LDAPConfig{}
		if r.Runtime != nil {
			cfg = r.Runtime.ADConfig(req.Context())
		}
		var env addomain.LDAPConfig
		if r.Runtime != nil {
			e := r.Runtime.EnvDefaults()
			env = addomain.LDAPConfig{
				URL: e.ADLDAPURL, BindDN: e.ADBindDN,
				SearchBase: e.ADSearchBase, SkipTLSVerify: e.ADSkipTLSVerify,
			}
		}
		render(w, req, r, "settings_ad.html", "Active Directory", map[string]any{
			"Cfg": cfg, "Env": env,
		})
	}
}

func adSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			http.Error(w, "runtime settings unavailable", http.StatusInternalServerError)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// "disable" button — clear every AD setting.
		if req.FormValue("disable") == "1" {
			if err := r.Runtime.SetADConfig(req.Context(), addomain.LDAPConfig{}); err != nil {
				flash(w, "err", err.Error())
			} else {
				flash(w, "ok", "AD integration disabled.")
			}
			http.Redirect(w, req, "/portal/settings/active-directory", http.StatusFound)
			return
		}
		cfg := addomain.LDAPConfig{
			URL:           strings.TrimSpace(req.FormValue("url")),
			BindDN:        strings.TrimSpace(req.FormValue("bind_dn")),
			BindPassword:  req.FormValue("bind_password"), // empty = preserve
			SearchBase:    strings.TrimSpace(req.FormValue("search_base")),
			SkipTLSVerify: req.FormValue("skip_tls_verify") == "1",
		}
		if cfg.URL == "" {
			flash(w, "err", "URL required (or click Disable).")
			http.Redirect(w, req, "/portal/settings/active-directory", http.StatusFound)
			return
		}
		if err := r.Runtime.SetADConfig(req.Context(), cfg); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Active Directory settings saved. Changes take effect immediately.")
		}
		http.Redirect(w, req, "/portal/settings/active-directory", http.StatusFound)
	}
}

// adTest performs a one-shot bind against the configured AD URL using
// the form's current values (or the stored values when password is
// empty). Returns ok / error inline.
func adTest(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			http.Error(w, "runtime settings unavailable", http.StatusInternalServerError)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		stored := r.Runtime.ADConfig(req.Context())
		cfg := addomain.LDAPConfig{
			URL:           strings.TrimSpace(req.FormValue("url")),
			BindDN:        strings.TrimSpace(req.FormValue("bind_dn")),
			BindPassword:  req.FormValue("bind_password"),
			SearchBase:    strings.TrimSpace(req.FormValue("search_base")),
			SkipTLSVerify: req.FormValue("skip_tls_verify") == "1",
		}
		// Empty password in the form means "use the stored value".
		if cfg.BindPassword == "" {
			cfg.BindPassword = stored.BindPassword
		}
		// Empty URL means "use stored" (operator testing the saved
		// config without re-typing it).
		if cfg.URL == "" {
			cfg = stored
		}
		if cfg.URL == "" {
			flash(w, "err", "Test: nothing configured.")
			http.Redirect(w, req, "/portal/settings/active-directory", http.StatusFound)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 8*time.Second)
		defer cancel()
		dir := addomain.NewLDAPDirectory(cfg)
		// A FindComputer against an unlikely name forces the dial +
		// bind. We don't care about the result, only that the search
		// succeeds without an LDAP error.
		_, err := dir.FindComputer(ctx, "autodeploy-test-nonexistent-machine")
		if err != nil {
			flash(w, "err", "Test failed: "+err.Error())
		} else {
			flash(w, "ok", "Bind succeeded. AD is reachable.")
		}
		http.Redirect(w, req, "/portal/settings/active-directory", http.StatusFound)
	}
}

func opsForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var retentionDays, throttle int
		if r.Runtime != nil {
			retentionDays = r.Runtime.LogRetentionDays()
			throttle = r.Runtime.PayloadMaxInFlight()
		}
		render(w, req, r, "settings_ops.html", "Operational settings", map[string]any{
			"RetentionDays": retentionDays,
			"Throttle":      throttle,
		})
	}
}

func opsSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			http.Error(w, "runtime settings unavailable", http.StatusInternalServerError)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if v := req.FormValue("retention_days"); v != "" {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				flash(w, "err", "retention_days must be a number")
				http.Redirect(w, req, "/portal/settings/operational", http.StatusFound)
				return
			}
			if err := r.Runtime.SetLogRetentionDays(req.Context(), n); err != nil {
				flash(w, "err", err.Error())
				http.Redirect(w, req, "/portal/settings/operational", http.StatusFound)
				return
			}
		}
		if v := req.FormValue("payload_max_in_flight"); v != "" {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				flash(w, "err", "payload_max_in_flight must be a number")
				http.Redirect(w, req, "/portal/settings/operational", http.StatusFound)
				return
			}
			if err := r.Runtime.SetPayloadMaxInFlight(req.Context(), n); err != nil {
				flash(w, "err", err.Error())
				http.Redirect(w, req, "/portal/settings/operational", http.StatusFound)
				return
			}
		}
		flash(w, "ok", "Saved. Retention takes effect on the next hourly tick. Payload throttle takes effect on the next server restart.")
		http.Redirect(w, req, "/portal/settings/operational", http.StatusFound)
	}
}
