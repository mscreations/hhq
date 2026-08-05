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
	"fmt"
	"html/template"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
)

// pluginNavItem is one nav button on the kiosk: a single (plugin, view)
// pair, since a plugin can now register more than one view - each gets its
// own button (see models.PluginView). Label/IconHTML are trusted verbatim
// (see internal/plugins' package doc for the trust boundary this implies).
type pluginNavItem struct {
	PluginID string
	ViewID   string
	Label    string
	IconHTML template.HTML
}

// buildPluginNavItems lists every registered view of every enabled plugin
// for the kiosk's nav bar (see KioskIndex/KioskFragmentHome) - one button
// per view, not per plugin. Unlike the old widget mechanism, this doesn't
// fetch each view's content up front - a plugin's GET /view/{id} is only
// ever fetched on demand, when its nav button is tapped (see
// KioskPluginView), so a slow/down plugin can't affect the initial kiosk
// page load.
func (a *App) buildPluginNavItems(ctx context.Context) []pluginNavItem {
	list, err := a.Plugins.ListViews(ctx)
	if err != nil {
		logging.Errorf("kiosk: listing plugin views: %v", err)
		return nil
	}

	items := make([]pluginNavItem, 0, len(list))
	for _, v := range list {
		items = append(items, pluginNavItem{
			PluginID: v.PluginID,
			ViewID:   v.ViewID,
			Label:    v.Label,
			IconHTML: template.HTML(v.Icon),
		})
	}
	return items
}

// KioskPluginView backs a plugin view's nav button
// (GET /kiosk/view/plugin/{id}/{viewID}): fetches that view's GET
// /view/{viewID} and wraps it for display in the kiosk's full-screen
// content region. A plugin that's unreachable or unknown/disabled renders
// an empty state rather than an error blob on an always-on wall display. An
// unrecognized viewID is not checked against the currently-cached
// hhq_plugin_views set here - it's just passed straight through to the
// plugin, so a stale/removed view naturally falls into the same
// "unavailable" handling as any other fetch failure below.
func (a *App) KioskPluginView(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	viewID := chi.URLParam(r, "viewID")
	plugin, err := a.Plugins.GetByID(r.Context(), id)
	if err != nil || !plugin.Enabled {
		a.renderFragment(w, "kiosk/_plugin_view", template.HTML(""))
		return
	}

	token, err := a.Encryptor.Decrypt(plugin.EncryptedToken)
	if err != nil {
		logging.Errorf("kiosk: decrypting token for plugin %q: %v", id, err)
		a.renderFragment(w, "kiosk/_plugin_view", template.HTML(""))
		return
	}

	html, err := retryOnForbidden(r.Context(), a, *plugin, token, func(token string) (string, error) {
		return plugins.FetchView(r.Context(), plugin.BaseURL, token, viewID)
	})
	if err != nil {
		logging.Warnf("kiosk: fetching view %q from plugin %q failed: %v", viewID, id, err)
		_ = a.Plugins.MarkHealth(r.Context(), id, err)
		a.renderFragment(w, "kiosk/_plugin_view", pluginUnavailableHTML(plugin.Name))
		return
	}
	_ = a.Plugins.MarkHealth(r.Context(), id, nil)
	a.renderFragment(w, "kiosk/_plugin_view", template.HTML(html))
}

// pluginUnavailableHTML is shown in place of a plugin's view when it can't
// be fetched (crashed, unreachable, etc) - previously this rendered an
// empty div, which looked identical to a slow/loading state and gave no
// indication anything was wrong on an always-on kiosk display.
func pluginUnavailableHTML(name string) template.HTML {
	return template.HTML(fmt.Sprintf(
		`<p class="plugin-view-unavailable">%s isn't available right now.</p>`,
		template.HTMLEscapeString(name),
	))
}

// KioskEventAction handles a tap on a plugin-supplied action button in the
// event detail popup (e.g. "Mark Paid" - see internal/plugins.EventAction).
// No auth required, matching KioskCompleteChore's reasoning: it can only
// invoke an action ID that the owning plugin itself already listed on this
// specific event, not an arbitrary plugin call. Resolves the owning plugin
// via the event's synthetic calendar (models.PluginStore.GetByCalendarID),
// POSTs the action to the plugin, then runs an immediate resync
// (plugins.SyncOne) so a since-vanished event (e.g. a paid bill, which the
// plugin's GET /events stops returning) is pruned from the cache before the
// kiosk's fragments re-render - see the HX-Trigger response header, which
// the calendar/agenda panels listen for (web/templates/kiosk/index.html) to
// refresh immediately rather than waiting for their next 60s poll.
func (a *App) KioskEventAction(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	actionID := chi.URLParam(r, "actionID")

	event, err := a.Events.GetByID(r.Context(), id)
	if err != nil {
		if err == models.ErrNotFound {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var action *models.EventAction
	for i, act := range event.Actions {
		if act.ID == actionID {
			action = &event.Actions[i]
			break
		}
	}
	if action == nil {
		http.Error(w, "unknown action for this event", http.StatusBadRequest)
		return
	}
	// Enforced here too, not just by hiding the button in kiosk/_event_detail.html
	// - the button's absence only stops an accidental tap, not a request replayed
	// or crafted directly against this route.
	if action.RequiresParent {
		user := auth.UserFromContext(r.Context())
		if user == nil || user.Role != models.RoleParent {
			http.Error(w, "this action requires a parent to be signed in", http.StatusForbidden)
			return
		}
	}

	plugin, err := a.Plugins.GetByCalendarID(r.Context(), event.CalendarID)
	if err != nil {
		logging.Errorf("kiosk: resolving plugin for event action: calendar id=%d: %v", event.CalendarID, err)
		http.Error(w, "plugin not found for this event", http.StatusInternalServerError)
		return
	}

	token, err := a.Encryptor.Decrypt(plugin.EncryptedToken)
	if err != nil {
		logging.Errorf("kiosk: decrypting token for plugin %q: %v", plugin.ID, err)
		http.Error(w, "action failed", http.StatusInternalServerError)
		return
	}

	_, err = retryOnForbidden(r.Context(), a, *plugin, token, func(token string) (struct{}, error) {
		return struct{}{}, plugins.PostAction(r.Context(), plugin.BaseURL, token, actionID, event.UID)
	})
	if err != nil {
		logging.Errorf("kiosk: plugin %q action %q failed for event uid=%q: %v", plugin.ID, actionID, event.UID, err)
		http.Error(w, "action failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	logging.Infof("kiosk: plugin %q action %q succeeded for event uid=%q", plugin.ID, actionID, event.UID)

	sc := plugins.SyncContext{
		Plugins:          a.Plugins,
		Calendars:        a.Calendars,
		Events:           a.Events,
		CalendarAccounts: a.CalendarAccounts,
		Encryptor:        a.Encryptor,
		ConnectionSecret: a.Cfg.PluginConnectionSecret,
	}
	if err := sc.SyncOne(r.Context(), *plugin, a.Cfg.CalendarWindowDays); err != nil {
		logging.Warnf("kiosk: resync after plugin %q action %q: %v", plugin.ID, actionID, err)
	}

	w.Header().Set("HX-Trigger", "eventActionDone")
	w.WriteHeader(http.StatusOK)
}

// --- Parent dashboard: Plugins card ---

// TogglePlugin flips one plugin's enabled/disabled state - disabling hides
// its kiosk widget (if any) and, since syncAllPlugins/ListEnabled both
// filter on enabled, stops its synthetic events from being refreshed
// (existing cached events stay until the next real sync/prune, matching how
// disabling a real calendar behaves).
func (a *App) TogglePlugin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plugin, err := a.Plugins.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newState := !plugin.Enabled
	if err := a.Plugins.SetEnabled(r.Context(), id, newState); err != nil {
		logging.Errorf("parent: toggling plugin %q: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: plugin %q set enabled=%v", id, newState)
	a.respondAfterMutation(w, r, "parent/_plugins")
}

// PluginSettingsPage reverse-proxies a plugin's own GET/POST /settings page
// through hhq (see internal/plugins.ProxySettings) - registered inside the
// RequireParent+VerifyCSRF route group (cmd/server/main.go), which is what
// enforces parent login before a plugin's settings are ever reachable; the
// plugin process itself has no knowledge of hhq's session/CSRF scheme.
func (a *App) PluginSettingsPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plugin, err := a.Plugins.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token, err := a.Encryptor.Decrypt(plugin.EncryptedToken)
	if err != nil {
		logging.Errorf("parent: decrypting token for plugin %q: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	csrfToken := a.SessionMgr.CSRFToken(a.CSRF, r)
	// The hhq_theme cookie is a plain, non-HttpOnly cookie parent.js sets
	// alongside its localStorage theme choice (see web/static/js/parent.js's
	// setTheme) specifically so server-side code can read the parent's
	// current theme - needed here because a plugin's settings page is a
	// standalone document (see plugins.ProxySettings) that can't inherit
	// hhq's own page CSS/theme variables the way an inlined kiosk view can.
	// A missing/unrecognized cookie value falls back to the default theme
	// inside ProxySettings' injectThemeVariables, not here.
	var themeName string
	if c, err := r.Cookie("hhq_theme"); err == nil {
		themeName = c.Value
	}
	_, err = retryOnForbidden(r.Context(), a, *plugin, token, func(token string) (struct{}, error) {
		return struct{}{}, plugins.ProxySettings(w, r, plugin.BaseURL, token, csrfToken, themeName)
	})
	if err != nil {
		// ProxySettings only returns an error when it couldn't reach the
		// plugin at all, or when it returned 403 and the one-shot reauth-retry
		// (callWithReauth) also failed (see both doc comments) - in every case
		// nothing was written to w yet, so redirect back to the dashboard with
		// a simple message shown in a modal (buildParentDashboardData/
		// dashboard.html), matching the dashboard's existing look/feel instead
		// of navigating to a bare page with a raw dial error.
		logging.Warnf("parent: plugin %q settings page unreachable: %v", id, err)
		_ = a.Plugins.MarkHealth(r.Context(), id, err)
		msg := fmt.Sprintf("%s isn't reachable right now. Make sure the plugin is running, then try again.", plugin.Name)
		http.Redirect(w, r, "/parent?plugin_settings_error="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}
