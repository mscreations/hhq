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

import "golang.org/x/crypto/bcrypt"

func HashPassword(plaintext string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// dummyHash is a bcrypt hash of a fixed, arbitrary string, precomputed once
// at startup. It exists purely so CheckPasswordTiming can pay the same
// bcrypt cost when there's no real stored hash to compare against (e.g. the
// submitted login email doesn't match any account) as when there is -
// otherwise a login handler that skips the bcrypt call entirely on unknown
// emails responds measurably faster for those than for known ones, letting
// an attacker enumerate valid emails by timing responses.
var dummyHash string

func init() {
	dummyHash = mustHashPassword("hhq-login-timing-mitigation-placeholder")
}

// mustHashPassword hashes plaintext or panics, extracted from init() so the
// panic path can be exercised directly in tests (bcrypt only fails this call
// in practice for a too-long plaintext, which init()'s fixed placeholder
// string never triggers).
func mustHashPassword(plaintext string) string {
	h, err := HashPassword(plaintext)
	if err != nil {
		panic("auth: failed to precompute dummy bcrypt hash: " + err.Error())
	}
	return h
}

// CheckPasswordTiming behaves like CheckPassword, except when hash is empty
// (no real account/stored hash to check against) it still performs a bcrypt
// comparison against a fixed dummy hash and always returns false - see
// dummyHash for why. Callers should pass "" for hash whenever the submitted
// identifier (e.g. email) didn't resolve to a real, checkable account.
func CheckPasswordTiming(hash, plaintext string) bool {
	if hash == "" {
		CheckPassword(dummyHash, plaintext)
		return false
	}
	return CheckPassword(hash, plaintext)
}
