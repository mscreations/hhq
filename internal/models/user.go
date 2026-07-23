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

package models

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type UserRole string

const (
	RoleParent UserRole = "parent"
	RoleChild  UserRole = "child"
)

type User struct {
	ID           int
	Name         string
	Role         UserRole
	Color        string
	Email        sql.NullString
	PasswordHash sql.NullString
	// Parents only: optional kiosk display name (e.g. "Mom"/"Dad"). See
	// DisplayLabel for the fallback-to-Name accessor used everywhere this is
	// actually rendered.
	DisplayName     sql.NullString
	AuthProvider    string
	ExternalSubject sql.NullString
	IsActive        bool
	CreatedAt       time.Time
	InvitedAt       sql.NullTime
	// True if this (child) row is created/kept in sync by the children.json
	// bootstrap config file (see internal/handlers/bootstrap_config.go) rather
	// than through the parent dashboard. Read-only in the UI for the same
	// reason bootstrap-managed calendar accounts are (see calendar.go).
	BootstrapManaged bool
	// HasAvatar/AvatarUpdatedAt are populated by every list/get query below
	// without ever selecting the avatar_image BYTEA itself (see AvatarURL) -
	// the actual image bytes are only ever fetched by GetAvatarByID, on
	// demand, by the dedicated GET /avatars/{id} serving route.
	HasAvatar       bool
	AvatarUpdatedAt sql.NullTime
}

// AvatarURL returns the URL the kiosk/dashboard should use for this user's
// avatar image, or "" if none is set (callers should fall back to the color
// swatch in that case). The ?v= query param is a cache-buster tied to
// avatar_updated_at, so replacing an avatar immediately invalidates any
// previously cached copy of the old image.
func (u User) AvatarURL() string {
	if !u.HasAvatar {
		return ""
	}
	return fmt.Sprintf("/avatars/%d?v=%d", u.ID, u.AvatarUpdatedAt.Time.Unix())
}

// DisplayLabel returns the parent's kiosk display name if set, otherwise
// their real name - used everywhere a parent's identity is shown on the
// kiosk (as opposed to the parent dashboard or emails, which always use the
// real Name).
func (u User) DisplayLabel() string {
	if u.DisplayName.Valid && u.DisplayName.String != "" {
		return u.DisplayName.String
	}
	return u.Name
}

var ErrNotFound = errors.New("not found")

type UserStore struct {
	DB *sql.DB
}

func (s *UserStore) ListChildren(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, role, color, email, password_hash, display_name, auth_provider, external_subject, is_active, created_at, invited_at, bootstrap_managed, (avatar_image IS NOT NULL) AS has_avatar, avatar_updated_at
		FROM hhq_users WHERE role = 'child' AND is_active = TRUE ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsers(rows)
}

func (s *UserStore) ListParents(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, role, color, email, password_hash, display_name, auth_provider, external_subject, is_active, created_at, invited_at, bootstrap_managed, (avatar_image IS NOT NULL) AS has_avatar, avatar_updated_at
		FROM hhq_users WHERE role = 'parent' AND is_active = TRUE ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsers(rows)
}

func (s *UserStore) ListAll(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, role, color, email, password_hash, display_name, auth_provider, external_subject, is_active, created_at, invited_at, bootstrap_managed, (avatar_image IS NOT NULL) AS has_avatar, avatar_updated_at
		FROM hhq_users ORDER BY role, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsers(rows)
}

func (s *UserStore) GetByID(ctx context.Context, id int) (*User, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, name, role, color, email, password_hash, display_name, auth_provider, external_subject, is_active, created_at, invited_at, bootstrap_managed, (avatar_image IS NOT NULL) AS has_avatar, avatar_updated_at
		FROM hhq_users WHERE id = $1`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *UserStore) GetByEmail(ctx context.Context, email string) (*User, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, name, role, color, email, password_hash, display_name, auth_provider, external_subject, is_active, created_at, invited_at, bootstrap_managed, (avatar_image IS NOT NULL) AS has_avatar, avatar_updated_at
		FROM hhq_users WHERE email = $1`, email)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *UserStore) CreateChild(ctx context.Context, name, color string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_users (name, role, color) VALUES ($1, 'child', $2) RETURNING id`,
		name, color).Scan(&id)
	return id, err
}

// GetChildByName looks up an active child by name, used by the children.json
// bootstrap reconciliation (see internal/handlers/bootstrap_config.go) to
// decide whether a config entry needs to create a new row or update an
// existing one. Returns ErrNotFound if no active child has that name.
func (s *UserStore) GetChildByName(ctx context.Context, name string) (*User, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, name, role, color, email, password_hash, display_name, auth_provider, external_subject, is_active, created_at, invited_at, bootstrap_managed, (avatar_image IS NOT NULL) AS has_avatar, avatar_updated_at
		FROM hhq_users WHERE role = 'child' AND is_active = TRUE AND name = $1`, name)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// CreateChildBootstrap creates a child row marked bootstrap_managed, used by
// the children.json bootstrap reconciliation.
func (s *UserStore) CreateChildBootstrap(ctx context.Context, name, color string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_users (name, role, color, bootstrap_managed) VALUES ($1, 'child', $2, TRUE) RETURNING id`,
		name, color).Scan(&id)
	return id, err
}

// UpdateChildBootstrap refreshes a bootstrap-managed child's color to match
// children.json on every startup (name is the reconciliation key, so it
// never changes here).
func (s *UserStore) UpdateChildBootstrap(ctx context.Context, id int, color string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_users SET color = $1 WHERE id = $2 AND role = 'child'`, color, id)
	return err
}

// childColorPalette mirrors calendar.go's colorPalette so each child added
// on the dashboard gets a visually distinct color automatically, the same
// way newly-discovered calendars do, rather than every child defaulting to
// the same fixed color.
var childColorPalette = []string{
	"#3B82F6", // blue
	"#EF4444", // red
	"#22C55E", // green
	"#F59E0B", // amber
	"#A855F7", // purple
	"#EC4899", // pink
	"#14B8A6", // teal
	"#F97316", // orange
	"#6366F1", // indigo
	"#84CC16", // lime
	"#06B6D4", // cyan
	"#D946EF", // fuchsia
	"#EAB308", // yellow
	"#10B981", // emerald
	"#F43F5E", // rose
	"#8B5CF6", // violet
	"#0EA5E9", // sky
	"#64748B", // slate
	"#1E3A8A", // navy
	"#991B1B", // maroon
	"#166534", // forest
	"#CA8A04", // mustard
	"#92400E", // brown
	"#7C3AED", // plum
}

// NextAvailableColor picks the first palette color not currently in use by
// any user (parent or child - kiosk avatars/labels for both share the same
// visual space). Not wrapped in a transaction/lock, matching
// CalendarStore.NextAvailableColor's trade-off - the tiny race window is
// acceptable for this single-instance, low-write-frequency app.
func (s *UserStore) NextAvailableColor(ctx context.Context) (string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT color FROM hhq_users`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	used := make(map[string]bool)
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", err
		}
		used[c] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	for _, c := range childColorPalette {
		if !used[c] {
			return c, nil
		}
	}
	return childColorPalette[len(used)%len(childColorPalette)], nil
}

func (s *UserStore) CreateParent(ctx context.Context, name, email, passwordHash string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_users (name, role, email, password_hash, auth_provider) VALUES ($1, 'parent', $2, $3, 'local') RETURNING id`,
		name, email, passwordHash).Scan(&id)
	return id, err
}

// InviteParent creates a parent row with no password set yet (password_hash
// NULL), which LoginSubmit already refuses to authenticate against - so an
// invited-but-not-yet-accepted parent naturally can't log in until they
// complete the invite-accept flow and set their own password.
func (s *UserStore) InviteParent(ctx context.Context, name, email string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_users (name, role, email, auth_provider, invited_at) VALUES ($1, 'parent', $2, 'local', now()) RETURNING id`,
		name, email).Scan(&id)
	return id, err
}

// SetPasswordAndAccept sets the password for an invited parent, completing
// their invite acceptance.
func (s *UserStore) SetPasswordAndAccept(ctx context.Context, id int, passwordHash string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_users SET password_hash = $1 WHERE id = $2 AND role = 'parent'`, passwordHash, id)
	return err
}

// SetPassword updates an existing parent's password hash, e.g. after they
// complete the forgot-password reset flow. Returns ErrNotFound if id doesn't
// match an existing parent (e.g. the account was deleted between the caller
// verifying the request and calling this), rather than silently succeeding
// with zero rows affected.
func (s *UserStore) SetPassword(ctx context.Context, id int, passwordHash string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE hhq_users SET password_hash = $1 WHERE id = $2 AND role = 'parent'`, passwordHash, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkInvited refreshes invited_at, used when resending an invite email.
func (s *UserStore) MarkInvited(ctx context.Context, id int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_users SET invited_at = now() WHERE id = $1 AND role = 'parent'`, id)
	return err
}

func (s *UserStore) UpdateChild(ctx context.Context, id int, name, color string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_users SET name = $1, color = $2 WHERE id = $3 AND role = 'child'`, name, color, id)
	return err
}

// SetDisplayName sets or clears a parent's kiosk display name (e.g.
// "Mom"/"Dad"). An empty string clears it, falling back to the real name
// (see User.DisplayLabel).
func (s *UserStore) SetDisplayName(ctx context.Context, id int, displayName string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_users SET display_name = $1 WHERE id = $2 AND role = 'parent'`,
		nullableString(displayName), id)
	return err
}

func (s *UserStore) Deactivate(ctx context.Context, id int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_users SET is_active = FALSE WHERE id = $1`, id)
	return err
}

// SetAvatar stores an avatar image for any user (child or parent). data
// should already be validated (see internal/handlers/avatar.go's
// validateAvatarBytes) before calling this.
func (s *UserStore) SetAvatar(ctx context.Context, id int, data []byte, contentType string) error {
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_users
		SET avatar_image = $1, avatar_content_type = $2, avatar_checksum = $3, avatar_updated_at = now()
		WHERE id = $4`,
		data, contentType, checksum, id)
	return err
}

// ClearAvatar removes a user's avatar, falling back to the color swatch
// everywhere it's displayed.
func (s *UserStore) ClearAvatar(ctx context.Context, id int) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_users
		SET avatar_image = NULL, avatar_content_type = NULL, avatar_checksum = NULL, avatar_updated_at = NULL
		WHERE id = $1`, id)
	return err
}

// GetAvatarByID is the only query that ever selects avatar_image - used by
// the GET /avatars/{id} serving route. ok is false if the user doesn't exist
// or has no avatar set.
func (s *UserStore) GetAvatarByID(ctx context.Context, id int) (data []byte, contentType string, updatedAt time.Time, ok bool, err error) {
	var nData []byte
	var nContentType sql.NullString
	var nUpdatedAt sql.NullTime
	row := s.DB.QueryRowContext(ctx, `SELECT avatar_image, avatar_content_type, avatar_updated_at FROM hhq_users WHERE id = $1`, id)
	if err = row.Scan(&nData, &nContentType, &nUpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", time.Time{}, false, nil
		}
		return nil, "", time.Time{}, false, err
	}
	if nData == nil {
		return nil, "", time.Time{}, false, nil
	}
	return nData, nContentType.String, nUpdatedAt.Time, true, nil
}

// GetAvatarChecksum is a cheap lookup (never selects the avatar_image BYTEA)
// used only by BootstrapChildren to decide whether a configured avatar_file's
// on-disk bytes have actually changed since the last reconcile pass, so an
// unchanged file doesn't rewrite the row or bump avatar_updated_at (which
// cache-busts the served image's URL) on every restart. ok is false if the
// user doesn't exist or has no avatar set.
func (s *UserStore) GetAvatarChecksum(ctx context.Context, id int) (checksum string, ok bool, err error) {
	var nChecksum sql.NullString
	row := s.DB.QueryRowContext(ctx, `SELECT avatar_checksum FROM hhq_users WHERE id = $1`, id)
	if err = row.Scan(&nChecksum); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if !nChecksum.Valid {
		return "", false, nil
	}
	return nChecksum.String, true, nil
}

func scanUsers(rows *sql.Rows) ([]User, error) {
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Role, &u.Color, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AuthProvider, &u.ExternalSubject, &u.IsActive, &u.CreatedAt, &u.InvitedAt, &u.BootstrapManaged, &u.HasAvatar, &u.AvatarUpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Name, &u.Role, &u.Color, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AuthProvider, &u.ExternalSubject, &u.IsActive, &u.CreatedAt, &u.InvitedAt, &u.BootstrapManaged, &u.HasAvatar, &u.AvatarUpdatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}
