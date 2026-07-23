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
	"context"
	"html/template"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/release"
	"github.com/mscreations/hhq/internal/util"
	"github.com/mscreations/hhq/internal/weather"
)

// App bundles every dependency handlers need. Passed around explicitly rather
// than via globals — this is the idiomatic Go pattern and makes testing easier.
type App struct {
	Cfg *config.Config

	// Version is the running app's version string (stamped at build time via
	// -ldflags "-X main.Version=..."), shown in the parent dashboard footer.
	Version string

	Users            *models.UserStore
	Sessions         *models.SessionStore
	CalendarAccounts *models.CalendarAccountStore
	Calendars        *models.CalendarStore
	Events           *models.EventStore
	Chores           *models.ChoreStore
	ChoreDefs        *models.ChoreDefinitionStore
	ChoreInstances   *models.ChoreInstanceStore
	Settings         *models.SettingsStore
	Weather          *weather.Cache
	Plugins          *models.PluginStore
	Release          *release.Cache

	SessionMgr       *auth.SessionManager
	Approval         *auth.ApprovalLinkSigner
	Invite           *auth.ApprovalLinkSigner
	PasswordReset    *auth.ApprovalLinkSigner
	GoogleOAuthState *auth.ApprovalLinkSigner
	CSRF             *auth.CSRFManager
	LoginLimiter     *auth.LoginLimiter
	Encryptor        *util.Encryptor
	Mailer           *email.Sender

	Templates *template.Template

	// syncing tracks calendar accounts with an in-progress background
	// sync (see syncAccountAsync in sync.go), so the dashboard can show a
	// "Resync Now" button as busy and self-poll until it completes. Zero
	// value is ready to use.
	syncing syncStatus
}

// appTitle returns the parent-editable app_title setting, falling back to the
// configured default if the setting can't be read.
func (a *App) appTitle(ctx context.Context) string {
	title, err := a.Settings.Get(ctx, "app_title", a.Cfg.AppTitle)
	if err != nil {
		logging.Errorf("appTitle: loading app_title setting: %v", err)
		return a.Cfg.AppTitle
	}
	return title
}
