package portal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/unattend"
)

func init() {
	registerUnattendRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/unattends", unattendList(r))
		get("/portal/unattends/new", unattendForm(r, model.Unattend{}, true))
		post("/portal/unattends", unattendCreate(r))
		get("/portal/unattends/{id}/edit", unattendEdit(r))
		post("/portal/unattends/{id}", unattendUpdate(r))
		post("/portal/unattends/{id}/delete", unattendDelete(r))
		get("/portal/unattends/{id}/preview", unattendPreview(r))
	}
}

func unattendList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Unattend.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		refs := map[model.ID]int{}
		for _, ua := range v {
			n, _ := r.Unattend.RefCount(req.Context(), ua.ID)
			refs[ua.ID] = n
		}
		render(w, req, r, "unattend_list.html", "Unattends", map[string]any{
			"Unattends": v, "Refs": refs,
		})
	}
}

func unattendForm(r Repos, ua model.Unattend, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		s := unattend.Defaults()
		if ua.SettingsJSON != "" {
			parsed, err := unattend.Parse(ua.SettingsJSON)
			if err == nil {
				s = parsed
			}
		}
		title := "New unattend"
		if !isNew {
			title = "Edit unattend: " + ua.Name
		}
		render(w, req, r, "unattend_form.html", title, map[string]any{
			"Unattend": ua, "S": s, "IsNew": isNew,
			"TargetOSes": unattend.TargetOSes,
			"Locales":    unattend.Locales,
			"Keyboards":  unattend.Keyboards,
			"TimeZones":  unattend.TimeZones,
			"Editions":   unattend.EditionsFor(s.TargetOS),
		})
	}
}

func unattendEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ua, err := r.Unattend.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		unattendForm(r, ua, false)(w, req)
	}
}

func unattendCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ua, err := buildUnattendFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/unattends/new", http.StatusFound)
			return
		}
		out, err := r.Unattend.Create(req.Context(), ua)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/unattends/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Unattend created.")
		http.Redirect(w, req, fmt.Sprintf("/portal/unattends/%d/edit", out.ID), http.StatusFound)
	}
}

func unattendUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ua, err := buildUnattendFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/unattends/%d/edit", id), http.StatusFound)
			return
		}
		ua.ID = id
		if err := r.Unattend.Update(req.Context(), ua); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/unattends/%d/edit", id), http.StatusFound)
	}
}

func unattendDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Unattend.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/unattends", http.StatusFound)
	}
}

// unattendPreview renders the generated unattend.xml inline so the
// operator can see what the form produced.
func unattendPreview(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		ua, err := r.Unattend.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		s, err := unattend.Parse(ua.SettingsJSON)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		x, err := unattend.Generate(s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "unattend_preview.html", "unattend.xml — "+ua.Name, map[string]any{
			"Unattend": ua, "XML": string(x),
		})
	}
}

// buildUnattendFromForm reads the structured form fields and produces a
// model.Unattend whose SettingsJSON is the generated JSON.
func buildUnattendFromForm(req *http.Request) (model.Unattend, error) {
	if err := req.ParseForm(); err != nil {
		return model.Unattend{}, fmt.Errorf("bad form: %w", err)
	}
	s := unattend.Defaults()
	s.TargetOS = formStr(req, "target_os", s.TargetOS)
	s.Locale = formStr(req, "locale", s.Locale)
	s.UILanguage = formStr(req, "ui_language", s.UILanguage)
	s.Keyboard = formStr(req, "keyboard", s.Keyboard)
	s.TimeZone = formStr(req, "time_zone", s.TimeZone)
	s.Edition = formStr(req, "edition", s.Edition)
	s.ProductKey = formStr(req, "product_key", "")
	s.AdminUser = formStr(req, "admin_user", s.AdminUser)
	s.AdminPassword = formStr(req, "admin_password", "")
	s.HideAdmin = formBool(req, "hide_admin")
	s.NameStrategy = formStr(req, "name_strategy", "random")
	s.ComputerName = formStr(req, "computer_name", "")
	s.SkipMachineOOBE = formBool(req, "skip_machine_oobe")
	s.SkipUserOOBE = formBool(req, "skip_user_oobe")
	s.HideEULA = formBool(req, "hide_eula")
	s.HideOEMRegistration = formBool(req, "hide_oem_registration")
	s.HideOnlineAccountScreens = formBool(req, "hide_online_account_screens")
	s.HideWirelessSetup = formBool(req, "hide_wireless_setup")
	s.ProtectYourPC = formInt(req, "protect_your_pc", 3)
	s.BypassNRO = formBool(req, "bypass_nro")
	s.BypassWin11Reqs = formBool(req, "bypass_win11_reqs")

	// Domain join: present only when the toggle is on.
	if formBool(req, "domain_join_enabled") {
		s.DomainJoin = &unattend.DomainJoin{
			Domain:       formStr(req, "domain", ""),
			OU:           formStr(req, "ou", ""),
			JoinUser:     formStr(req, "join_user", ""),
			JoinPassword: formStr(req, "join_password", ""),
		}
	} else {
		s.DomainJoin = nil
	}

	// First-logon commands: repeating triples (order, description, command_line)
	// indexed by an integer suffix the template emits.
	orders := req.Form["flc_order[]"]
	descs := req.Form["flc_desc[]"]
	lines := req.Form["flc_line[]"]
	for i := 0; i < len(orders) && i < len(descs) && i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		ord, _ := strconv.Atoi(strings.TrimSpace(orders[i]))
		s.FirstLogonCommands = append(s.FirstLogonCommands, unattend.FirstLogonCommand{
			Order:       ord,
			Description: strings.TrimSpace(descs[i]),
			CommandLine: line,
		})
	}

	raw, err := json.Marshal(s)
	if err != nil {
		return model.Unattend{}, err
	}
	return model.Unattend{
		Name:         strings.TrimSpace(req.FormValue("name")),
		Description:  strings.TrimSpace(req.FormValue("description")),
		SettingsJSON: string(raw),
	}, nil
}

// Form helpers used across portal handlers.
func formStr(req *http.Request, key, fallback string) string {
	v := strings.TrimSpace(req.FormValue(key))
	if v == "" {
		return fallback
	}
	return v
}

func formBool(req *http.Request, key string) bool {
	v := req.FormValue(key)
	return v == "1" || v == "on" || v == "true"
}

func formInt(req *http.Request, key string, fallback int) int {
	v := strings.TrimSpace(req.FormValue(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
