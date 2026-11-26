package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMCPToken(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "mcp.token")
	if err := os.WriteFile(file, []byte("  asterism_abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveMCPToken(file); err != nil || got != "asterism_abc" {
		t.Fatalf("file: %q %v", got, err)
	}
	t.Setenv("ASTERISM_TOKEN", "from-env")
	if got, _ := resolveMCPToken(""); got != "from-env" {
		t.Fatalf("env: %q", got)
	}
	if _, err := resolveMCPToken(filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "asterism token generate") {
		t.Fatalf("missing file should explain how to create a token: %v", err)
	}
}
