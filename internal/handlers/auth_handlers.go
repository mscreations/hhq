// Copyright (C) 2026 Jon Shaulis
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package handlers

import (
	"net/http"
	"strings"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/logging"
)

// sanitizeNextPath restricts a post-login redirect target to a same-site,
// path-only value, rejecting anything that could send the browser off-site
// (an absolute URL, or a protocol-relative "//host/..." URL) — otherwise
// "next" (attacker-controllable via a crafted login link) is an open
// redirect.
func sanitizeNextPath(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/parent"
	}
	return next
}

type loginViewData struct {
	Error    string
	Next     string
	AppTitle string
}

func (a *App) LoginPage(w http.ResponseWriter, r *http.Request) {
	existing, err := a.Users.ListParents(r.Context())
	if err == nil && len(existing) == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	a.renderFragment(w, "parent/login", loginViewData{Next: sanitizeNextPath(r.URL.Query().Get("next")), AppTitle: a.appTitle(r.Context())})
}

func (a *App) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")
	next := sanitizeNextPath(r.FormValue("next"))

	// Rate-limit by both remote IP and the submitted email: per-IP catches a
	// single attacker hammering the endpoint, per-email catches a distributed
	// attempt (many source IPs) targeting one specific account.
	ipKey := "ip:" + r.RemoteAddr
	emailKey := "email:" + strings.ToLower(email)
	if !a.LoginLimiter.Allow(ipKey) || !a.LoginLimiter.Allow(emailKey) {
		logging.Warnf("login: rate limit exceeded for %q from %s, rejecting without checking credentials", email, r.RemoteAddr)
		a.renderFragment(w, "parent/login", loginViewData{Error: "Too many attempts. Please try again later.", Next: next, AppTitle: a.appTitle(r.Context())})
		return
	}

	user, err := a.Users.GetByEmail(r.Context(), email)
	storedHash := ""
	if err == nil && user.Role == "parent" && user.PasswordHash.Valid && user.IsActive {
		storedHash = user.PasswordHash.String
	}

	// CheckPasswordTiming always performs a bcrypt comparison, even when
	// storedHash is "" (unknown email/non-parent/no password set), so a
	// nonexistent email doesn't respond measurably faster than a real one -
	// see internal/auth/password.go for why that matters.
	if !auth.CheckPasswordTiming(storedHash, password) {
		a.LoginLimiter.RecordFailure(ipKey)
		a.LoginLimiter.RecordFailure(emailKey)
		logging.Warnf("login: failed attempt for %q from %s", email, r.RemoteAddr)
		a.renderFragment(w, "parent/login", loginViewData{Error: "Invalid email or password.", Next: next, AppTitle: a.appTitle(r.Context())})
		return
	}
	a.LoginLimiter.Reset(ipKey)
	a.LoginLimiter.Reset(emailKey)

	if err := a.SessionMgr.Login(w, r, user.ID); err != nil {
		logging.Errorf("login: creating session for %q: %v", email, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("login: %q signed in successfully", email)

	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) Logout(w http.ResponseWriter, r *http.Request) {
	a.SessionMgr.Logout(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
