package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhavalabs/asterism/internal/agent/config"
)

// TestDeployExamples parses and validates the example agent.yaml files
// shipped in deploy/agent/examples, with the environment the compose
// snippet and systemd unit provide.
func TestDeployExamples(t *testing.T) {
	files, err := filepath.Glob("../../deploy/agent/examples/*.yaml")
	if err != nil || len(files) != 3 {
		t.Fatalf("examples: %v %v", files, err)
	}
	tok := filepath.Join(t.TempDir(), "agent.token")
	if err := os.WriteFile(tok, []byte("asta_example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{config.EnvTokenFile: tok}
			if strings.Contains(f, "compose") {
				env[config.EnvHostRoot] = "/host"
				env[config.EnvDockerEndpoint] = "tcp://socket-proxy:2375"
			}
			cfg, err := config.Parse(b, f, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if is := cfg.Validate(); is.HasErrors() {
				t.Fatalf("validate: %v", is.Errors())
			}
			if _, err := cfg.ResolveToken(); err != nil {
				t.Fatalf("token: %v", err)
			}
			if cfg.Actions.Enabled {
				t.Error("shipped examples must be read-only (actions.enabled: false)")
			}
		})
	}
}
