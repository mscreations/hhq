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
	"net/http"
)

// CSRFManager issues and verifies CSRF tokens for the parent dashboard's
// state-changing forms. Rather than storing a token per-session in the
// database, the token is derived deterministically from the session cookie
// value via HMAC: anyone who can read the session cookie (i.e. the
// legitimate browser rendering the form) can compute the correct token, but
// a cross-site attacker - who can only get the browser to *send* the cookie
// on a forged request, not read its value - cannot.
type CSRFManager struct {
	secret []byte
}

func NewCSRFManager(secret string) *CSRFManager {
	return &CSRFManager{secret: []byte(secret)}
}

// Token derives the CSRF token for a given session token.
func (c *CSRFManager) Token(sessionToken string) string {
	h := hmac.New(sha256.New, c.secret)
	h.Write([]byte(sessionToken))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Verify checks a submitted CSRF token against the expected value for the
// given session token, in constant time.
func (c *CSRFManager) Verify(sessionToken, submitted string) bool {
	if submitted == "" || sessionToken == "" {
		return false
	}
	expected := c.Token(sessionToken)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(submitted)) == 1
}

// FormFieldName is the form field name templates should use for the hidden
// CSRF input.
const CSRFFormFieldName = "csrf_token"

// VerifyCSRF is middleware that rejects unsafe-method requests (anything but
// GET/HEAD/OPTIONS) lacking a valid CSRF token bound to the current session.
// Mount it inside the RequireParent-protected route group, after
// SessionManager.LoadUser has already run.
func (sm *SessionManager) VerifyCSRF(csrf *CSRFManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}

			sessionToken, ok := sm.SessionToken(r)
			if !ok {
				http.Error(w, "missing session", http.StatusForbidden)
				return
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if !csrf.Verify(sessionToken, r.FormValue(CSRFFormFieldName)) {
				http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRFToken returns the CSRF token for the current request's session, for
// embedding in a hidden form field when rendering a page. Returns "" if
// there's no session (the form's own submission will then be rejected by
// VerifyCSRF, same as any other unauthenticated POST).
func (sm *SessionManager) CSRFToken(csrf *CSRFManager, r *http.Request) string {
	sessionToken, ok := sm.SessionToken(r)
	if !ok {
		return ""
	}
	return csrf.Token(sessionToken)
}
