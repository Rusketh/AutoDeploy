package portal

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/addomain"
)

// settings_runtime.go owns the portal pages for AD and operational
// settings that the operator can edit at runtime.

// commonTimezones is a short pick-list of IANA zones offered as datalist
// suggestions on the operational settings page. It is a convenience only —
// the field accepts any IANA name the server's tz database knows.
var commonTimezones = []string{
	"UTC",
	"America/Los_Angeles",
	"America/Denver",
	"America/Chicago",
	"America/New_York",
	"America/Sao_Paulo",
	"Europe/London",
	"Europe/Paris",
	"Europe/Berlin",
	"Europe/Moscow",
	"Asia/Dubai",
	"Asia/Kolkata",
	"Asia/Singapore",
	"Asia/Shanghai",
	"Asia/Tokyo",
	"Australia/Sydney",
	"Pacific/Auckland",
}

func adForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		cfg := addomain.LDAPConfig{}
		if r.Runtime != nil {
			cfg = r.Runtime.ADConfig(req.Context())
		}
		adRender(w, req, r, cfg, nil, nil)
	}
}

// adTestOutcome is a connection test's result shown on the AD page.
type adTestOutcome struct {
	OK  bool
	Msg string
}

// adRender renders the AD page. cfg is what the fields show — the stored
// config, or the submitted values after a rejected save or a test; the
// bind password is never echoed. fe (rejected save) makes it a 422.
func adRender(w http.ResponseWriter, req *http.Request, r Repos, cfg addomain.LDAPConfig, fe *FormError, test *adTestOutcome) {
	var env addomain.LDAPConfig
	stored := false
	if r.Runtime != nil {
		e := r.Runtime.EnvDefaults()
		env = addomain.LDAPConfig{
			URL: e.ADLDAPURL, BindDN: e.ADBindDN,
			SearchBase: e.ADSearchBase, SkipTLSVerify: e.ADSkipTLSVerify,
		}
		stored = r.Runtime.ADEnabled()
	}
	cfg.BindPassword = ""
	data := map[string]any{"Cfg": cfg, "Env": env, "Stored": stored, "Test": test}
	if test != nil {
		// A test isn't a save: show the result at 200, with only the
		// "type the password again" note if one was typed.
		note := &FormError{}
		keepSecretNote(note, req, "bind_password")
		data["FormError"] = note
		render(w, req, r, "settings_ad.html", "Active Directory", data)
		return
	}
	renderForm(w, req, r, "settings_ad.html", "Active Directory", fe, data)
}

// adConfigFromForm reads the AD form fields.
func adConfigFromForm(req *http.Request) addomain.LDAPConfig {
	return addomain.LDAPConfig{
		URL:           strings.TrimSpace(req.FormValue("url")),
		BindDN:        strings.TrimSpace(req.FormValue("bind_dn")),
		BindPassword:  req.FormValue("bind_password"), // empty = preserve
		SearchBase:    strings.TrimSpace(req.FormValue("search_base")),
		SkipTLSVerify: req.FormValue("skip_tls_verify") == "1",
	}
}

func adSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			failPage(w, req, r, http.StatusInternalServerError, errors.New("runtime settings unavailable"))
			return
		}
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		// "disable" button — clear every AD setting.
		if req.FormValue("disable") == "1" {
			if err := r.Runtime.SetADConfig(req.Context(), addomain.LDAPConfig{}); err != nil {
				flash(w, "err", err.Error())
			} else {
				forgetADTest()
				flash(w, "ok", "AD integration disabled.")
			}
			http.Redirect(w, req, "/portal/settings/active-directory", http.StatusFound)
			return
		}
		cfg := adConfigFromForm(req)
		var fe *FormError
		low := strings.ToLower(cfg.URL)
		switch {
		case cfg.URL == "":
			fe = fieldFormError("url", "Enter the domain controller's address, e.g. ldaps://dc.corp.example:636 — or use Disable AD.")
		case !strings.HasPrefix(low, "ldap://") && !strings.HasPrefix(low, "ldaps://"):
			fe = fieldFormError("url", "Start the address with ldaps:// (or ldap://), e.g. ldaps://dc.corp.example:636.")
		}
		if fe == nil {
			if err := r.Runtime.SetADConfig(req.Context(), cfg); err != nil {
				fe = newFormError(req, "", err)
			}
		}
		if fe != nil {
			keepSecretNote(fe, req, "bind_password")
			adRender(w, req, r, cfg, fe, nil)
			return
		}
		forgetADTest()
		flash(w, "ok", "Active Directory settings saved. Changes take effect immediately.")
		http.Redirect(w, req, "/portal/settings/active-directory", http.StatusFound)
	}
}

// adTest performs a one-shot bind against the configured AD URL using
// the form's current values (or the stored values when password is
// empty), and shows the result on the page with the typed values kept.
func adTest(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			failPage(w, req, r, http.StatusInternalServerError, errors.New("runtime settings unavailable"))
			return
		}
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		stored := r.Runtime.ADConfig(req.Context())
		form := adConfigFromForm(req)
		cfg := form
		// Empty password in the form means "use the stored value".
		if cfg.BindPassword == "" {
			cfg.BindPassword = stored.BindPassword
		}
		// Empty URL means "use stored" (operator testing the saved
		// config without re-typing it).
		if cfg.URL == "" {
			cfg = stored
			form = stored
		}
		if cfg.URL == "" {
			adRender(w, req, r, form, nil, &adTestOutcome{Msg: "Nothing to test yet: enter the LDAP URL first."})
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 8*time.Second)
		defer cancel()
		dir := addomain.NewLDAPDirectory(cfg)
		// A FindComputer against an unlikely name forces the dial +
		// bind. We don't care about the result, only that the search
		// succeeds without an LDAP error.
		_, err := dir.FindComputer(ctx, "autodeploy-test-nonexistent-machine")
		// The settings hub shows this result instead of dialling AD itself.
		res := adTestResult{URL: cfg.URL, OK: err == nil, At: time.Now()}
		if err != nil {
			res.Err = err.Error()
		}
		rememberADTest(res)
		if err != nil {
			adRender(w, req, r, form, nil, &adTestOutcome{Msg: "Connection test failed: " + err.Error()})
			return
		}
		adRender(w, req, r, form, nil, &adTestOutcome{OK: true, Msg: "Connected and signed in. AD is reachable with these settings."})
	}
}

func opsForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var retentionDays, throttle, checkInSeconds int
		var timezone string
		if r.Runtime != nil {
			retentionDays = r.Runtime.LogRetentionDays()
			throttle = r.Runtime.PayloadMaxInFlight()
			checkInSeconds = r.Runtime.AgentPollIntervalSeconds()
			timezone = r.Runtime.DisplayTimezone()
		}
		opsRender(w, req, r, nil, map[string]any{
			"RetentionDays":  retentionDays,
			"Throttle":       throttle,
			"CheckInSeconds": checkInSeconds,
			"Timezone":       timezone,
		})
	}
}

func opsRender(w http.ResponseWriter, req *http.Request, r Repos, fe *FormError, data map[string]any) {
	data["CommonZones"] = commonTimezones
	renderForm(w, req, r, "settings_ops.html", "Operational settings", fe, data)
}

// opsNumber parses one optional whole-number field (blank = leave as is).
func opsNumber(fe *FormError, req *http.Request, field string, min int, msg string) (int, bool) {
	v := strings.TrimSpace(req.FormValue(field))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		fe.Fields[field] = msg
		return 0, false
	}
	return n, true
}

func opsSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Runtime == nil {
			failPage(w, req, r, http.StatusInternalServerError, errors.New("runtime settings unavailable"))
			return
		}
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		// Check every field before saving any, so a bad value changes
		// nothing and each problem shows beside its field.
		fe := &FormError{Fields: map[string]string{}}
		retention, setRetention := opsNumber(fe, req, "retention_days", 0, "Enter a whole number of days (0 keeps logs forever).")
		throttle, setThrottle := opsNumber(fe, req, "payload_max_in_flight", 0, "Enter a whole number (0 means no limit).")
		poll, setPoll := opsNumber(fe, req, "agent_poll_seconds", 5, "Enter a whole number of seconds, 5 or more.")
		tz := strings.TrimSpace(req.FormValue("display_timezone"))
		if tz != "" {
			if _, err := time.LoadLocation(tz); err != nil {
				fe.Fields["display_timezone"] = "Unknown time zone. Use a name like Europe/London or America/New_York, or leave blank for UTC."
			}
		}
		save := func(err error) {
			if err != nil && fe.Message == "" {
				fe.Message = "Couldn't save: " + strings.TrimSuffix(friendlyError("", http.StatusUnprocessableEntity, err), ".") + "."
			}
		}
		if len(fe.Fields) == 0 {
			if setRetention {
				save(r.Runtime.SetLogRetentionDays(req.Context(), retention))
			}
			if setThrottle {
				save(r.Runtime.SetPayloadMaxInFlight(req.Context(), throttle))
			}
			if setPoll {
				save(r.Runtime.SetAgentPollIntervalSeconds(req.Context(), poll))
			}
			// Always processed (even when blank) so an operator can clear
			// it back to UTC.
			save(r.Runtime.SetDisplayTimezone(req.Context(), tz))
		} else {
			fe.Message = "Couldn't save — fix the highlighted fields below. Nothing was changed."
		}
		if fe.Message != "" {
			opsRender(w, req, r, fe, map[string]any{
				"RetentionDays":  req.FormValue("retention_days"),
				"Throttle":       req.FormValue("payload_max_in_flight"),
				"CheckInSeconds": req.FormValue("agent_poll_seconds"),
				"Timezone":       tz,
			})
			return
		}
		flash(w, "ok", "Saved. Retention takes effect on the next hourly tick; payload throttle on the next server restart; the agent check-in interval on each agent's next poll. The display time zone applies immediately.")
		http.Redirect(w, req, "/portal/settings/operational", http.StatusFound)
	}
}
