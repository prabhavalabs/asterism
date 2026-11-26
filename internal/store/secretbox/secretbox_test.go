package secretbox

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyFileCreatedWith0600AndReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v, want 0600", st.Mode().Perm())
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) || len(k1) != KeySize {
		t.Fatal("key not reused")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("loose key mode not tightened: %v", st.Mode().Perm())
	}
}

func TestKeyFileWrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("want error for short key")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key, _ := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(`{"webhook_url":"https://hooks.slack.com/services/T/B/xyz"}`)
	blob, err := box.Seal(secret, []byte("chan-1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("hooks.slack.com")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := box.Open(blob, []byte("chan-1"))
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := box.Open(blob, []byte("chan-2")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong aad: %v", err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := box.Open(blob, []byte("chan-1")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered: %v", err)
	}
	other, _ := New(bytes.Repeat([]byte{7}, KeySize))
	good, _ := box.Seal(secret, nil)
	if _, err := other.Open(good, nil); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := box.Open([]byte{1, 2}, nil); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("short blob: %v", err)
	}
}
