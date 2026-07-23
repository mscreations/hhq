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

// Register calls POST {baseURL}/register and returns the plugin-issued
// token. Deliberately unauthenticated - trust is established by whichever
// caller reaches a freshly-started plugin's /register first (intended to be
// hhq, on its own first successful contact with that plugin). A plugin is
// expected to reject any call to /register once it has already issued a
// token (see billtracker-plugin's Register handler), so this is only ever
// meaningful to call once per plugin - see tryRegisterAndRefresh, which
// only calls it when the plugin's stored token is still empty.
func Register(ctx context.Context, baseURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/register", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("registering: %w", err)
	}
	defer resp.Body.Close()
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
