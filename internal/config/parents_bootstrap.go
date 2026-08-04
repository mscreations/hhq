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

package config

import (
	"encoding/json"
	"fmt"
)

// ParentBootstrap is one entry in the CONFIG_DIR/parents.json bootstrap
// file - see internal/handlers/bootstrap_config.go's BootstrapParents for
// how these are reconciled against the database on every startup. This
// replaces the old BOOTSTRAP_PARENT_NAME/_EMAIL/_PASSWORD/_AVATAR_FILE env
// vars, which only ever created a single parent once (when none existed
// yet) rather than being reconciled like every other bootstrap resource.
type ParentBootstrap struct {
	Name string `json:"name"`
	// DisplayName is optional - a kiosk-only display name (e.g. "Mom"/"Dad",
	// see models.User.DisplayLabel). Blank means "don't touch" on an update
	// (same convention as Color below), not "clear it".
	DisplayName string `json:"display_name"`
	// Email is this parent's login identifier, stored as hhq_users.email
	// internally
	Email string `json:"email"`
	// EmailFile is an alternative to Email - a path to a file
	// containing just the email. Mutually exclusive with Email - see
	// ResolveEmail.
	EmailFile string `json:"email_file,omitempty"`
	Password  string `json:"password"`
	// PasswordFile is an alternative to Password - a path to a file
	// containing just the password (e.g. a Kubernetes Secret volume mount),
	// so parents.json itself can hold no secret material and live in a
	// plain ConfigMap. Mutually exclusive with Password - see
	// ResolvePassword.
	PasswordFile string `json:"password_file,omitempty"`
	// Color is optional - if blank, BootstrapParents auto-assigns one from
	// the same palette used when a parent/child is added via the dashboard.
	Color string `json:"color"`
	// AvatarFile is optional - a path to an image file, applied as this
	// parent's avatar on every startup. Blank means "don't touch this
	// parent's avatar" (same convention as a blank Color).
	AvatarFile string `json:"avatar_file"`
}

// ResolveEmail returns this entry's effective email: Email if set,
// or the trimmed contents of EmailFile if that's set instead. Returns an
// error if both are set (ambiguous) or if EmailFile can't be read.
func (e ParentBootstrap) ResolveEmail() (string, error) {
	return resolveBootstrapField("email", e.Email, e.EmailFile)
}

// ResolvePassword returns this entry's effective password: Password if set,
// or the trimmed contents of PasswordFile if that's set instead. Same
// mutual-exclusion/error behavior as ResolveEmail.
func (e ParentBootstrap) ResolvePassword() (string, error) {
	return resolveBootstrapField("password", e.Password, e.PasswordFile)
}

// ParseParentsBootstrap unmarshals CONFIG_DIR/parents.json's contents - a
// JSON array of ParentBootstrap entries. Per-entry validation (missing
// name/email/password) happens later in BootstrapParents, not here.
func ParseParentsBootstrap(raw string) ([]ParentBootstrap, error) {
	var entries []ParentBootstrap
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parsing parents.json: %w", err)
	}
	return entries, nil
}
