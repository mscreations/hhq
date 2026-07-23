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

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

type inviteAcceptViewData struct {
	Token    string
	Name     string
	Email    string
	Error    string
	AppTitle string
}

// loadPendingInvite verifies the token and returns the invited user, or
// writes an appropriate response itself and returns ok=false.
func (a *App) loadPendingInvite(w http.ResponseWriter, r *http.Request, token string) (user *models.User, ok bool) {
	appTitle := a.appTitle(r.Context())

	userID, action, err := a.Invite.Verify(token)
	if err != nil || action != auth.ActionInviteAccept {
		a.renderFragment(w, "parent/invite_accept", inviteAcceptViewData{
			Error:    "This invite link is invalid or has expired. Ask your inviter to resend it.",
			AppTitle: appTitle,
		})
		return nil, false
	}

	u, err := a.Users.GetByID(r.Context(), userID)
	if err == models.ErrNotFound {
		a.renderFragment(w, "parent/invite_accept", inviteAcceptViewData{
			Error:    "This invite link is invalid or has expired. Ask your inviter to resend it.",
			AppTitle: appTitle,
		})
		return nil, false
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	if u.Role != models.RoleParent || u.PasswordHash.Valid {
		a.renderFragment(w, "parent/invite_accept", inviteAcceptViewData{
			Error:    "This invite has already been accepted. Try logging in instead.",
			AppTitle: appTitle,
		})
		return nil, false
	}
	return u, true
}

func (a *App) InviteAcceptPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	user, ok := a.loadPendingInvite(w, r, token)
	if !ok {
		return
	}
	a.renderFragment(w, "parent/invite_accept", inviteAcceptViewData{
		Token:    token,
		Name:     user.Name,
		Email:    user.Email.String,
		AppTitle: a.appTitle(r.Context()),
	})
}

func (a *App) InviteAcceptSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	user, ok := a.loadPendingInvite(w, r, token)
	if !ok {
		return
	}

	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")
	if password == "" {
		a.renderFragment(w, "parent/invite_accept", inviteAcceptViewData{Token: token, Name: user.Name, Email: user.Email.String, Error: "Password is required.", AppTitle: a.appTitle(r.Context())})
		return
	}
	if password != confirm {
		a.renderFragment(w, "parent/invite_accept", inviteAcceptViewData{Token: token, Name: user.Name, Email: user.Email.String, Error: "Passwords do not match.", AppTitle: a.appTitle(r.Context())})
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Users.SetPasswordAndAccept(r.Context(), user.ID, hash); err != nil {
		logging.Errorf("invite: accepting invite for %q: %v", user.Email.String, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("invite: %q accepted invite and set a password (id=%d)", user.Email.String, user.ID)

	if err := a.SessionMgr.Login(w, r, user.ID); err != nil {
		logging.Errorf("invite: creating session for %q: %v", user.Email.String, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/parent", http.StatusSeeOther)
}
