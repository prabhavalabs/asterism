// Package secretbox encrypts small secrets at rest with AES-256-GCM using a
// key file in the data directory (<data_dir>/secret.key, mode 0600).
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// version prefixes every sealed blob so the format can change later.
const version byte = 1

// ErrDecrypt is returned when a blob cannot be authenticated with the key.
var ErrDecrypt = errors.New("secretbox: decryption failed (wrong key or corrupted data)")

// Box seals and opens secrets with one key.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secretbox: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	return &Box{aead: aead}, nil
}

// LoadOrCreateKey reads the key file at path, creating it with a random key
// (mode 0600) when it does not exist. An existing file with group or world
// permissions is tightened to 0600.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(key) != KeySize {
			return nil, fmt.Errorf("secretbox: %s holds %d bytes, want %d; restore the original key or remove the file and re-enter channel secrets", path, len(key), KeySize)
		}
		if st, statErr := os.Stat(path); statErr == nil && st.Mode().Perm()&0o077 != 0 {
			if chErr := os.Chmod(path, 0o600); chErr != nil {
				return nil, fmt.Errorf("secretbox: tighten %s to 0600: %w", path, chErr)
			}
		}
		return key, nil
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, fmt.Errorf("secretbox: read %s: %w", path, err)
	}
	key = make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secretbox: generate key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("secretbox: create %s: %w", path, err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("secretbox: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("secretbox: close %s: %w", path, err)
	}
	return key, nil
}

// Seal encrypts plaintext. aad binds the blob to its owner (for example a
// channel id) so blobs cannot be swapped between rows.
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+b.aead.Overhead())
	out = append(out, version)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts a blob produced by Seal with the same aad.
func (b *Box) Open(blob, aad []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(blob) < 1+ns+b.aead.Overhead() || blob[0] != version {
		return nil, ErrDecrypt
	}
	pt, err := b.aead.Open(nil, blob[1:1+ns], blob[1+ns:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
