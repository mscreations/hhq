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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type Session struct {
	Token     string
	UserID    int
	CreatedAt time.Time
	ExpiresAt time.Time
}

type SessionStore struct {
	DB *sql.DB
}

// Create generates a new opaque session token and stores it with an expiry.
func (s *SessionStore) Create(ctx context.Context, userID int, ttl time.Duration) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)

	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO hhq_sessions (token, user_id, expires_at) VALUES ($1, $2, $3)`,
		token, userID, time.Now().Add(ttl))
	if err != nil {
		return "", err
	}
	return token, nil
}

// Get returns the session if it exists and hasn't expired.
func (s *SessionStore) Get(ctx context.Context, token string) (*Session, error) {
	var sess Session
	err := s.DB.QueryRowContext(ctx, `
		SELECT token, user_id, created_at, expires_at FROM hhq_sessions WHERE token = $1`, token).
		Scan(&sess.Token, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, ErrNotFound
	}
	return &sess, nil
}

func (s *SessionStore) Delete(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM hhq_sessions WHERE token = $1`, token)
	return err
}

// DeleteAllForUser deletes every session belonging to the given user, e.g.
// after a password reset - so a pre-existing session cookie (possibly an
// attacker's, if the reset was prompted by a suspected compromise) doesn't
// outlive the change.
func (s *SessionStore) DeleteAllForUser(ctx context.Context, userID int) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM hhq_sessions WHERE user_id = $1`, userID)
	return err
}

// DeleteExpired is intended to be called periodically (see internal/scheduler) to
// keep the sessions table from growing unbounded.
func (s *SessionStore) DeleteExpired(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM hhq_sessions WHERE expires_at < now()`)
	return err
}
