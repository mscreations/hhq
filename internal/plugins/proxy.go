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
// chrome/URLs, so it has no way to render its own "back" link. #3B82F6
// matches hhq's own dashboard accent color (web/static/css/parent.css).
const backLinkHTML = `<div style="margin-bottom:1.5em"><a href="/parent" style="color:#3B82F6;text-decoration:none;font-size:14px">&larr; Back to Dashboard</a></div>`

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
// plugin's settings page is otherwise a dead end back to hhq's own UI.
// ProxySettings' return value is nil once it has written a response to w -
// including its own error responses for a request-creation failure or a
// response-body read failure, both of which are effectively-never-happens
// local/plugin-response-shape bugs rather than "plugin is down". A non-nil
// return means w was NOT written to - the plugin itself could not be
// reached at all (connection refused/timeout/DNS failure) - so the caller
// (PluginSettingsPage) can render its own friendlier, modal-driven message
// instead of dumping a raw dial error onto a bare page.
func ProxySettings(w http.ResponseWriter, r *http.Request, baseURL, token, csrfToken string) error {
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
		_, _ = io.WriteString(w, injectBackLink(injectCSRFTokens(string(body), csrfToken)))
		return nil
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return nil
}
