package handlers

import (
	"net/http"
	"strings"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

type forgotPasswordViewData struct {
	Sent     bool
	Error    string
	Email    string
	AppTitle string
}

func (a *App) ForgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	a.renderFragment(w, "parent/forgot_password", forgotPasswordViewData{AppTitle: a.appTitle(r.Context())})
}

// ForgotPasswordSubmit always renders the same "check your email" response
// regardless of whether the submitted address matches a real parent account -
// revealing that distinction would let an attacker enumerate valid parent
// emails. The actual lookup+send for a matched email is dispatched to the
// background (sendPasswordResetEmailAsync) rather than awaited here, so a
// match and a non-match take the same amount of time to respond to - an
// attacker measuring response latency can't use it as a side channel to
// distinguish the two, which a synchronous SMTP round-trip only on the
// matched branch would otherwise allow.
func (a *App) ForgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	reqEmail := r.FormValue("email")
	appTitle := a.appTitle(r.Context())

	// Rate-limit by both IP and email, same shape as LoginSubmit, so this
	// endpoint (which triggers sending an email) can't be hammered to spam
	// an inbox or enumerate accounts via timing. Unlike LoginSubmit, this
	// deliberately never calls Reset on a "success" - a matched email is
	// still a sensitive, mail-sending action, so each key is capped at
	// LoginLimiter's max attempts per window regardless of outcome, rather
	// than only counting failures the way a login attempt does.
	ipKey := "reset-ip:" + r.RemoteAddr
	emailKey := "reset-email:" + strings.ToLower(reqEmail)
	if !a.LoginLimiter.Allow(ipKey) || !a.LoginLimiter.Allow(emailKey) {
		logging.Warnf("forgot-password: rate limit exceeded for %q from %s", reqEmail, r.RemoteAddr)
		a.renderFragment(w, "parent/forgot_password", forgotPasswordViewData{Sent: true, AppTitle: appTitle})
		return
	}
	a.LoginLimiter.RecordFailure(ipKey)
	a.LoginLimiter.RecordFailure(emailKey)

	user, err := a.Users.GetByEmail(r.Context(), reqEmail)
	if err == nil && user.Role == models.RoleParent && user.IsActive {
		a.sendPasswordResetEmailAsync(user.ID, user.Name, user.Email.String, user.PasswordHash.String, appTitle)
	} else {
		logging.Infof("forgot-password: request for unknown/non-parent email %q", reqEmail)
	}

	a.renderFragment(w, "parent/forgot_password", forgotPasswordViewData{Sent: true, AppTitle: appTitle})
}

// sendPasswordResetEmailAsync builds and sends the reset email in the
// background, so ForgotPasswordSubmit can respond immediately regardless of
// whether a real account matched (see that function's doc comment). Mirrors
// the syncAccountAsync/refreshWeatherAsync pattern used elsewhere in this
// package for background work kicked off from a request handler.
func (a *App) sendPasswordResetEmailAsync(userID int, recipientName, recipientEmail, currentPasswordHash, appTitle string) {
	go func() {
		if err := a.sendPasswordResetEmail(userID, recipientName, recipientEmail, currentPasswordHash, appTitle); err != nil {
			logging.Errorf("forgot-password: sending reset email to %q: %v", recipientEmail, err)
			return
		}
		logging.Infof("forgot-password: sent reset email to %q", recipientEmail)
	}()
}

// sendPasswordResetEmail signs a reset token bound to the user's current
// password hash (see auth.ApprovalLinkSigner.SignWithContext - this makes
// the token self-invalidate the moment the password actually changes, so it
// can't be replayed after a successful reset, or after any other password
// change) and emails it.
func (a *App) sendPasswordResetEmail(userID int, recipientName, recipientEmail, currentPasswordHash, appTitle string) error {
	token := a.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, a.Cfg.PasswordResetLinkTTL, currentPasswordHash)
	resetURL := a.Cfg.PublicBaseURL + "/reset-password?token=" + token

	subject, htmlBody, err := email.RenderPasswordResetEmail(email.PasswordResetEmailData{
		AppName:       appTitle,
		RecipientName: recipientName,
		ResetURL:      resetURL,
	})
	if err != nil {
		return err
	}
	return a.Mailer.Send(appTitle, []string{recipientEmail}, subject, htmlBody)
}

type resetPasswordViewData struct {
	Token    string
	Error    string
	AppTitle string
}

// loadPendingReset verifies the token and returns the target parent user, or
// writes an appropriate response itself and returns ok=false. The token is
// verified against the user's *current* password hash (VerifyWithContext),
// so a token issued for a since-changed password - including one already
// consumed by an earlier successful reset - is rejected here rather than
// being replayable until its TTL expires.
func (a *App) loadPendingReset(w http.ResponseWriter, r *http.Request, token string) (user *models.User, ok bool) {
	invalid := resetPasswordViewData{
		Error:    "This password reset link is invalid or has expired. Request a new one.",
		AppTitle: a.appTitle(r.Context()),
	}

	// PeekID is unverified - it only tells us which user row to load so we
	// can check the token's signature against that user's current password
	// hash below. An attacker-forged id can't produce a token that passes
	// VerifyWithContext without knowing the signing secret.
	id, peekOK := auth.PeekID(token)
	if !peekOK {
		a.renderFragment(w, "parent/reset_password", invalid)
		return nil, false
	}

	u, err := a.Users.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		a.renderFragment(w, "parent/reset_password", invalid)
		return nil, false
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}

	userID, action, err := a.PasswordReset.VerifyWithContext(token, u.PasswordHash.String)
	if err != nil || action != auth.ActionPasswordReset || userID != u.ID {
		a.renderFragment(w, "parent/reset_password", invalid)
		return nil, false
	}
	if u.Role != models.RoleParent || !u.IsActive {
		a.renderFragment(w, "parent/reset_password", invalid)
		return nil, false
	}
	return u, true
}

func (a *App) ResetPasswordPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, ok := a.loadPendingReset(w, r, token); !ok {
		return
	}
	a.renderFragment(w, "parent/reset_password", resetPasswordViewData{Token: token, AppTitle: a.appTitle(r.Context())})
}

func (a *App) ResetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	user, ok := a.loadPendingReset(w, r, token)
	if !ok {
		return
	}

	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	if password == "" {
		a.renderFragment(w, "parent/reset_password", resetPasswordViewData{Token: token, Error: "Password is required.", AppTitle: a.appTitle(r.Context())})
		return
	}
	if password != confirm {
		a.renderFragment(w, "parent/reset_password", resetPasswordViewData{Token: token, Error: "Passwords do not match.", AppTitle: a.appTitle(r.Context())})
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Users.SetPassword(r.Context(), user.ID, hash); err != nil {
		logging.Errorf("reset-password: setting new password for %q: %v", user.Email.String, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("reset-password: %q reset their password (id=%d)", user.Email.String, user.ID)

	// Invalidate any pre-existing sessions for this account before signing
	// them into a new one - "forgot password" is often prompted by a
	// suspected compromise, so a stale session cookie (possibly an
	// attacker's) shouldn't outlive the reset.
	if err := a.Sessions.DeleteAllForUser(r.Context(), user.ID); err != nil {
		logging.Errorf("reset-password: invalidating existing sessions for %q: %v", user.Email.String, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := a.SessionMgr.Login(w, r, user.ID); err != nil {
		logging.Errorf("reset-password: creating session for %q: %v", user.Email.String, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/parent", http.StatusSeeOther)
}
