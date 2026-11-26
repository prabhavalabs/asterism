package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestIconScannerUsesHostRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "srv", "shop", "public")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), svg, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Host: config.Host{Root: root}}
	store := state.NewStore(state.Options{})
	store.UpsertContainer(&v1.Container{Id: "ctr:shop-web", Name: "shop-web", ComposeWorkingDir: "/srv/shop"})
	store.UpsertContainer(&v1.Container{Id: "ctr:other", Name: "other", ComposeWorkingDir: "/srv/none"})
	s := newIconScanner(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.scan()

	snap := store.Snapshot()
	if len(snap.GetProjectIcons()) != 1 {
		t.Fatalf("icons = %v", snap.GetProjectIcons())
	}
	ref := snap.GetProjectIcons()[0]
	if ref.GetWorkingDir() != "/srv/shop" || ref.GetRelPath() != "public/favicon.svg" || ref.GetSizeBytes() != uint32(len(svg)) {
		t.Fatalf("ref = %v", ref)
	}
	data, err := s.ReadIcon(ref.GetSha256())
	if err != nil || string(data) != string(svg) {
		t.Fatalf("ReadIcon = %q %v", data, err)
	}
	if _, err := s.ReadIcon("0000"); err == nil {
		t.Fatal("unknown digest read")
	}
	// The file changes after the scan: the read is refused.
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><g/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadIcon(ref.GetSha256()); err == nil {
		t.Fatal("changed file served")
	}
	// Reads are rate limited.
	s.readsLeft = 0
	if _, err := s.ReadIcon(ref.GetSha256()); err == nil {
		t.Fatal("rate limit not applied")
	}
}
