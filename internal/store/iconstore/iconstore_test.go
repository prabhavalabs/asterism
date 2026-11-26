package iconstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	k := Key("../../etc/passwd")
	if !ValidKey(k) || strings.ContainsAny(k, "./") || k == Key("shop") {
		t.Fatalf("Key = %q", k)
	}
	for _, bad := range []string{"", "x", strings.Repeat("A", 32), "../" + k[:29], k + "0"} {
		if ValidKey(bad) {
			t.Fatalf("ValidKey(%q)", bad)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("<svg/>")
	m, err := s.Put(SlotUploaded, "shop", data, Meta{ContentType: "image/svg+xml", Source: "upload"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "shop" || len(m.SHA256) != 64 || m.Size != len(data) || m.ETag() == "" {
		t.Fatalf("meta = %+v", m)
	}
	for _, sl := range []Slot{SlotUploaded, SlotFavicon} {
		st, err := os.Stat(filepath.Join(dir, "icons", string(sl)))
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s dir: %v %v", sl, st, err)
		}
	}
	st, err := os.Stat(filepath.Join(dir, "icons", "uploaded", Key("shop")+".img"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("img: %v %v", st, err)
	}

	// Negative favicon entry plus a reopen.
	if _, err := s.Update(SlotFavicon, "blog", func(m *Meta) { m.CheckedMs, m.Status = 42, "no favicon found" }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "icons", "favicon", ".tmp-123"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s2.Get(SlotUploaded, "shop"); !ok || got.SHA256 != m.SHA256 {
		t.Fatalf("reopen uploaded = %+v %v", got, ok)
	}
	if got, ok := s2.Get(SlotFavicon, "blog"); !ok || got.HasImage() || got.Status != "no favicon found" {
		t.Fatalf("reopen favicon = %+v %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "icons", "favicon", ".tmp-123")); !os.IsNotExist(err) {
		t.Fatal("temp file not cleaned")
	}
	b, _, err := s2.Read(SlotUploaded, Key("shop"))
	if err != nil || string(b) != "<svg/>" {
		t.Fatalf("Read = %q %v", b, err)
	}
	if _, _, err := s2.Read(SlotUploaded, "../../x"); err == nil {
		t.Fatal("invalid key read")
	}

	// Tampered bytes are refused.
	if err := os.WriteFile(filepath.Join(dir, "icons", "uploaded", Key("shop")+".img"), []byte("<svg>evil</svg>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.Read(SlotUploaded, Key("shop")); err == nil {
		t.Fatal("tampered image served")
	}

	if err := s2.Delete(SlotUploaded, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get(SlotUploaded, "shop"); ok {
		t.Fatal("still present after delete")
	}
	if err := s2.Delete(SlotUploaded, "shop"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestOpenNeedsAbsolute(t *testing.T) {
	if _, err := Open("relative"); err == nil {
		t.Fatal("relative dir accepted")
	}
}
