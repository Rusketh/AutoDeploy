package api

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
)

const sessionCookieName = "autodeploy_session"
const sessionTTL = 12 * time.Hour

// RegisterAuth mounts login, logout and account management endpoints.
func RegisterAuth(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/auth/login", handleLogin(r))
	mux.HandleFunc("POST /api/v1/auth/logout", handleLogout(r))
	mux.HandleFunc("GET /api/v1/auth/me", handleMe(r))

	// Local accounts.
	mux.HandleFunc("GET /api/v1/accounts", handleListAccounts(r))
	mux.HandleFunc("POST /api/v1/accounts", handleCreateAccount(r))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", handleDeleteAccount(r))
	mux.HandleFunc("POST /api/v1/accounts/{id}/disable", handleAccountSetDisabled(r, true))
	mux.HandleFunc("POST /api/v1/accounts/{id}/enable", handleAccountSetDisabled(r, false))
	mux.HandleFunc("POST /api/v1/accounts/{id}/password", handleAccountSetPassword(r))

	// System settings: access PIN.
	mux.HandleFunc("PUT /api/v1/settings/access-pin", handleSetAccessPIN(r))
	mux.HandleFunc("GET /api/v1/settings/access-pin", handleGetAccessPINStatus(r))

	// Boot Client access-PIN validation (no auth — this IS the auth).
	mux.HandleFunc("POST /api/v1/clients/validate-pin", handleValidatePIN(r))
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

var loginLimiter = struct {
	mu      sync.Mutex
	counts  map[string]int
	resetAt time.Time
}{counts: map[string]int{}}

func loginAllowed(ip string) bool {
	const maxAttempts = 10
	const window = time.Minute

	loginLimiter.mu.Lock()
	defer loginLimiter.mu.Unlock()

	now := time.Now()
	if now.After(loginLimiter.resetAt) {
		loginLimiter.counts = map[string]int{}
		loginLimiter.resetAt = now.Add(window)
	}
	loginLimiter.counts[ip]++
	return loginLimiter.counts[ip] <= maxAttempts
}

func handleLogin(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ip, _, _ := net.SplitHostPort(req.RemoteAddr)
		if !loginAllowed(ip) {
			http.Error(w, "too many login attempts", http.StatusTooManyRequests)
			return
		}

		var in loginRequest
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		u, err := r.Users.Authenticate(req.Context(), in.Username, in.Password)
		if err != nil {
			emitLoginFailed(req, r, in.Username, ip)
			// Constant error message — do not leak whether user exists.
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		tok, err := r.Users.CreateSession(req.Context(), u.ID, sessionTTL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    tok,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Now().Add(sessionTTL),
			Secure:   req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https",
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"id":       u.ID,
			"username": u.Username,
		})
	}
}

func handleLogout(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if c, err := req.Cookie(sessionCookieName); err == nil {
			_ = r.Users.DeleteSession(req.Context(), c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:    sessionCookieName,
			Value:   "",
			Path:    "/",
			Expires: time.Unix(0, 0),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleMe(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, ok := UserFromRequest(req, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "username": u.Username})
	}
}

// UserFromRequest pulls the session cookie out of req and resolves it to a
// User via the repo. Returns ok=false if no valid session.
func UserFromRequest(req *http.Request, r Repos) (auth.User, bool) {
	c, err := req.Cookie(sessionCookieName)
	if err != nil {
		return auth.User{}, false
	}
	uid, err := r.Users.LookupSession(req.Context(), c.Value)
	if err != nil {
		return auth.User{}, false
	}
	u, err := r.Users.GetUser(req.Context(), uid)
	if err != nil {
		return auth.User{}, false
	}
	return u, true
}

func handleListAccounts(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, err := r.Users.ListUsers(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, u)
	}
}

type createAccountReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func handleCreateAccount(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in createAccountReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		u, err := r.Users.CreateUser(req.Context(), in.Username, in.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": u.ID, "username": u.Username})
	}
}

func handleDeleteAccount(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.Users.Delete(req.Context(), int64(id)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleAccountSetDisabled(r Repos, disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.Users.SetDisabled(req.Context(), int64(id), disabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type setPasswordReq struct {
	Password string `json:"password"`
}

func handleAccountSetPassword(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var in setPasswordReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := r.Users.SetPassword(req.Context(), int64(id), in.Password); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type setPINReq struct {
	PIN string `json:"pin"` // empty disables
}

func handleSetAccessPIN(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in setPINReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := r.Settings.SetAccessPIN(req.Context(), in.PIN); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": in.PIN != ""})
	}
}

func handleGetAccessPINStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		on, err := r.Settings.AccessPINEnabled(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": on})
	}
}

type validatePINReq struct {
	SystemUUID string `json:"system_uuid"`
	PIN        string `json:"pin"`
}

type validatePINResp struct {
	Granted           bool `json:"granted"`
	LockedOut         bool `json:"locked_out,omitempty"`
	AttemptsRemaining int  `json:"attempts_remaining,omitempty"`
}

func handleValidatePIN(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in validatePINReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if in.SystemUUID == "" {
			http.Error(w, "system_uuid required", http.StatusBadRequest)
			return
		}
		// Rate-limit check first.
		locked, err := r.Settings.PINLockedOut(req.Context(), in.SystemUUID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if locked {
			writeJSON(w, http.StatusTooManyRequests, validatePINResp{
				Granted: false, LockedOut: true,
			})
			return
		}
		on, err := r.Settings.AccessPINEnabled(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !on {
			// No gate configured: every attempt is granted.
			writeJSON(w, http.StatusOK, validatePINResp{Granted: true})
			return
		}
		ok, err := r.Settings.ValidateAccessPIN(req.Context(), in.PIN)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = r.Settings.RecordPINAttempt(req.Context(), in.SystemUUID, ok)
		writeJSON(w, http.StatusOK, validatePINResp{Granted: ok})
	}
}
