package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

// googleOAuthStateTTL is how long a "Connect Google Calendar" link stays
// valid between GoogleConnectStart minting it and the parent completing
// Google's consent screen and being redirected back.
const googleOAuthStateTTL = 10 * time.Minute

// GoogleConnectStart redirects a logged-in parent to Google's OAuth2 consent
// screen. Reached either from the dashboard's "Connect Google Calendar" link
// (fresh account, no reconnect_id) or an existing Google account's edit
// page's "Reconnect" link (?reconnect_id=<account id>, used when a token has
// been revoked/expired).
func (a *App) GoogleConnectStart(w http.ResponseWriter, r *http.Request) {
	if !a.Cfg.GoogleOAuthEnabled() {
		http.Error(w, "Google Calendar sync is not configured on this server", http.StatusNotFound)
		return
	}

	reconnectID := 0
	if idStr := r.URL.Query().Get("reconnect_id"); idStr != "" {
		id, err := parseInt(idStr)
		if err != nil {
			http.Error(w, "invalid reconnect_id", http.StatusBadRequest)
			return
		}
		account, err := a.CalendarAccounts.GetByID(r.Context(), id)
		if err == models.ErrAccountNotFound || (err == nil && account.Provider != models.ProviderGoogle) {
			http.Error(w, "not a Google calendar account", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		reconnectID = id
	}

	// state is the token's only payload (id=reconnectID, or 0 for a fresh
	// connect) - this is Google's own CSRF defense for the flow (unforgeable,
	// short-lived), independent of whether the session cookie itself survives
	// the redirect chain out to accounts.google.com and back. The actual
	// authorization boundary is RequireParent gating both this route and the
	// callback below - a parent must be logged in for either to do anything.
	state := a.GoogleOAuthState.Sign(reconnectID, auth.ActionGoogleOAuthConnect, googleOAuthStateTTL)

	authURL := config.GoogleOAuthConfig(a.Cfg).AuthCodeURL(state,
		oauth2.AccessTypeOffline, // required to receive a refresh token at all
		oauth2.ApprovalForce,     // re-issue a refresh token even if this Google account already granted consent once (needed for reconnect)
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// googleUserInfo is the subset of Google's userinfo response this app cares
// about - just enough to show "Connected as X" on the dashboard.
type googleUserInfo struct {
	Email string `json:"email"`
}

// GoogleConnectCallback is Google's OAuth2 redirect target after the parent
// approves (or cancels) the consent screen.
func (a *App) GoogleConnectCallback(w http.ResponseWriter, r *http.Request) {
	if !a.Cfg.GoogleOAuthEnabled() {
		http.Error(w, "Google Calendar sync is not configured on this server", http.StatusNotFound)
		return
	}

	if errCode := r.URL.Query().Get("error"); errCode != "" {
		// The parent clicked "Cancel" on Google's consent screen (or some
		// other non-fatal denial) - send them back rather than a raw 500.
		logging.Infof("google oauth: consent denied or cancelled (%s)", errCode)
		http.Redirect(w, r, "/parent?google_error="+url.QueryEscape("Google Calendar connection was cancelled."), http.StatusSeeOther)
		return
	}

	reconnectID, action, err := a.GoogleOAuthState.Verify(r.URL.Query().Get("state"))
	if err != nil || action != auth.ActionGoogleOAuthConnect {
		logging.Warnf("google oauth: rejecting callback with invalid/expired state: %v", err)
		http.Error(w, "This Google Calendar connection link has expired. Please try again from the dashboard.", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	oauthCfg := config.GoogleOAuthConfig(a.Cfg)
	token, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		logging.Errorf("google oauth: exchanging code: %v", err)
		http.Redirect(w, r, "/parent?google_error="+url.QueryEscape("Couldn't connect to Google Calendar - please try again."), http.StatusSeeOther)
		return
	}
	if token.RefreshToken == "" {
		// Can happen if Google decides not to (re)issue one despite
		// ApprovalForce - rare, but nothing useful can be synced without it.
		logging.Errorf("google oauth: token response for %q had no refresh token", tokenEmailHint(token))
		http.Redirect(w, r, "/parent?google_error="+url.QueryEscape("Google didn't grant offline access - please try connecting again."), http.StatusSeeOther)
		return
	}

	email, err := fetchGoogleEmail(ctx, oauthCfg, token)
	if err != nil {
		// Display-only info, not required for the account to function -
		// log and continue rather than failing the whole connect.
		logging.Warnf("google oauth: fetching connected account email: %v", err)
	}

	encrypted, err := a.Encryptor.Encrypt(token.RefreshToken)
	if err != nil {
		logging.Errorf("google oauth: encrypting refresh token: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var id int
	if reconnectID != 0 {
		id = reconnectID
		if err := a.CalendarAccounts.UpdateGoogleToken(ctx, id, encrypted, email); err != nil {
			logging.Errorf("google oauth: updating token for account id=%d: %v", id, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logging.Infof("google oauth: reconnected account id=%d (%s)", id, email)
	} else {
		name := email
		if name == "" {
			name = "Google Calendar"
		}
		id, err = a.CalendarAccounts.CreateGoogle(ctx, name, encrypted, email)
		if err != nil {
			logging.Errorf("google oauth: creating account: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logging.Infof("google oauth: connected new account id=%d (%s)", id, email)
	}

	a.syncAccountAsync(id)
	http.Redirect(w, r, "/parent", http.StatusSeeOther)
}

// GoogleUserInfoURL is a package-level var (rather than an inline literal)
// so tests can redirect it to a local httptest server, same pattern as
// config.GoogleEndpoint.
var GoogleUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"

// fetchGoogleEmail looks up the connected account's email via Google's
// userinfo endpoint, for display-only purposes ("Connected as X").
func fetchGoogleEmail(ctx context.Context, oauthCfg *oauth2.Config, token *oauth2.Token) (string, error) {
	client := oauthCfg.Client(ctx, token)
	resp, err := client.Get(GoogleUserInfoURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var info googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	return info.Email, nil
}

// tokenEmailHint is a best-effort, side-effect-free description of a token
// for log messages when no email is known yet (e.g. the refresh-token-missing
// error path above, before fetchGoogleEmail would normally run).
func tokenEmailHint(token *oauth2.Token) string {
	if token == nil {
		return "unknown"
	}
	return "token issued " + token.Expiry.Format(time.RFC3339)
}
