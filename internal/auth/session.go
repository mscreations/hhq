package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/mscreations/hhq/internal/models"
)

const cookieName = "hhq_session"

type contextKey string

const userContextKey contextKey = "currentUser"

// SessionManager ties together the DB-backed session store with HTTP cookie handling.
type SessionManager struct {
	Sessions *models.SessionStore
	Users    *models.UserStore
	TTL      time.Duration
	// Secure controls the cookie's Secure flag. Set via config.Config.CookieSecure,
	// which defaults to true when PublicBaseURL is https:// (overridable with the
	// COOKIE_SECURE env var) rather than being hardcoded, since this app's pod may
	// be reachable without Traefik in front of it in some deployments.
	Secure bool
}

// Login creates a session and sets the cookie on the response.
func (sm *SessionManager) Login(w http.ResponseWriter, r *http.Request, userID int) error {
	token, err := sm.Sessions.Create(r.Context(), userID, sm.TTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   sm.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sm.TTL),
	})
	return nil
}

func (sm *SessionManager) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = sm.Sessions.Delete(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// LoadUser is middleware that, if a valid session cookie is present, attaches the
// corresponding *models.User to the request context. It never blocks the request —
// pages that require a parent should use RequireParent below.
func (sm *SessionManager) LoadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		sess, err := sm.Sessions.Get(r.Context(), c.Value)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		user, err := sm.Users.GetByID(r.Context(), sess.UserID)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireParent redirects to /login if there's no authenticated parent in context,
// or to /setup if no parent user exists yet (first-run: there's no account to log
// into, so sending the visitor to /login would be a dead end). Use this to protect
// the parent dashboard and settings routes. The kiosk routes deliberately do NOT
// use this middleware, per the "view only/no login on kiosk" requirement.
func (sm *SessionManager) RequireParent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil || user.Role != models.RoleParent {
			existing, err := sm.Users.ListParents(r.Context())
			if err == nil && len(existing) == 0 {
				http.Redirect(w, r, "/setup", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func UserFromContext(ctx context.Context) *models.User {
	u, _ := ctx.Value(userContextKey).(*models.User)
	return u
}

// SessionToken returns the raw session cookie value for the current request,
// if any. Used to derive a per-session CSRF token (see CSRFManager) without
// needing separate storage for it.
func (sm *SessionManager) SessionToken(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	return c.Value, true
}
