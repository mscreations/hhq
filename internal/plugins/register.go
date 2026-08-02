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
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// registerTimeout bounds a single POST /register attempt - a timeout here
// just means "not up yet," handled by the caller's retry loop (see
// internal/handlers/plugin_bootstrap.go's tryRegisterAndRefresh), not a
// hard failure.
const registerTimeout = 5 * time.Second

// registerResponse is the JSON body a plugin's POST /register returns on
// success: a freshly generated shared secret hhq will send as
// "Authorization: Bearer <token>" on every subsequent request (see
// client.go/manifest.go/proxy.go).
type registerResponse struct {
	Token string `json:"token"`
}

// connectionSecretHeader carries the shared connection secret (see
// config.Config.PluginConnectionSecret) on every POST /register call - a
// plugin checks this before issuing/reissuing a token, which is what makes
// /register safe to call more than once per plugin (see PLUGINS.md's
// "Authentication: self-registration").
const connectionSecretHeader = "X-Plugin-Connection-Secret"

// Register calls POST {baseURL}/register (with the shared connection
// secret) and returns the plugin-issued token. A plugin verifies
// connectionSecret before doing anything else, rejecting a mismatch with
// 401 (wrapped here as ErrConnectionSecretMismatch - deliberately distinct
// from the 403 every other route uses for a stale bearer token, since a bad
// connection secret is an operator misconfiguration retrying won't fix, not
// a token that'll resolve itself on the next re-registration) - the secret,
// not "first caller wins," is what protects this endpoint. On a valid
// secret, the plugin issues a fresh token every time, overwriting any it
// had previously issued - this is what lets tryRegisterAndRefresh
// (startup/periodic path) and reregisterPlugin (403 recovery path, see
// internal/handlers/plugin_auth.go) both call this safely, not just once
// per plugin's lifetime.
func Register(ctx context.Context, baseURL, connectionSecret string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/register", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(connectionSecretHeader, connectionSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("registering: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("registering: %w - check that PLUGIN_CONNECTION_SECRET matches on both hhq and the plugin", ErrConnectionSecretMismatch)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registering: unexpected status %d", resp.StatusCode)
	}

	var decoded registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decoding register response: %w", err)
	}
	if decoded.Token == "" {
		return "", fmt.Errorf("registering: plugin returned an empty token")
	}
	return decoded.Token, nil
}
