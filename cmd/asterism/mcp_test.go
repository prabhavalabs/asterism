package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHubURLFromListen(t *testing.T) {
	for listen, want := range map[string]string{
		"127.0.0.1:7700": "http://127.0.0.1:7700",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		":7700":          "http://127.0.0.1:7700",
		"[::]:7700":      "http://127.0.0.1:7700",
		"[::1]:7700":     "http://[::1]:7700",
	} {
		got, err := hubURLFromListen(listen)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", listen, got, err, want)
		}
	}
	if _, err := hubURLFromListen("no-port"); err == nil {
		t.Error("want an error for a listen address without a port")
	}
}

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
