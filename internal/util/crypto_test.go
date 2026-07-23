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

package util

import (
	"strings"
	"testing"
)

func validHexKey() string {
	return strings.Repeat("ab", 32) // 64 hex chars -> 32 bytes
}

func TestNewEncryptor(t *testing.T) {
	tests := []struct {
		name    string
		hexKey  string
		wantErr string
	}{
		{name: "valid 32-byte key", hexKey: validHexKey()},
		{name: "not hex", hexKey: "not-hex-at-all", wantErr: "hex-encoded"},
		{name: "too short", hexKey: strings.Repeat("ab", 16), wantErr: "32 bytes"},
		{name: "too long", hexKey: strings.Repeat("ab", 64), wantErr: "32 bytes"},
		{name: "empty", hexKey: "", wantErr: "32 bytes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := NewEncryptor(tt.hexKey)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if enc == nil {
					t.Fatal("expected non-nil encryptor")
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	enc, err := NewEncryptor(validHexKey())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	plaintexts := []string{
		"",
		"hunter2",
		"a very long app-specific password with spaces and symbols !@#$%^&*()",
		"unicode: 密码 pässwörd",
	}

	for _, pt := range plaintexts {
		ct, err := enc.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		got, err := enc.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt round trip for %q: %v", pt, err)
		}
		if got != pt {
			t.Fatalf("round trip mismatch: got %q, want %q", got, pt)
		}
	}
}

func TestEncryptProducesUniqueNoncePerCall(t *testing.T) {
	enc, err := NewEncryptor(validHexKey())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	ct1, err := enc.Encrypt("same plaintext")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct2, err := enc.Encrypt("same plaintext")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if string(ct1) == string(ct2) {
		t.Fatal("expected different ciphertexts for the same plaintext due to random nonce")
	}
}

func TestDecryptRejectsShortCiphertext(t *testing.T) {
	enc, err := NewEncryptor(validHexKey())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	_, err = enc.Decrypt([]byte("short"))
	if err == nil {
		t.Fatal("expected error for ciphertext shorter than nonce size")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("expected 'too short' error, got: %v", err)
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	enc, err := NewEncryptor(validHexKey())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	ct, err := enc.Encrypt("sensitive value")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte{}, ct...)
	tampered[len(tampered)-1] ^= 0xFF

	if _, err := enc.Decrypt(tampered); err == nil {
		t.Fatal("expected error decrypting tampered ciphertext")
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	enc1, err := NewEncryptor(validHexKey())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	enc2, err := NewEncryptor(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	ct, err := enc1.Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if _, err := enc2.Decrypt(ct); err == nil {
		t.Fatal("expected error decrypting with the wrong key")
	}
}
