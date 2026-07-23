// Package util holds small, dependency-free helpers used across the app.
package util

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Encryptor wraps an AES-256-GCM key used to encrypt sensitive values
// (currently just CalDAV account passwords) before storing them in Postgres.
// This protects credentials if the database is ever exposed independently
// of the application (backups, read replicas, etc.) — it is NOT a substitute
// for restricting DB access in the first place.
type Encryptor struct {
	gcm cipher.AEAD
}

// NewEncryptor builds an Encryptor from a hex-encoded 32-byte key, typically
// supplied via the ENCRYPTION_KEY environment variable (see deploy/k8s/secret-example.yaml).
// Generate one with: openssl rand -hex 32
func NewEncryptor(hexKey string) (*Encryptor, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be hex-encoded: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must decode to exactly 32 bytes (64 hex chars)")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	return &Encryptor{gcm: gcm}, nil
}

// Encrypt returns nonce||ciphertext as raw bytes, suitable for a BYTEA column.
func (e *Encryptor) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return e.gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (e *Encryptor) Decrypt(data []byte) (string, error) {
	nonceSize := e.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := e.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting: %w", err)
	}
	return string(plaintext), nil
}
