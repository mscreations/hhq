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
)

type setupViewData struct {
	Error    string
	AppTitle string
}

// SetupPage shows the first-run "create the initial parent" form, but only
// while no parent exists yet - once one does (whether created here, via
// parents.json, or via an accepted invite), this page self-disables by
// redirecting to /login.
func (a *App) SetupPage(w http.ResponseWriter, r *http.Request) {
	existing, err := a.Users.ListParents(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(existing) > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.renderFragment(w, "parent/setup", setupViewData{AppTitle: a.appTitle(r.Context())})
}

func (a *App) SetupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	// Re-check under the same guard as SetupPage - closes the race where two
	// browser tabs (or a slow attacker) both load /setup before either submits.
	existing, err := a.Users.ListParents(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(existing) > 0 {
		a.renderFragment(w, "parent/setup", setupViewData{Error: "Setup has already been completed. Please log in instead.", AppTitle: a.appTitle(r.Context())})
		return
	}

	name := r.FormValue("name")
	email := r.FormValue("email")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")

	if name == "" || email == "" || password == "" {
		a.renderFragment(w, "parent/setup", setupViewData{Error: "All fields are required.", AppTitle: a.appTitle(r.Context())})
		return
	}
	if password != confirm {
		a.renderFragment(w, "parent/setup", setupViewData{Error: "Passwords do not match.", AppTitle: a.appTitle(r.Context())})
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := a.Users.CreateParent(r.Context(), name, email, hash)
	if err != nil {
		logging.Errorf("setup: creating initial parent %q: %v", email, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("setup: created initial parent account %q (id=%d)", email, id)

	if err := a.SessionMgr.Login(w, r, id); err != nil {
		logging.Errorf("setup: creating session for %q: %v", email, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/parent", http.StatusSeeOther)
}
