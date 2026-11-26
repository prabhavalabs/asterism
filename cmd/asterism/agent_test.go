package main

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/config"
)

func TestAgentTokenCreatePrintsUsableConfig(t *testing.T) {
	var out bytes.Buffer
	if err := printAgentToken(&out, "prod-1", true); err != nil {
		t.Fatal(err)
	}
	tok := regexp.MustCompile(`asta_[A-Za-z0-9_-]{43}`).FindString(out.String())
	if tok == "" {
		t.Fatalf("no token in output:\n%s", out.String())
	}
	// The printed snippet loads as-is and its digest matches the token.
	snippet := out.String()[strings.Index(out.String(), "[collect]"):strings.Index(out.String(), "\nOn the agent host")]
	path := t.TempDir() + "/asterism.toml"
	if err := os.WriteFile(path, []byte(snippet), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("snippet does not load: %v\n%s", err, snippet)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "prod-1" || !cfg.Agents[0].AllowActions || cfg.Agents[0].SHA256 != auth.TokenDigest(tok) || cfg.Collect.Mode != "agent" {
		t.Fatalf("agents = %+v", cfg.Agents)
	}

	for _, bad := range []string{"", "@prod", "has space"} {
		if err := printAgentToken(&out, bad, false); err == nil {
			t.Fatalf("host %q accepted", bad)
		}
	}
}
