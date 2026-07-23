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
// BOOTSTRAP_PARENT_* env vars, or via an accepted invite), this page
// self-disables by redirecting to /login.
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
