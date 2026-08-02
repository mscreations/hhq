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
	"errors"
	"fmt"

	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
)

// retryOnForbidden calls attempt once with token. If attempt fails
// specifically because the plugin returned 403 Forbidden
// (plugins.ErrForbidden - its stored token no longer matches what the
// plugin has on record, e.g. the plugin process was redeployed and lost
// its token store), it re-registers once via reregisterPlugin (using the
// shared connection secret, see config.Config.PluginConnectionSecret) and
// retries attempt exactly once with the fresh token. Any other error from
// attempt (including a second 403 after a fresh re-registration) is
// returned as-is - this is a one-shot recovery, not a retry loop.
//
// Callers decrypt plugin's token themselves first (rather than this helper
// doing it) so each call site keeps its own existing decrypt-failure
// handling distinct from a plugin-call failure.
func retryOnForbidden[T any](ctx context.Context, a *App, plugin models.Plugin, token string, attempt func(token string) (T, error)) (T, error) {
	result, err := attempt(token)
	if err == nil || !errors.Is(err, plugins.ErrForbidden) {
		return result, err
	}

	logging.Warnf("plugin %q: rejected stored token (403) - re-registering", plugin.ID)
	freshToken, rerr := a.reregisterPlugin(ctx, plugin.ID, plugin.BaseURL)
	if rerr != nil {
		if errors.Is(rerr, plugins.ErrConnectionSecretMismatch) {
			// Not the transient "plugin isn't up yet" case - a standing
			// operator misconfiguration, logged louder so it's easy to spot.
			logging.Errorf("plugin %q: %v", plugin.ID, rerr)
		}
		var zero T
		return zero, fmt.Errorf("plugin rejected token and re-registration failed: %w", rerr)
	}
	logging.Infof("plugin %q: re-registered successfully after 403, retrying", plugin.ID)
	return attempt(freshToken)
}

// reregisterPlugin calls the plugin's POST /register (with the shared
// connection secret) to obtain a fresh token, encrypts it, and stores it -
// shared by retryOnForbidden's 403-recovery path and tryRegisterAndRefresh's
// startup/periodic path (see plugin_bootstrap.go).
func (a *App) reregisterPlugin(ctx context.Context, id, baseURL string) (string, error) {
	token, err := plugins.Register(ctx, baseURL, a.Cfg.PluginConnectionSecret)
	if err != nil {
		return "", err
	}
	encryptedToken, err := a.Encryptor.Encrypt(token)
	if err != nil {
		return "", fmt.Errorf("encrypting received token: %w", err)
	}
	if err := a.Plugins.SetToken(ctx, id, encryptedToken); err != nil {
		return "", fmt.Errorf("storing received token: %w", err)
	}
	return token, nil
}
