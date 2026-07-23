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

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ApprovalLinkSigner signs/verifies the approve & reject links sent in parent
// notification emails, so a parent can click "Approve" or "Reject" directly
// from their email client with no login required — while still preventing
// the link from being forged or replayed past its expiry.
//
// Token format (then base64url-encoded as a whole): "<instanceID>.<action>.<expiryUnix>.<hmac>"
type ApprovalLinkSigner struct {
	secret []byte
}

func NewApprovalLinkSigner(secret string) *ApprovalLinkSigner {
	return &ApprovalLinkSigner{secret: []byte(secret)}
}

type Action string

const (
	ActionApprove Action = "approve"
	ActionReject  Action = "reject"

	// ActionInviteAccept marks a token signed by a separate signer (see
	// cfg.InviteSecret / handlers.App.Invite) used for parent invite links,
	// rather than chore-approval links. Kept in this same Action type since
	// Sign/Verify are already generic over (id, action, ttl).
	ActionInviteAccept Action = "invite_accept"

	// ActionPasswordReset marks a token signed by yet another separate
	// signer (see cfg.PasswordResetSecret / handlers.App.PasswordReset) used
	// for "forgot password" reset links.
	ActionPasswordReset Action = "password_reset"

	// ActionGoogleOAuthConnect marks a token signed by yet another separate
	// signer (see handlers.App.GoogleOAuthState) used as the OAuth2 "state"
	// parameter for the Google Calendar connect flow. Its id field holds 0
	// for a fresh connect, or an existing Google account's ID when the token
	// was minted for a "Reconnect" link (see internal/handlers/google_oauth.go).
	ActionGoogleOAuthConnect Action = "google_oauth_connect"
)

// Sign produces an opaque, URL-safe token for the given chore instance + action,
// valid until now()+ttl.
func (s *ApprovalLinkSigner) Sign(instanceID int, action Action, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%d.%s.%d", instanceID, action, exp)
	mac := s.mac(payload)
	raw := payload + "." + mac
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

var ErrInvalidToken = errors.New("invalid or expired approval token")

// Verify decodes and validates a token, returning the chore instance ID and action.
func (s *ApprovalLinkSigner) Verify(token string) (instanceID int, action Action, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, "", ErrInvalidToken
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 4 {
		return 0, "", ErrInvalidToken
	}
	idStr, actionStr, expStr, sig := parts[0], parts[1], parts[2], parts[3]
	payload := idStr + "." + actionStr + "." + expStr

	expectedSig := s.mac(payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) != 1 {
		return 0, "", ErrInvalidToken
	}

	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, "", ErrInvalidToken
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		return 0, "", ErrInvalidToken
	}

	return id, Action(actionStr), nil
}

func (s *ApprovalLinkSigner) mac(payload string) string {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// PeekID extracts the unverified id embedded in a token, without checking its
// signature or expiry. The returned id must not be trusted for anything
// beyond looking up context data (e.g. a user row) needed to perform a real
// VerifyWithContext call on the same token - an attacker can set this field
// to anything, since it isn't authenticated until that later check passes.
func PeekID(token string) (id int, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(string(raw), ".")

	id, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	return id, true
}

// SignWithContext is like Sign, but additionally binds the token to a
// caller-supplied context string (e.g. a user's current password hash) so
// the token self-invalidates the moment that context changes - see
// VerifyWithContext. Used for password-reset links so a token can't be
// replayed after the password it was issued for has already been changed
// once, without needing a separate "used tokens" table.
func (s *ApprovalLinkSigner) SignWithContext(id int, action Action, ttl time.Duration, context string) string {
	exp := time.Now().Add(ttl).Unix()
	ctxHash := s.mac("ctx:" + context)
	payload := fmt.Sprintf("%d.%s.%d.%s", id, action, exp, ctxHash)
	mac := s.mac(payload)
	raw := payload + "." + mac
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// VerifyWithContext decodes and validates a token produced by
// SignWithContext, additionally requiring the caller-supplied context to
// match what the token was signed with. Pass the target's *current* state
// (e.g. password hash) as context: if it has changed since the token was
// issued - including via an earlier use of this same token - the token is
// rejected even though its signature and expiry are still otherwise valid.
func (s *ApprovalLinkSigner) VerifyWithContext(token, context string) (id int, action Action, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, "", ErrInvalidToken
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 5 {
		return 0, "", ErrInvalidToken
	}
	idStr, actionStr, expStr, ctxHash, sig := parts[0], parts[1], parts[2], parts[3], parts[4]
	payload := idStr + "." + actionStr + "." + expStr + "." + ctxHash

	expectedSig := s.mac(payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) != 1 {
		return 0, "", ErrInvalidToken
	}

	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, "", ErrInvalidToken
	}

	expectedCtxHash := s.mac("ctx:" + context)
	if subtle.ConstantTimeCompare([]byte(ctxHash), []byte(expectedCtxHash)) != 1 {
		return 0, "", ErrInvalidToken
	}

	id, err = strconv.Atoi(idStr)
	if err != nil {
		return 0, "", ErrInvalidToken
	}

	return id, Action(actionStr), nil
}
