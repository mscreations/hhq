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

package plugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mscreations/hhq/internal/theme"
)

// settingsProxyTimeout bounds a proxied GET/POST /settings request. Longer
// than the view timeout since this is a deliberate parent-initiated page
// load/form submission, not a background poll.
const settingsProxyTimeout = 15 * time.Second

// postFormOpenTag matches a plugin-rendered <form ...> opening tag whose
// method is POST (case-insensitive) - i.e. every form that will need a CSRF
// token injected before the parent's browser can ever submit it.
var postFormOpenTag = regexp.MustCompile(`(?i)<form\b[^>]*\bmethod\s*=\s*"post"[^>]*>`)

// injectCSRFTokens inserts a hidden csrf_token input, bound to the current
// hhq parent session, right after every POST <form> opening tag in a
// plugin's proxied settings HTML. This is what lets a plugin's own
// hand-written forms (which know nothing of hhq's session/CSRF scheme - see
// ProxySettings' doc comment) satisfy hhq's VerifyCSRF middleware once
// submitted: the token travels back to hhq inside the plugin's own POST
// body as an ordinary form field, and the plugin itself simply ignores it.
func injectCSRFTokens(html, csrfToken string) string {
	hidden := fmt.Sprintf(`<input type="hidden" name="csrf_token" value="%s">`, csrfToken)
	return postFormOpenTag.ReplaceAllStringFunc(html, func(tag string) string {
		return tag + hidden
	})
}

// bodyOpenTag matches a plugin's <body ...> opening tag, so injectBackLink
// knows where to place the back-to-dashboard link.
var bodyOpenTag = regexp.MustCompile(`(?i)<body[^>]*>`)

// backLinkHTML is injected as the first thing inside a plugin's <body> -
// a plugin's settings page is a full standalone HTML document (see
// ProxySettings' doc comment) with no knowledge of hhq's own dashboard
// chrome/URLs, so it has no way to render its own "back" link.
// var(--hhq-accent, #3B82F6) resolves against the <style> block
// injectThemeVariables adds to <head> - the literal fallback (hhq's
// default dashboard accent) only applies if that injection is somehow
// missing (e.g. the plugin's response has no <head> at all).
const backLinkHTML = `<div style="margin-bottom:1.5em"><a href="/parent" style="color:var(--hhq-accent, #3B82F6);text-decoration:none;font-size:14px">&larr; Back to Dashboard</a></div>`

// injectBackLink inserts backLinkHTML right after the opening <body> tag,
// or prepends it if the plugin's response has no <body> tag (e.g. a bare
// HTML fragment) so a way back is always present regardless.
func injectBackLink(html string) string {
	loc := bodyOpenTag.FindStringIndex(html)
	if loc == nil {
		return backLinkHTML + html
	}
	return html[:loc[1]] + backLinkHTML + html[loc[1]:]
}

// headOpenTag matches a plugin's <head ...> opening tag, so
// injectThemeVariables knows where to place the theme <style> block.
var headOpenTag = regexp.MustCompile(`(?i)<head[^>]*>`)

// injectThemeVariables inserts a <style> block defining the current
// theme's --hhq-* custom properties on :root, right after the opening
// <head> tag (or prepended if there's no <head> tag at all, mirroring
// injectBackLink's no-<body> fallback). A plugin's settings page is a
// standalone document proxied through hhq (see ProxySettings' doc
// comment) - unlike a kiosk view fragment, which is inlined into hhq's
// own page and inherits these variables from the cascade for free, a
// settings page has no way to see hhq's stylesheets at all. This is the
// settings-page half of the plugin theme contract documented in
// PLUGINS.md's "Theming" section; themeName is resolved via
// internal/theme.ByName, which falls back to the default theme for an
// empty or unrecognized name rather than erroring.
func injectThemeVariables(html, themeName string) string {
	v := theme.ByName(themeName).Vars
	style := fmt.Sprintf(
		`<style>:root{--hhq-bg:%s;--hhq-panel-bg:%s;--hhq-border:%s;--hhq-text:%s;--hhq-text-dim:%s;--hhq-accent:%s;--hhq-green:%s;--hhq-red:%s;--hhq-amber:%s;--hhq-gold:%s;}</style>`,
		v.Bg, v.PanelBg, v.Border, v.Text, v.TextDim, v.Accent, v.Green, v.Red, v.Amber, v.Gold,
	)
	loc := headOpenTag.FindStringIndex(html)
	if loc == nil {
		return style + html
	}
	return html[:loc[1]] + style + html[loc[1]:]
}

// ProxySettings relays r (method, body, content-type) to {baseURL}/settings
// and copies the plugin's response back to w - the parent's browser never
// talks to a plugin process directly. Called from behind hhq's own
// RequireParent+VerifyCSRF middleware (see internal/handlers/plugins.go's
// PluginSettingsPage), which is what enforces parent login before a
// plugin's settings are ever reachable - the plugin itself has no knowledge
// of hhq's session/CSRF scheme. csrfToken is stamped into every POST form in
// any HTML response the plugin returns (see injectCSRFTokens) - both on a
// plain GET and on the re-rendered page a POST typically responds with
// (the plugin's own SettingsPage handler renders the same full page after
// handling a POST action, so its forms need fresh tokens too, not just the
// initial GET's) - so that whichever page the parent's browser ends up on,
// its forms carry a token VerifyCSRF will accept. Every such response also
// gets a "Back to Dashboard" link injected (see injectBackLink), since a
// plugin's settings page is otherwise a dead end back to hhq's own UI, and
// a <style> block defining the current theme's --hhq-* variables (see
// injectThemeVariables), since a plugin's settings page is a standalone
// document that can't otherwise see hhq's own theme. themeName is the
// caller's already-resolved current theme name (PluginSettingsPage reads
// it from the hhq_theme cookie parent.js sets) - an empty or unrecognized
// name falls back to the default theme, it never errors this call.
// ProxySettings' return value is nil once it has written a response to w -
// including its own error responses for a request-creation failure or a
// response-body read failure, both of which are effectively-never-happens
// local/plugin-response-shape bugs rather than "plugin is down". A non-nil
// return means w was NOT written to - either the plugin itself could not be
// reached at all (connection refused/timeout/DNS failure), or it returned
// 403 Forbidden (wrapped as ErrForbidden - the stored token no longer
// matches what the plugin has, see internal/handlers/plugin_auth.go's
// callWithReauth, which retries this call once with a freshly re-registered
// token) - so the caller (PluginSettingsPage) can render its own friendlier,
// modal-driven message instead of dumping a raw dial error onto a bare page.
func ProxySettings(w http.ResponseWriter, r *http.Request, baseURL, token, csrfToken, themeName string) error {
	ctx, cancel := context.WithTimeout(r.Context(), settingsProxyTimeout)
	defer cancel()

	// hhq's VerifyCSRF middleware (mounted in front of this handler) already
	// called r.ParseForm to read the csrf_token field, which drains r.Body -
	// forwarding r.Body as-is would hand the plugin an empty request. Rebuild
	// the body from r.PostForm (already parsed, csrf_token included and
	// harmless - the plugin ignores fields it doesn't recognize) instead.
	body := io.Reader(r.Body)
	if r.Method == http.MethodPost {
		body = strings.NewReader(r.PostForm.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, baseURL+"/settings", body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("plugin settings page unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		// Nothing has been written to w yet - safe for the caller to retry
		// this whole call once with a freshly re-registered token (see
		// internal/handlers/plugin_auth.go's callWithReauth).
		return fmt.Errorf("plugin settings page: %w", ErrForbidden)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	if strings.Contains(ct, "text/html") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return nil
		}
		w.WriteHeader(resp.StatusCode)
		out := injectThemeVariables(injectBackLink(injectCSRFTokens(string(body), csrfToken)), themeName)
		_, _ = io.WriteString(w, out)
		return nil
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return nil
}
