package portal

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Create/edit form errors. A failed save re-renders the same form in the
// same response (HTTP 422) with the operator's submitted values and the
// error beside the field it concerns — instead of a flash and a redirect
// to a blank form that loses everything they typed. Successful saves keep
// the post-redirect-get pattern. Secrets (passwords, tokens) are never
// echoed back: handlers leave those fields blank on re-render.

// FormError is a failed save carried into the re-rendered form.
type FormError struct {
	// Message is shown at the top of the form.
	Message string
	// Fields maps a form field name to the message shown under it.
	Fields map[string]string
}

// fieldPrefixes maps the leading word of a model validation message to the
// form field it concerns ("name required" → name, "base_url required" →
// base_url). Messages that don't start with a known field stay at the top.
var fieldPrefixes = []struct{ prefix, field string }{
	{"name pattern", "name_regex"},
	{"name_regex", "name_regex"},
	{"name", "name"},
	{"webhook name", "name"},
	{"webhook url", "url"},
	{"os_type", "os_type"},
	{"base_url", "base_url"},
	{"kb_number", "kb_number"},
	{"title", "title"},
	{"settings_json", "settings_json"},
	{"payload", "payload"},
	{"serial and model", "file"},
}

// newFormError builds the FormError for a failed save of a kind entity.
// The raw error is logged; the operator sees the friendly sentence.
func newFormError(req *http.Request, kind string, err error) *FormError {
	slog.Info("portal.form.invalid", slog.String("path", req.URL.Path),
		slog.String("kind", kind), slog.String("error", err.Error()))
	msg := friendlyError(kind, http.StatusUnprocessableEntity, err)
	fe := &FormError{Fields: map[string]string{}}
	switch {
	case errors.Is(err, model.ErrConflict):
		fe.Fields["name"] = "Another " + kindNoun(kind) + " already has this name. Choose a different name."
	case errors.Is(err, model.ErrCycle):
		fe.Fields["parent_id"] = msg
	case errors.Is(err, model.ErrValidation):
		low := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(err.Error(), model.ErrValidation.Error()+":")))
		for _, p := range fieldPrefixes {
			if strings.HasPrefix(low, p.prefix+" ") || low == p.prefix {
				fe.Fields[p.field] = msg
				break
			}
		}
	}
	if len(fe.Fields) > 0 {
		fe.Message = "Couldn't save — fix the highlighted field below. Nothing was changed."
	} else {
		fe.Message = "Couldn't save: " + strings.TrimSuffix(msg, ".") + ". Nothing was changed."
	}
	return fe
}

// fieldFormError is a FormError for one field, for checks a handler
// makes itself before calling the repo.
func fieldFormError(field, msg string) *FormError {
	return &FormError{
		Message: "Couldn't save — fix the highlighted field below. Nothing was changed.",
		Fields:  map[string]string{field: msg},
	}
}

// keepSecretNote tells the operator to re-type a secret they entered:
// secret fields are never echoed back into a re-rendered form, so a
// password typed before a failed save is gone. No-op when the field was
// left blank or already carries its own error.
func keepSecretNote(fe *FormError, req *http.Request, field string) {
	if fe == nil || req.FormValue(field) == "" || fe.Fields[field] != "" {
		return
	}
	if fe.Fields == nil {
		fe.Fields = map[string]string{}
	}
	fe.Fields[field] = "For security this isn't kept after a failed save — enter it again."
}

// formErrorFuncs are the template helpers the form partials use. They
// accept any so a page rendered without a FormError (the normal GET)
// passes nil and gets nothing.
func formErrorFuncs() template.FuncMap {
	get := func(v any) *FormError {
		fe, _ := v.(*FormError)
		return fe
	}
	return template.FuncMap{
		// fieldError renders the inline message under a field, or nothing.
		"fieldError": func(v any, field string) template.HTML {
			fe := get(v)
			if fe == nil || fe.Fields[field] == "" {
				return ""
			}
			return template.HTML(`<span class="field-error" id="err-` + template.HTMLEscapeString(field) + `" role="alert">` +
				template.HTMLEscapeString(fe.Fields[field]) + `</span>`)
		},
		// fieldInvalid is true when field has an inline error.
		"fieldInvalid": func(v any, field string) bool {
			fe := get(v)
			return fe != nil && fe.Fields[field] != ""
		},
		// invalidAttr goes inside the field's tag: it marks the control
		// aria-invalid and links it to its message, or renders nothing.
		"invalidAttr": func(v any, field string) template.HTMLAttr {
			fe := get(v)
			if fe == nil || fe.Fields[field] == "" {
				return ""
			}
			return template.HTMLAttr(`aria-invalid="true" aria-describedby="err-` + template.HTMLEscapeString(field) + `"`)
		},
	}
}

// renderForm renders a create/edit form page; with a non-nil FormError
// the response is 422 Unprocessable Entity so scripts and tests can tell
// a rejected save from a fresh form.
func renderForm(w http.ResponseWriter, req *http.Request, r Repos, page, title string, fe *FormError, data map[string]any) {
	data["FormError"] = fe
	if fe == nil {
		render(w, req, r, page, title, data)
		return
	}
	render(&statusWriter{ResponseWriter: w, status: http.StatusUnprocessableEntity}, req, r, page, title, data)
}
