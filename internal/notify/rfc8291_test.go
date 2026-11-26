package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

// decryptAES128GCM implements the user-agent side of RFC 8291 / RFC 8188
// (single record) for tests.
func decryptAES128GCM(t *testing.T, body []byte, ua *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatal("body too short")
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idlen := int(body[20])
	asPub := body[21 : 21+idlen]
	ct := body[21+idlen:]
	if rs < 18 {
		t.Fatalf("record size %d", rs)
	}
	peer, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ua.ECDH(peer)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...)
	info = append(info, asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	// Strip padding: content, 0x02 delimiter (last record), zeros.
	i := len(pt) - 1
	for i >= 0 && pt[i] == 0 {
		i--
	}
	if i < 0 || pt[i] != 2 {
		t.Fatalf("bad padding delimiter")
	}
	return pt[:i]
}
