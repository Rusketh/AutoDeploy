package portal

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
)

// Portal sessions use a sliding idle timeout (see auth.TouchSession):
// every page view or form post pushes the expiry forward, up to an
// absolute cap from sign-in. Background polling doesn't count as
// activity, so an open-but-idle tab still times out. Each page carries
// its expiry in <meta name="ad-session-expires">; app.js shows a "Stay
// signed in" banner shortly before it and calls the keep-alive endpoint.

// ctxKeySession is the private context key for the request's session.
type ctxKeySession struct{}

// sessionFromRequest validates the session cookie. Navigations and form
// posts record activity (sliding the expiry and refreshing the cookie);
// background GETs only check it.
func sessionFromRequest(w http.ResponseWriter, req *http.Request, r Repos) (auth.User, auth.SessionInfo, bool) {
	c, err := req.Cookie(sessionCookieName)
	if err != nil {
		return auth.User{}, auth.SessionInfo{}, false
	}
	var info auth.SessionInfo
	if isBackgroundRequest(req) && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
		info, err = r.Users.SessionStatus(req.Context(), c.Value)
	} else {
		info, err = r.Users.TouchSession(req.Context(), c.Value)
	}
	if err != nil {
		return auth.User{}, auth.SessionInfo{}, false
	}
	u, err := r.Users.GetUser(req.Context(), info.UserID)
	if err != nil {
		return auth.User{}, auth.SessionInfo{}, false
	}
	if info.Extended {
		setSessionCookie(w, req, c.Value, info.CreatedAt)
	}
	return u, info, true
}

// setSessionCookie writes the session cookie. Its browser lifetime is the
// session's absolute deadline; the server-side idle expiry decides
// whether it is still valid.
func setSessionCookie(w http.ResponseWriter, req *http.Request, token string, created time.Time) {
	if created.IsZero() {
		created = time.Now()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  auth.SessionDeadline(created),
		Secure:   req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https",
	})
}

// redirectToLogin sends a request without a valid session to the sign-in
// page. "next" is where to return afterwards: the requested page for a
// GET; for a POST (a form submitted after the session lapsed) the page
// the form was on — the same-origin Referer — or failing that the
// section's list page, never the POST URL itself. Script requests get a
// 401 instead of an HTML redirect.
func redirectToLogin(w http.ResponseWriter, req *http.Request) {
	_, cookieErr := req.Cookie(sessionCookieName)
	if isBackgroundRequest(req) {
		http.Error(w, "session expired — sign in again", http.StatusUnauthorized)
		return
	}
	q := url.Values{}
	next := req.URL.RequestURI()
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		next = formPageFor(req)
		q.Set("expired", "post")
	} else if cookieErr == nil {
		q.Set("expired", "1")
	}
	q.Set("next", next)
	http.Redirect(w, req, "/portal/login?"+q.Encode(), http.StatusFound)
}

// formPageFor is the GET page a POST came from: the Referer when it is a
// portal page on this host, else the list page of the URL's section.
func formPageFor(req *http.Request) string {
	if ref := req.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == req.Host &&
			strings.HasPrefix(u.Path, "/portal/") && u.Path != "/portal/login" {
			return u.RequestURI()
		}
	}
	return sectionFor(req.URL.Path).url
}

// safeNext keeps a post-login redirect inside the portal.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/portal/") || strings.HasPrefix(next, "//") {
		return "/portal/"
	}
	return next
}

// loginNotice is the sign-in page's explanation for ?expired=.
func loginNotice(expired string) string {
	switch expired {
	case "post":
		return "Your session expired — sign in again; your changes were not saved."
	case "1":
		return "Your session expired — sign in again."
	}
	return ""
}

// sessionKeepAlive is the "Stay signed in" endpoint: it always extends
// the session to a full idle window (bypassing the once-a-minute
// throttle) and reports the new expiry so the page can re-arm its
// warning.
func sessionKeepAlive(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		info, _ := req.Context().Value(ctxKeySession{}).(auth.SessionInfo)
		if c, err := req.Cookie(sessionCookieName); err == nil {
			if ext, err := r.Users.ExtendSession(req.Context(), c.Value); err == nil {
				info = ext
				setSessionCookie(w, req, c.Value, ext.CreatedAt)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"expires_at": info.ExpiresAt.UnixMilli(),
			"now":        time.Now().UnixMilli(),
		})
	}
}

// sessionExpiresMillis is the request's session expiry (Unix ms) for the
// page's meta tag, or 0 when there is none.
func sessionExpiresMillis(req *http.Request) int64 {
	info, ok := req.Context().Value(ctxKeySession{}).(auth.SessionInfo)
	if !ok || info.ExpiresAt.IsZero() {
		return 0
	}
	return info.ExpiresAt.UnixMilli()
}
