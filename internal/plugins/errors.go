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

import "errors"

// ErrForbidden is returned (wrapped, via errors.Is) by FetchManifest,
// FetchView, FetchEvents, PostAction, and ProxySettings specifically when a
// plugin responds 403 Forbidden - meaning its stored bearer token no longer
// matches what the plugin has on record (e.g. the plugin process was
// redeployed and lost its token store). Callers use this to trigger a
// one-shot re-registration-and-retry (see internal/handlers/plugin_auth.go's
// callWithReauth) rather than treating it as an ordinary failure.
var ErrForbidden = errors.New("plugin rejected token")

// ErrConnectionSecretMismatch is returned (wrapped, via errors.Is) by
// Register specifically when a plugin responds 401 Unauthorized to
// POST /register - meaning the plugin checked the X-Plugin-Connection-Secret
// header and it didn't match what the plugin has configured. Unlike
// ErrForbidden (a stale token, expected to resolve itself once
// re-registration completes), this points at a standing operator
// misconfiguration (PLUGIN_CONNECTION_SECRET differs between hhq and the
// plugin) that retrying alone will never fix - callers use this to log a
// more actionable message than a generic "unexpected status" error.
var ErrConnectionSecretMismatch = errors.New("plugin rejected connection secret")
