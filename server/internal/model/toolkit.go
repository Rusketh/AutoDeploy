package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// Run As modes for a Tool Kit. RunAsKit is a deploy-time choice only: "use
// whatever the kit is configured with".
const (
	RunAsKit          = "kit"
	RunAsSystem       = "system"
	RunAsUser         = "user"
	RunAsLoggedOnUser = "logged_on_user"
)

// Reboot policies applied by the agent once a Tool Kit run finishes.
const (
	RebootNever       = "never"
	RebootIfRequested = "if_requested" // a step exited 3010 or 1641
	RebootAlways      = "always"
)

// DefaultToolkitTimeoutMinutes is the timeout a new kit starts with.
const DefaultToolkitTimeoutMinutes = 60

// MaxToolkitTimeoutMinutes caps the configurable timeout (24 hours).
const MaxToolkitTimeoutMinutes = 24 * 60

// RunAsLabel is the human label for a Run As mode.
func RunAsLabel(mode string) string {
	switch mode {
	case RunAsSystem, "":
		return "SYSTEM"
	case RunAsUser:
		return "Specific account"
	case RunAsLoggedOnUser:
		return "Logged-on user"
	case RunAsKit:
		return "Tool kit default"
	}
	return mode
}

// RebootLabel is the human label for a reboot policy.
func RebootLabel(p string) string {
	switch p {
	case RebootNever, "":
		return "Never"
	case RebootIfRequested:
		return "If a step requests it (3010 / 1641)"
	case RebootAlways:
		return "Always"
	}
	return p
}

// ToolkitParam is one named parameter a Tool Kit declares. Its value is
// substituted for %NAME% (case-insensitive) in every step field before the
// steps are handed to the agent. Default is used unless the operator
// overrides it at deploy time; Required rejects a run whose value is blank.
type ToolkitParam struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Default  string `json:"default,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// DisplayLabel is the parameter's label, falling back to its name.
func (p ToolkitParam) DisplayLabel() string {
	if strings.TrimSpace(p.Label) != "" {
		return p.Label
	}
	return p.Name
}

// Toolkit is a reusable, run-once payload: files + ordered steps (the
// software-package step format) with a default Run As account.
type Toolkit struct {
	ID          ID             `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	StepsJSON   string         `json:"steps_json"`
	Params      []ToolkitParam `json:"params"`
	RunAsMode   string         `json:"run_as_mode"`
	RunAsUser   string         `json:"run_as_user"`
	// HasPassword reports that a Run As password is stored, without exposing it.
	HasPassword    bool      `json:"has_password"`
	TimeoutMinutes int       `json:"timeout_minutes"`
	RebootPolicy   string    `json:"reboot_policy"`
	Revision       int       `json:"revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ToolkitRepo stores Tool Kits and the per-operation Run As password
// overrides. Passwords are encrypted at rest with the shared secrets.Box.
type ToolkitRepo struct {
	db *storage.DB
	bx *secrets.Box
}

// NewToolkitRepo binds the repo to a storage handle and a secrets box. A nil
// box disables password storage (saving a password then fails validation).
func NewToolkitRepo(db *storage.DB, bx *secrets.Box) *ToolkitRepo {
	return &ToolkitRepo{db: db, bx: bx}
}

// paramNameRE is the allowed parameter name shape: an identifier, so %NAME%
// can't be confused with a path or collide with punctuation.
var paramNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// reservedParamNames can't be declared: they already mean something to the
// agent's step expansion.
var reservedParamNames = map[string]bool{"pkgdir": true, "payload": true}

func validateToolkit(in *Toolkit) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if err := validateName(in.Name); err != nil {
		return err
	}
	if in.StepsJSON == "" {
		in.StepsJSON = "[]"
	}
	if _, err := swspec.ParseSteps(in.StepsJSON); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if in.RunAsMode == "" {
		in.RunAsMode = RunAsSystem
	}
	switch in.RunAsMode {
	case RunAsSystem, RunAsLoggedOnUser:
		in.RunAsUser = ""
	case RunAsUser:
		in.RunAsUser = strings.TrimSpace(in.RunAsUser)
		if err := ValidateRunAsUser(in.RunAsUser); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown run-as mode %q", ErrValidation, in.RunAsMode)
	}
	if in.RebootPolicy == "" {
		in.RebootPolicy = RebootNever
	}
	switch in.RebootPolicy {
	case RebootNever, RebootIfRequested, RebootAlways:
	default:
		return fmt.Errorf("%w: unknown reboot policy %q", ErrValidation, in.RebootPolicy)
	}
	if in.TimeoutMinutes < 0 || in.TimeoutMinutes > MaxToolkitTimeoutMinutes {
		return fmt.Errorf("%w: timeout must be between 0 and %d minutes", ErrValidation, MaxToolkitTimeoutMinutes)
	}
	return validateToolkitParams(in.Params)
}

func validateToolkitParams(params []ToolkitParam) error {
	seen := map[string]bool{}
	for i := range params {
		p := &params[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Label = strings.TrimSpace(p.Label)
		if !paramNameRE.MatchString(p.Name) {
			return fmt.Errorf("%w: parameter %d: name %q must start with a letter or underscore and use only letters, digits and underscores (max 64)", ErrValidation, i+1, p.Name)
		}
		key := strings.ToLower(p.Name)
		if reservedParamNames[key] {
			return fmt.Errorf("%w: parameter %d: %q is reserved", ErrValidation, i+1, p.Name)
		}
		if seen[key] {
			return fmt.Errorf("%w: parameter %q is declared twice", ErrValidation, p.Name)
		}
		seen[key] = true
		if len(p.Default) > 4096 {
			return fmt.Errorf("%w: parameter %q: default longer than 4096 characters", ErrValidation, p.Name)
		}
	}
	return nil
}

// ValidateRunAsUser checks a Run As account name: DOMAIN\user, user@domain
// or a bare local account (.\user is accepted too).
func ValidateRunAsUser(u string) error {
	u = strings.TrimSpace(u)
	if u == "" {
		return fmt.Errorf("%w: run-as account required", ErrValidation)
	}
	if len(u) > 256 {
		return fmt.Errorf("%w: run-as account longer than 256 characters", ErrValidation)
	}
	if strings.ContainsAny(u, "\"/[]:;|=,+*?<>\r\n\t") {
		return fmt.Errorf("%w: run-as account %q contains invalid characters", ErrValidation, u)
	}
	if strings.Count(u, `\`) > 1 || strings.Count(u, "@") > 1 {
		return fmt.Errorf("%w: run-as account %q is not DOMAIN\\user, user@domain or a local account name", ErrValidation, u)
	}
	return nil
}

func marshalParams(p []ToolkitParam) string {
	if len(p) == 0 {
		return "[]"
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func unmarshalParams(s string) []ToolkitParam {
	if s == "" || s == "[]" {
		return nil
	}
	var out []ToolkitParam
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func (r *ToolkitRepo) encrypt(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	if r.bx == nil {
		return "", fmt.Errorf("%w: secrets storage is not configured; cannot store a password", ErrValidation)
	}
	return r.bx.Encrypt(password)
}

// Create inserts a kit. password is the Run As password (RunAsUser mode
// only; ignored otherwise). Returns the stored row.
func (r *ToolkitRepo) Create(ctx context.Context, in Toolkit, password string) (Toolkit, error) {
	if err := validateToolkit(&in); err != nil {
		return Toolkit{}, err
	}
	if in.RunAsMode != RunAsUser {
		password = ""
	}
	cipher, err := r.encrypt(password)
	if err != nil {
		return Toolkit{}, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO toolkit
		    (name, description, steps_json, params_json, run_as_mode, run_as_user,
		     run_as_password_cipher, timeout_minutes, reboot_policy, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		in.Name, in.Description, in.StepsJSON, marshalParams(in.Params), in.RunAsMode,
		in.RunAsUser, cipher, in.TimeoutMinutes, in.RebootPolicy)
	if err != nil {
		if isUniqueErr(err) {
			return Toolkit{}, fmt.Errorf("tool kit %q: %w", in.Name, ErrConflict)
		}
		return Toolkit{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

const toolkitColumns = `id, name, description, steps_json, params_json, run_as_mode,
	run_as_user, run_as_password_cipher != '', timeout_minutes, reboot_policy, revision,
	created_at, updated_at`

func scanToolkit(s interface{ Scan(...any) error }) (Toolkit, error) {
	var v Toolkit
	var params string
	err := s.Scan(&v.ID, &v.Name, &v.Description, &v.StepsJSON, &params, &v.RunAsMode,
		&v.RunAsUser, &v.HasPassword, &v.TimeoutMinutes, &v.RebootPolicy, &v.Revision,
		&v.CreatedAt, &v.UpdatedAt)
	v.Params = unmarshalParams(params)
	return v, err
}

// Get returns one kit.
func (r *ToolkitRepo) Get(ctx context.Context, id ID) (Toolkit, error) {
	v, err := scanToolkit(r.db.QueryRowContext(ctx,
		`SELECT `+toolkitColumns+` FROM toolkit WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Toolkit{}, fmt.Errorf("tool kit %d: %w", id, ErrNotFound)
	}
	return v, err
}

// List returns every kit, by name.
func (r *ToolkitRepo) List(ctx context.Context) ([]Toolkit, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+toolkitColumns+` FROM toolkit ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Toolkit
	for rows.Next() {
		v, err := scanToolkit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Update saves a kit and bumps its revision. An empty password PRESERVES the
// stored one (so a save that doesn't retype it keeps it); a non-empty one
// replaces it. Switching away from the "specific account" mode clears the
// stored password, so a credential never lingers unused.
func (r *ToolkitRepo) Update(ctx context.Context, in Toolkit, password string) error {
	if err := validateToolkit(&in); err != nil {
		return err
	}
	var cipherExpr string
	var args []any
	switch {
	case in.RunAsMode != RunAsUser:
		cipherExpr = "''"
	case password != "":
		c, err := r.encrypt(password)
		if err != nil {
			return err
		}
		cipherExpr = "?"
		args = append(args, c)
	default:
		cipherExpr = "run_as_password_cipher"
	}
	q := `UPDATE toolkit SET name=?, description=?, steps_json=?, params_json=?,
		run_as_mode=?, run_as_user=?, run_as_password_cipher=` + cipherExpr + `,
		timeout_minutes=?, reboot_policy=?, revision=revision+1,
		updated_at=CURRENT_TIMESTAMP
		WHERE id=?`
	all := []any{in.Name, in.Description, in.StepsJSON, marshalParams(in.Params),
		in.RunAsMode, in.RunAsUser}
	all = append(all, args...)
	all = append(all, in.TimeoutMinutes, in.RebootPolicy, in.ID)
	res, err := r.db.ExecContext(ctx, q, all...)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("tool kit %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("tool kit %d: %w", in.ID, ErrNotFound)
	}
	return nil
}

// BumpRevision increments a kit's revision without changing anything else.
// Called when its files change, since what a run executes changed too.
func (r *ToolkitRepo) BumpRevision(ctx context.Context, id ID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE toolkit SET revision=revision+1, updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	return err
}

// Duplicate copies a kit (steps, parameters, Run As account and its stored
// password, timeout, reboot policy) under a new name. Files are copied by the
// caller, which owns blob storage.
func (r *ToolkitRepo) Duplicate(ctx context.Context, id ID, newName string) (Toolkit, error) {
	src, err := r.Get(ctx, id)
	if err != nil {
		return Toolkit{}, err
	}
	src.Name = newName
	if err := validateToolkit(&src); err != nil {
		return Toolkit{}, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO toolkit
		    (name, description, steps_json, params_json, run_as_mode, run_as_user,
		     run_as_password_cipher, timeout_minutes, reboot_policy, revision)
		SELECT ?, description, steps_json, params_json, run_as_mode, run_as_user,
		       run_as_password_cipher, timeout_minutes, reboot_policy, 1
		FROM toolkit WHERE id=?`, src.Name, id)
	if err != nil {
		if isUniqueErr(err) {
			return Toolkit{}, fmt.Errorf("tool kit %q: %w", src.Name, ErrConflict)
		}
		return Toolkit{}, err
	}
	nid, _ := res.LastInsertId()
	return r.Get(ctx, ID(nid))
}

// toolkitOpFilter matches bulk operations that run a given kit.
const toolkitOpFilter = `o.action='toolkit' AND CAST(json_extract(o.payload,'$.toolkit_id') AS INTEGER)=?`

// PendingUse counts what still depends on a kit: bulk operations that are
// scheduled, active or paused, and jobs still queued or running.
func (r *ToolkitRepo) PendingUse(ctx context.Context, id ID) (ops, jobs int, err error) {
	if err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM bulk_operation o
		WHERE `+toolkitOpFilter+` AND o.status IN ('scheduled','active','paused')`,
		int64(id)).Scan(&ops); err != nil {
		return 0, 0, err
	}
	err = r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE `+toolkitOpFilter+` AND j.status IN ('queued','running')`,
		int64(id)).Scan(&jobs)
	return ops, jobs, err
}

// Delete removes a kit. It is refused while any operation that runs it is
// scheduled, active or paused, so a queued job can never point at a kit that
// no longer exists. Finished operations keep their history (the kit's name
// is then shown as deleted).
func (r *ToolkitRepo) Delete(ctx context.Context, id ID) error {
	ops, jobs, err := r.PendingUse(ctx, id)
	if err != nil {
		return err
	}
	if ops > 0 || jobs > 0 {
		return fmt.Errorf("tool kit %d has %d scheduled or running operation(s); cancel them first: %w", id, ops, ErrInUse)
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM toolkit WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("tool kit %d: %w", id, ErrNotFound)
	}
	return nil
}

// RetrievePassword decrypts a kit's Run As password. This is the audited
// secret-access boundary: callers log the fact of retrieval, never the value.
func (r *ToolkitRepo) RetrievePassword(ctx context.Context, id ID) (string, error) {
	var cipher string
	err := r.db.QueryRowContext(ctx,
		`SELECT run_as_password_cipher FROM toolkit WHERE id=?`, id).Scan(&cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("tool kit %d: %w", id, ErrNotFound)
	}
	if err != nil || cipher == "" {
		return "", err
	}
	if r.bx == nil {
		return "", fmt.Errorf("secrets storage is not configured")
	}
	return r.bx.Decrypt(cipher)
}

// SetOperationCredential stores (or replaces) a per-operation Run As
// password override. It is deleted automatically once the operation
// completes or is cancelled.
func (r *ToolkitRepo) SetOperationCredential(ctx context.Context, opID ID, user, password string) error {
	if err := ValidateRunAsUser(user); err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("%w: password required", ErrValidation)
	}
	c, err := r.encrypt(password)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO bulk_credential (operation_id, run_as_user, password_cipher)
		VALUES (?, ?, ?)
		ON CONFLICT(operation_id) DO UPDATE SET
		    run_as_user=excluded.run_as_user,
		    password_cipher=excluded.password_cipher,
		    created_at=CURRENT_TIMESTAMP`,
		int64(opID), strings.TrimSpace(user), c)
	return err
}

// HasOperationCredential reports whether an override password is stored for
// an operation, and for which account (never the password).
func (r *ToolkitRepo) HasOperationCredential(ctx context.Context, opID ID) (user string, ok bool, err error) {
	err = r.db.QueryRowContext(ctx,
		`SELECT run_as_user FROM bulk_credential WHERE operation_id=?`, int64(opID)).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return user, err == nil, err
}

// RetrieveOperationCredential decrypts an operation's override password. The
// audited secret-access boundary, like RetrievePassword.
func (r *ToolkitRepo) RetrieveOperationCredential(ctx context.Context, opID ID) (user, password string, err error) {
	var cipher string
	err = r.db.QueryRowContext(ctx,
		`SELECT run_as_user, password_cipher FROM bulk_credential WHERE operation_id=?`,
		int64(opID)).Scan(&user, &cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("no run-as credential for operation %d: %w", opID, ErrNotFound)
	}
	if err != nil {
		return "", "", err
	}
	if r.bx == nil {
		return "", "", fmt.Errorf("secrets storage is not configured")
	}
	password, err = r.bx.Decrypt(cipher)
	return user, password, err
}

// DeleteOperationCredential removes an operation's override password.
func (r *ToolkitRepo) DeleteOperationCredential(ctx context.Context, opID ID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM bulk_credential WHERE operation_id=?`, int64(opID))
	return err
}

// PurgeFinishedCredentials deletes the override password of every operation
// that has completed or been cancelled. CompleteJob/CancelOperation already
// delete them as the operation finishes; this sweep (run by the bulk
// scheduler) is the backstop. Returns the number removed.
func PurgeFinishedCredentials(ctx context.Context, db *storage.DB) (int64, error) {
	res, err := db.ExecContext(ctx, `
		DELETE FROM bulk_credential WHERE operation_id IN (
		    SELECT id FROM bulk_operation WHERE status IN ('completed','cancelled'))`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- Deploy-time payload -------------------------------------------------

// ToolkitPayload is the bulk-operation payload of a "toolkit" action. It
// carries no secrets: an overriding password lives in bulk_credential and is
// fetched by the agent just in time.
type ToolkitPayload struct {
	ToolkitID ID `json:"toolkit_id"`
	// Revision is the kit revision when the operation was queued; the agent
	// reports the revision it actually ran in the job result.
	Revision int `json:"revision,omitempty"`
	// RunAs is "", "kit" (the kit's own settings), "system", "user" or
	// "logged_on_user". RunAsUser names the account for "user".
	RunAs     string `json:"run_as,omitempty"`
	RunAsUser string `json:"run_as_user,omitempty"`
	// Params overrides parameter values by name (case-insensitive).
	Params map[string]string `json:"params,omitempty"`
	// SkipSucceeded leaves out machines where this kit already succeeded (or
	// still has a pending job), so a recurring or re-evaluated operation runs
	// the kit at most once per machine.
	SkipSucceeded bool `json:"skip_succeeded,omitempty"`
}

// ParseToolkitPayload decodes and checks a toolkit payload.
func ParseToolkitPayload(s string) (ToolkitPayload, error) {
	var p ToolkitPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return p, fmt.Errorf("%w: toolkit payload: %v", ErrValidation, err)
	}
	if p.ToolkitID <= 0 {
		return p, fmt.Errorf("%w: toolkit payload needs a toolkit_id", ErrValidation)
	}
	switch p.RunAs {
	case "", RunAsKit, RunAsSystem, RunAsLoggedOnUser:
		p.RunAsUser = ""
	case RunAsUser:
		if err := ValidateRunAsUser(p.RunAsUser); err != nil {
			return p, err
		}
	default:
		return p, fmt.Errorf("%w: unknown run_as %q", ErrValidation, p.RunAs)
	}
	return p, nil
}

// EffectiveRunAs resolves the mode and account a run uses: the deploy-time
// override when set, otherwise the kit's own configuration.
func EffectiveRunAs(kit Toolkit, p ToolkitPayload) (mode, user string) {
	switch p.RunAs {
	case "", RunAsKit:
		mode, user = kit.RunAsMode, kit.RunAsUser
		if mode == "" {
			mode = RunAsSystem
		}
		return mode, user
	case RunAsUser:
		return RunAsUser, p.RunAsUser
	}
	return p.RunAs, ""
}

// ResolveParams merges a kit's declared parameters with deploy-time
// overrides (matched case-insensitively) and returns name → value. A blank
// required parameter is an error. Overrides naming undeclared parameters are
// ignored.
func ResolveParams(declared []ToolkitParam, overrides map[string]string) (map[string]string, error) {
	lower := make(map[string]string, len(overrides))
	for k, v := range overrides {
		lower[strings.ToLower(strings.TrimSpace(k))] = v
	}
	out := make(map[string]string, len(declared))
	for _, p := range declared {
		v := p.Default
		if o, ok := lower[strings.ToLower(p.Name)]; ok {
			v = o
		}
		if p.Required && strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%w: parameter %s (%s) is required", ErrValidation, p.Name, p.DisplayLabel())
		}
		out[p.Name] = v
	}
	return out, nil
}

// ExpandToolkitSteps substitutes each parameter's value for %NAME%
// (case-insensitive) in every text field of every step. Unknown %TOKENS% are
// left alone so the agent can still expand Windows environment variables and
// %pkgdir%. Values are inserted literally (never re-expanded).
func ExpandToolkitSteps(steps []swspec.InstallStep, values map[string]string) []swspec.InstallStep {
	if len(values) == 0 {
		return steps
	}
	// One pass over each field: every %NAME% is replaced by its value in a
	// single scan, so a value that itself contains %OTHER% is inserted as-is
	// (never re-expanded, whatever order the parameters come in).
	lower := make(map[string]string, len(values))
	for name, v := range values {
		lower[strings.ToLower(name)] = v
	}
	tokenRE := regexp.MustCompile(`%[A-Za-z_][A-Za-z0-9_]*%`)
	ex := func(s string) string {
		if !strings.Contains(s, "%") {
			return s
		}
		return tokenRE.ReplaceAllStringFunc(s, func(tok string) string {
			if v, ok := lower[strings.ToLower(tok[1:len(tok)-1])]; ok {
				return v
			}
			return tok
		})
	}
	exs := func(in []string) []string {
		if in == nil {
			return nil
		}
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = ex(s)
		}
		return out
	}
	out := make([]swspec.InstallStep, len(steps))
	for i, s := range steps {
		s.Description = ex(s.Description)
		s.SourcePath = ex(s.SourcePath)
		s.DestinationPath = ex(s.DestinationPath)
		s.MSIPath = ex(s.MSIPath)
		s.MSIArgs = exs(s.MSIArgs)
		s.APPXPath = ex(s.APPXPath)
		s.APPXDependencies = exs(s.APPXDependencies)
		s.APPXLicense = ex(s.APPXLicense)
		s.ScriptBody = ex(s.ScriptBody)
		s.ExePath = ex(s.ExePath)
		s.ExeArgs = exs(s.ExeArgs)
		s.WingetID = ex(s.WingetID)
		s.WingetArgs = exs(s.WingetArgs)
		out[i] = s
	}
	return out
}

// ---- Run history -----------------------------------------------------------

// ToolkitStepResult is one step's outcome as the agent reports it.
type ToolkitStepResult struct {
	Step        int    `json:"step"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	AsSystem    bool   `json:"as_system,omitempty"`
	ExitCode    int    `json:"exit_code"`
	Skipped     bool   `json:"skipped,omitempty"`
	Error       string `json:"error,omitempty"`
	// Output is a bounded tail of the step's stdout+stderr.
	Output string `json:"output,omitempty"`
}

// ToolkitResult is the result_json a toolkit job reports.
type ToolkitResult struct {
	ToolkitID ID                  `json:"toolkit_id"`
	Revision  int                 `json:"revision"`
	RunAs     string              `json:"run_as"`
	RunAsUser string              `json:"run_as_user,omitempty"`
	Steps     []ToolkitStepResult `json:"steps,omitempty"`
	TimedOut  bool                `json:"timed_out,omitempty"`
	Reboot    string              `json:"reboot,omitempty"`
	Error     string              `json:"error,omitempty"`
	DurationS int                 `json:"duration_s,omitempty"`
}

// ParseToolkitResult decodes a toolkit job's result JSON; ok is false for an
// empty or foreign result (e.g. a cancelled job).
func ParseToolkitResult(s string) (ToolkitResult, bool) {
	var r ToolkitResult
	if s == "" || s == "{}" {
		return r, false
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return r, false
	}
	return r, r.ToolkitID != 0 || len(r.Steps) > 0 || r.Error != ""
}

// ToolkitRun is one toolkit job, for the machine and kit history views.
type ToolkitRun struct {
	JobID         ID
	OperationID   ID
	OperationName string
	MachineID     ID
	CreatedBy     string
	Status        string
	Payload       ToolkitPayload
	Result        ToolkitResult
	HasResult     bool
	QueuedAt      time.Time
	CompletedAt   *time.Time
}

func (r *ToolkitRepo) runs(ctx context.Context, where string, arg any, limit int) ([]ToolkitRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT j.id, j.operation_id, o.name, j.machine_id, o.created_by, j.status,
		       o.payload, j.result_json, j.queued_at, j.completed_at
		FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE o.action='toolkit' AND `+where+`
		ORDER BY j.id DESC LIMIT ?`, arg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolkitRun
	for rows.Next() {
		var v ToolkitRun
		var payload, result string
		var completed sql.NullTime
		if err := rows.Scan(&v.JobID, &v.OperationID, &v.OperationName, &v.MachineID,
			&v.CreatedBy, &v.Status, &payload, &result, &v.QueuedAt, &completed); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &v.Payload)
		v.Result, v.HasResult = ParseToolkitResult(result)
		if completed.Valid {
			t := completed.Time
			v.CompletedAt = &t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RunsForMachine returns a machine's toolkit jobs, newest first.
func (r *ToolkitRepo) RunsForMachine(ctx context.Context, machineID ID, limit int) ([]ToolkitRun, error) {
	return r.runs(ctx, "j.machine_id=?", int64(machineID), limit)
}

// RunsForToolkit returns a kit's jobs across every machine, newest first.
func (r *ToolkitRepo) RunsForToolkit(ctx context.Context, id ID, limit int) ([]ToolkitRun, error) {
	return r.runs(ctx, "CAST(json_extract(o.payload,'$.toolkit_id') AS INTEGER)=?", int64(id), limit)
}

// RunStats summarises every kit's job outcomes for the list page.
type RunStats struct {
	OK, Failed, Pending int
	LastRun             *time.Time
}

// AllRunStats returns per-kit job counts.
func (r *ToolkitRepo) AllRunStats(ctx context.Context) (map[ID]RunStats, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT CAST(json_extract(o.payload,'$.toolkit_id') AS INTEGER), j.status,
		       COUNT(*), MAX(j.queued_at)
		FROM bulk_job j JOIN bulk_operation o ON o.id=j.operation_id
		WHERE o.action='toolkit'
		GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ID]RunStats{}
	for rows.Next() {
		var id ID
		var status string
		var n int
		var last sql.NullString
		if err := rows.Scan(&id, &status, &n, &last); err != nil {
			return nil, err
		}
		s := out[id]
		switch status {
		case "ok":
			s.OK += n
		case "failed":
			s.Failed += n
		case "queued", "running":
			s.Pending += n
		}
		if last.Valid {
			if t := parseDBTime(last.String); !t.IsZero() && (s.LastRun == nil || t.After(*s.LastRun)) {
				s.LastRun = &t
			}
		}
		out[id] = s
	}
	return out, rows.Err()
}
