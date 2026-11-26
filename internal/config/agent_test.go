package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := t.TempDir() + "/asterism.toml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const agentDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAgentModeConfig(t *testing.T) {
	cfg, _, err := Load(writeConfig(t, `
[collect]
mode = "agent"

[collect.agent]
primary_host = "prod-1"
stale_after = "45s"

[[agents]]
name = "edge-2"
sha256 = "`+agentDigest+`"

[[agents]]
name = "prod-1"
sha256 = "`+agentDigest+`"
machine_id = "abc"
allow_actions = true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Collect.Mode != "agent" || cfg.Collect.Agent.StaleAfter != 45*time.Second || cfg.PrimaryAgent() != "prod-1" {
		t.Fatalf("collect = %+v", cfg.Collect)
	}
	if len(cfg.Agents) != 2 || !cfg.Agents[1].AllowActions || cfg.Agents[1].MachineID != "abc" || cfg.Agents[0].AllowActions {
		t.Fatalf("agents = %+v", cfg.Agents)
	}
}

func TestAgentModeDefaultsAndErrors(t *testing.T) {
	cfg, _, err := Load(writeConfig(t, "[collect]\nmode = \"agent\"\n[[agents]]\nname = \"solo\"\nsha256 = \""+agentDigest+"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collect.Agent.StaleAfter != 30*time.Second || cfg.PrimaryAgent() != "solo" {
		t.Fatalf("defaults: %+v primary %q", cfg.Collect.Agent, cfg.PrimaryAgent())
	}
	for name, body := range map[string]string{
		"no agents":       "[collect]\nmode = \"agent\"\n",
		"unknown primary": "[collect]\nmode = \"agent\"\n[collect.agent]\nprimary_host = \"x\"\n[[agents]]\nname = \"solo\"\nsha256 = \"" + agentDigest + "\"\n",
		"stale too short": "[collect.agent]\nstale_after = \"1s\"\n",
		"duplicate name":  "[[agents]]\nname = \"a\"\nsha256 = \"" + agentDigest + "\"\n[[agents]]\nname = \"a\"\nsha256 = \"" + agentDigest + "\"\n",
		"nameless agent":  "[[agents]]\nsha256 = \"" + agentDigest + "\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	t.Setenv("ASTERISM_COLLECT_MODE", "agent")
	if _, _, err := Load(writeConfig(t, "")); err == nil || !strings.Contains(err.Error(), "agent token create") {
		t.Fatalf("env agent mode without agents: %v", err)
	}
}
