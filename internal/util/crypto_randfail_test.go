package util

import (
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"
)

// failingReader always returns an error, used to exercise Encrypt's
// io.ReadFull(rand.Reader, nonce) failure branch (crypto.go line ~50) - a
// branch that's otherwise unreachable in practice, since crypto/rand.Reader
// only errors if the OS's real entropy source itself fails. crypto/rand.Reader
// is an ordinary exported package variable, so it can be swapped for the
// duration of this test and restored afterward (not run in parallel with any
// other test that relies on real randomness).
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("simulated entropy source failure")
}

func TestEncryptReturnsErrorWhenRandReaderFails(t *testing.T) {
	enc, err := NewEncryptor(validHexKey())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	original := rand.Reader
	rand.Reader = failingReader{}
	defer func() { rand.Reader = original }()

	_, err = enc.Encrypt("whatever")
	if err == nil {
		t.Fatal("expected Encrypt to fail when rand.Reader errors")
	}
	if !strings.Contains(err.Error(), "generating nonce") {
		t.Fatalf("error = %v, want it to include the 'generating nonce' wrap context", err)
	}
}

// TestEncryptStillSucceedsWithRealReader is a light sanity check that the
// package-level rand.Reader swap above doesn't leak across tests - if it did,
// this would fail too (proving isolation rather than assuming it).
func TestEncryptStillSucceedsWithRealReader(t *testing.T) {
	enc, err := NewEncryptor(validHexKey())
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	if _, err := enc.Encrypt("whatever"); err != nil {
		t.Fatalf("Encrypt with the real rand.Reader: %v", err)
	}
	var _ io.Reader = rand.Reader
}
