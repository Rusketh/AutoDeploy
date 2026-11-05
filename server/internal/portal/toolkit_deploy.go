package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// Deploying a Tool Kit -- the "Run tool kit" action on the bulk form and on
// a machine's page. The action payload names the kit, an optional Run As
// override and parameter values. A password typed for an override account
// NEVER goes into the payload, the review page or a log: it is stored
// encrypted against the operation (bulk_credential) once the operation is
// created. Between the bulk form and its review page it is held in a short
// lived in-memory stash and the page carries only an opaque token.

// Form field names.
const (
	tkFieldID            = "toolkit_id"
	tkFieldRunAs         = "toolkit_run_as"
	tkFieldRunAsUser     = "toolkit_run_as_user"
	tkFieldPassword      = "toolkit_run_as_password"
	tkFieldPasswordToken = "toolkit_password_token"
	tkFieldSkipSucceeded = "toolkit_skip_succeeded"
	tkFieldParamPrefix   = "toolkit_param_" // + <kitID> + "_" + <NAME>
)

// toolkitCred is a deploy-time Run As override password to store against the
// operation once it exists. Password is a secret: never log or render it.
type toolkitCred struct {
	User     string
	Password string
}

// ---- Review-step stash -------------------------------------------------------

// credStashTTL bounds how long a typed password waits between the bulk form's
// review page and the final "Queue operation" click.
const credStashTTL = 30 * time.Minute

type stashEntry struct {
	owner, user, password string
	expires               time.Time
}

// credStash holds override passwords in memory, keyed by a random token and
// bound to the portal user who typed them. Lost on restart (the operator is
// then asked to re-type the password).
type credStash struct {
	mu sync.Mutex
	m  map[string]stashEntry
}

var reviewCreds = &credStash{m: map[string]stashEntry{}}

func (s *credStash) put(owner, user, password string) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, e := range s.m {
		if now.After(e.expires) {
			delete(s.m, k)
		}
	}
	s.m[tok] = stashEntry{owner: owner, user: user, password: password, expires: now.Add(credStashTTL)}
	return tok
}

// get returns a stashed password for the same portal user and account.
func (s *credStash) get(tok, owner, user string) (string, bool) {
	if tok == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[tok]
	if !ok || time.Now().After(e.expires) || e.owner != owner || !strings.EqualFold(e.user, user) {
		return "", false
	}
	return e.password, true
}

func (s *credStash) drop(tok string) {
	if tok == "" {
		return
	}
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

func portalUsername(req *http.Request) string {
	if u, ok := req.Context().Value(ctxKeyUser{}).(auth.User); ok {
		return u.Username
	}
	return ""
}

// ---- Parsing the action ------------------------------------------------------

// toolkitParamField is the form field carrying one parameter's value for a
// kit (each kit's parameters are separate inputs so switching the selected
// kit never mixes values).
func toolkitParamField(kitID model.ID, name string) string {
	return tkFieldParamPrefix + strconv.FormatInt(int64(kitID), 10) + "_" + name
}

// toolkitPayloadFromForm builds a toolkit action payload from the submitted
// action fields. existingOp is the operation being edited (0 for a new one):
// an override password already stored for it satisfies the password check.
// The returned cred carries a newly entered password to store.
func toolkitPayloadFromForm(req *http.Request, kits *model.ToolkitRepo, existingOp model.ID) (string, toolkitCred, error) {
	var cred toolkitCred
	if kits == nil {
		return "", cred, fieldErr(tkFieldID, "Tool Kits aren't available on this server.")
	}
	kid, err := strconv.ParseInt(strings.TrimSpace(req.FormValue(tkFieldID)), 10, 64)
	if err != nil || kid <= 0 {
		return "", cred, fieldErr(tkFieldID, "Choose the tool kit to run.")
	}
	kit, err := kits.Get(req.Context(), model.ID(kid))
	if err != nil {
		return "", cred, fieldErr(tkFieldID, "That tool kit no longer exists — choose another.")
	}
	p := model.ToolkitPayload{
		ToolkitID:     kit.ID,
		Revision:      kit.Revision,
		RunAs:         req.FormValue(tkFieldRunAs),
		SkipSucceeded: req.FormValue(tkFieldSkipSucceeded) != "",
	}
	if p.RunAs == "" {
		p.RunAs = model.RunAsKit
	}

	// Parameters: every declared one, from its per-kit input (default when
	// the input wasn't posted at all).
	if len(kit.Params) > 0 {
		vals := map[string]string{}
		for _, dp := range kit.Params {
			field := toolkitParamField(kit.ID, dp.Name)
			if _, posted := req.Form[field]; posted {
				vals[dp.Name] = req.FormValue(field)
			} else {
				vals[dp.Name] = dp.Default
			}
		}
		for _, dp := range kit.Params {
			if dp.Required && strings.TrimSpace(vals[dp.Name]) == "" {
				return "", cred, fieldErr(toolkitParamField(kit.ID, dp.Name), "Enter a value for "+dp.DisplayLabel()+".")
			}
		}
		p.Params = vals
	}

	switch p.RunAs {
	case model.RunAsKit:
		if kit.RunAsMode == model.RunAsUser && !kit.HasPassword {
			return "", cred, fieldErr(tkFieldRunAs, "This tool kit runs as "+kit.RunAsUser+" but has no stored password. Choose “a different account” and enter one, or set it on the tool kit.")
		}
	case model.RunAsSystem, model.RunAsLoggedOnUser:
	case model.RunAsUser:
		user := strings.TrimSpace(req.FormValue(tkFieldRunAsUser))
		if model.ValidateRunAsUser(user) != nil {
			return "", cred, fieldErr(tkFieldRunAsUser, "Enter the account as DOMAIN\\user, user@domain or a local account name.")
		}
		p.RunAsUser = user
		pw := req.FormValue(tkFieldPassword)
		if pw == "" {
			pw, _ = reviewCreds.get(req.FormValue(tkFieldPasswordToken), portalUsername(req), user)
		}
		switch {
		case pw != "":
			cred = toolkitCred{User: user, Password: pw}
		case existingOp > 0 && opHasCredentialFor(req.Context(), kits, existingOp, user):
			// Keep the password already stored for this operation.
		case kit.RunAsMode == model.RunAsUser && kit.HasPassword && strings.EqualFold(kit.RunAsUser, user):
			// The kit's own account: its stored password is used.
		default:
			return "", cred, fieldErr(tkFieldPassword, "Enter the password for "+user+".")
		}
	default:
		return "", cred, fieldErr(tkFieldRunAs, "Choose which account the tool kit runs as.")
	}
	b, _ := json.Marshal(p)
	return string(b), cred, nil
}

func opHasCredentialFor(ctx context.Context, kits *model.ToolkitRepo, opID model.ID, user string) bool {
	u, ok, err := kits.HasOperationCredential(ctx, opID)
	return err == nil && ok && strings.EqualFold(u, user)
}

// storeToolkitCredential saves (or, when the operation no longer runs as a
// specific account, removes) an operation's override password after it was
// created or edited.
func storeToolkitCredential(ctx context.Context, r Repos, op model.BulkOperation, cred toolkitCred) error {
	if r.Toolkits == nil {
		return nil
	}
	if op.Action != model.BulkActionToolkit {
		// An operation edited away from "run tool kit" keeps no password.
		return r.Toolkits.DeleteOperationCredential(ctx, op.ID)
	}
	if cred.Password != "" {
		return r.Toolkits.SetOperationCredential(ctx, op.ID, cred.User, cred.Password)
	}
	p, err := model.ParseToolkitPayload(op.Payload)
	if err != nil {
		return nil
	}
	if p.RunAs != model.RunAsUser {
		return r.Toolkits.DeleteOperationCredential(ctx, op.ID)
	}
	if !opHasCredentialFor(ctx, r.Toolkits, op.ID, p.RunAsUser) {
		// The account changed without a new password: the stored one was for
		// a different account and must not be used.
		return r.Toolkits.DeleteOperationCredential(ctx, op.ID)
	}
	return nil
}

// stashReviewPassword moves a typed override password into the review stash
// and returns the hidden fields to re-post: the password field is dropped
// and the token added.
func stashReviewPassword(req *http.Request, hidden []hiddenField) []hiddenField {
	out := hidden[:0:0]
	for _, h := range hidden {
		if h.Name == tkFieldPassword || h.Name == tkFieldPasswordToken {
			continue
		}
		out = append(out, h)
	}
	if tok := reviewPasswordToken(req); tok != "" {
		out = append(out, hiddenField{Name: tkFieldPasswordToken, Value: tok})
	}
	return out
}

// reviewPasswordToken returns a stash token for the submitted override
// password: a fresh one when a password was typed, else the token already
// carried by the form.
func reviewPasswordToken(req *http.Request) string {
	if req.FormValue("action") != model.BulkActionToolkit || req.FormValue(tkFieldRunAs) != model.RunAsUser {
		return ""
	}
	if pw := req.FormValue(tkFieldPassword); pw != "" {
		return reviewCreds.put(portalUsername(req), strings.TrimSpace(req.FormValue(tkFieldRunAsUser)), pw)
	}
	return req.FormValue(tkFieldPasswordToken)
}

// ---- Picker data -------------------------------------------------------------

type toolkitPickerParam struct {
	Field, Name, Label string
	Required           bool
	Value              string
}

type toolkitPickerKit struct {
	ID                int64
	Name, Description string
	RunAsText         string
	MissingPassword   bool
	Params            []toolkitPickerParam
}

// toolkitPickerKits lists every kit for the action picker, each with its
// parameter inputs pre-filled: the submitted/stored values for the kit the
// form selected, defaults for the rest.
func toolkitPickerKits(ctx context.Context, r Repos, ap map[string]any) []toolkitPickerKit {
	if r.Toolkits == nil {
		return nil
	}
	kits, _ := r.Toolkits.List(ctx)
	selID, _ := ap["ToolkitID"].(int64)
	selVals, _ := ap["ToolkitParams"].(map[string]string)
	out := make([]toolkitPickerKit, 0, len(kits))
	for _, k := range kits {
		v := toolkitPickerKit{
			ID: int64(k.ID), Name: k.Name, Description: k.Description,
			RunAsText:       runAsText(k.RunAsMode, k.RunAsUser),
			MissingPassword: k.RunAsMode == model.RunAsUser && !k.HasPassword,
		}
		for _, p := range k.Params {
			val := p.Default
			if int64(k.ID) == selID {
				if sv, ok := lookupFold(selVals, p.Name); ok {
					val = sv
				}
			}
			v.Params = append(v.Params, toolkitPickerParam{
				Field: toolkitParamField(k.ID, p.Name), Name: p.Name,
				Label: p.DisplayLabel(), Required: p.Required, Value: val,
			})
		}
		out = append(out, v)
	}
	return out
}

func lookupFold(m map[string]string, key string) (string, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// toolkitPrefillDefaults are the picker's empty Tool Kit fields.
func toolkitPrefillDefaults(ap map[string]any) {
	ap["ToolkitID"] = int64(0)
	ap["ToolkitRunAs"] = model.RunAsKit
	ap["ToolkitRunAsUser"] = ""
	ap["ToolkitParams"] = map[string]string{}
	ap["ToolkitSkipSucceeded"] = false
	ap["ToolkitPasswordToken"] = ""
	ap["ToolkitStoredPassword"] = false
}

// toolkitPrefillFromPayload fills the Tool Kit fields from a stored payload.
func toolkitPrefillFromPayload(ap map[string]any, payload string) {
	p, err := model.ParseToolkitPayload(payload)
	if err != nil {
		return
	}
	ap["ToolkitID"] = int64(p.ToolkitID)
	if p.RunAs != "" {
		ap["ToolkitRunAs"] = p.RunAs
	}
	ap["ToolkitRunAsUser"] = p.RunAsUser
	if p.Params != nil {
		ap["ToolkitParams"] = p.Params
	}
	ap["ToolkitSkipSucceeded"] = p.SkipSucceeded
}

// toolkitPrefillFromRequest fills the Tool Kit fields from a submitted form
// (never the password). With stash set (the bulk form, which has a review
// step) a typed password is stashed and its token kept instead.
func toolkitPrefillFromRequest(ap map[string]any, req *http.Request, stash bool) {
	toolkitPrefillDefaults(ap)
	if n, err := strconv.ParseInt(strings.TrimSpace(req.FormValue(tkFieldID)), 10, 64); err == nil {
		ap["ToolkitID"] = n
		vals := map[string]string{}
		prefix := tkFieldParamPrefix + strconv.FormatInt(n, 10) + "_"
		for k, v := range req.Form {
			if strings.HasPrefix(k, prefix) && len(v) > 0 {
				vals[strings.TrimPrefix(k, prefix)] = v[0]
			}
		}
		ap["ToolkitParams"] = vals
	}
	if v := req.FormValue(tkFieldRunAs); v != "" {
		ap["ToolkitRunAs"] = v
	}
	ap["ToolkitRunAsUser"] = strings.TrimSpace(req.FormValue(tkFieldRunAsUser))
	ap["ToolkitSkipSucceeded"] = req.FormValue(tkFieldSkipSucceeded) != ""
	if !stash {
		return
	}
	if tok := reviewPasswordToken(req); tok != "" {
		ap["ToolkitPasswordToken"] = tok
		ap["ToolkitStoredPassword"] = true
	}
}

// ---- Display helpers ---------------------------------------------------------

// runAsText describes a Run As mode and account in words.
func runAsText(mode, user string) string {
	switch mode {
	case model.RunAsUser:
		if user != "" {
			return user
		}
		return "a specific account"
	case model.RunAsLoggedOnUser:
		return "the logged-on user"
	case model.RunAsKit, "":
		if user != "" {
			return user
		}
		if mode == "" {
			return "SYSTEM"
		}
		return "the tool kit's account"
	}
	return "SYSTEM"
}

// toolkitResultFor decodes a toolkit job's result for the toolkit_result
// partial; nil when the job has no toolkit result (queued, cancelled...).
func toolkitResultFor(raw string) *model.ToolkitResult {
	r, ok := model.ParseToolkitResult(raw)
	if !ok {
		return nil
	}
	return &r
}

// toolkitPayloadSummary describes a toolkit operation's settings in words
// for the review and detail pages: account, parameters, skip rule.
type toolkitPayloadSummary struct {
	ToolkitID     model.ID
	RunAs         string
	Params        []string // "NAME = value"
	SkipSucceeded bool
	Revision      int
}

func summarizeToolkitPayload(payload string, kit *model.Toolkit) toolkitPayloadSummary {
	var s toolkitPayloadSummary
	p, err := model.ParseToolkitPayload(payload)
	if err != nil {
		return s
	}
	s.ToolkitID, s.SkipSucceeded, s.Revision = p.ToolkitID, p.SkipSucceeded, p.Revision
	mode, user := p.RunAs, p.RunAsUser
	if kit != nil {
		mode, user = model.EffectiveRunAs(*kit, p)
	}
	s.RunAs = runAsText(mode, user)
	if p.RunAs == "" || p.RunAs == model.RunAsKit {
		s.RunAs += " (tool kit default)"
	}
	keys := make([]string, 0, len(p.Params))
	for k := range p.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.Params = append(s.Params, k+" = "+p.Params[k])
	}
	return s
}
