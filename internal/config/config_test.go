package config

import (
	"os"
	"testing"
	"time"
)

func TestExampleConfigLoads(t *testing.T) {
	cfg, _, err := Load("../../asterism.example.toml")
	if err != nil {
		t.Fatalf("Load example: %v", err)
	}
	if cfg.Actions.AllowActions || cfg.Actions.AllowProtected {
		t.Fatal("example config must be read-only by default")
	}
	if cfg.Actions.StopTimeout() != 10*time.Second {
		t.Fatalf("stop timeout = %v", cfg.Actions.StopTimeout())
	}
}

func TestStopTimeoutBounds(t *testing.T) {
	tests := []struct {
		in   int
		want time.Duration
	}{
		{0, 10 * time.Second},
		{-5, 10 * time.Second},
		{45, 45 * time.Second},
		{900, 300 * time.Second},
	}
	for _, tc := range tests {
		if got := (ActionsConfig{DefaultStopTimeoutSeconds: tc.in}).StopTimeout(); got != tc.want {
			t.Fatalf("StopTimeout(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestProjectDisplayConfig(t *testing.T) {
	path := t.TempDir() + "/asterism.toml"
	body := "[projects.\"shop\"]\nicon = \"postgres\"\ncolor = \"chart-3\"\n\n[projects.\"staging-lab\"]\ncolor = \"role-db\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Projects["shop"]; got.Icon != "postgres" || got.Color != "chart-3" {
		t.Fatalf("shop = %+v", got)
	}
	if got := cfg.Projects["staging-lab"]; got.Icon != "" || got.Color != "role-db" {
		t.Fatalf("staging-lab = %+v", got)
	}
}

func TestShowcaseFleetConfig(t *testing.T) {
	path := t.TempDir() + "/asterism.toml"
	if err := os.WriteFile(path, []byte("[collect]\nmode = \"showcase\"\nshowcase_fleet = 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil || cfg.Collect.ShowcaseFleet != 6 {
		t.Fatalf("fleet = %d, err = %v", cfg.Collect.ShowcaseFleet, err)
	}
	t.Setenv("ASTERISM_SHOWCASE_FLEET", "12")
	if cfg, _, _ = Load(path); cfg.Collect.ShowcaseFleet != 12 {
		t.Fatalf("env fleet = %d", cfg.Collect.ShowcaseFleet)
	}
	t.Setenv("ASTERISM_SHOWCASE_FLEET", "-3")
	if cfg, _, _ = Load(path); cfg.Collect.ShowcaseFleet != 6 {
		t.Fatalf("negative env fleet applied: %d", cfg.Collect.ShowcaseFleet)
	}
}

func TestNormalizePublicURL(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "", want: ""},
		{in: "https://monitor.example.com", want: "https://monitor.example.com"},
		{in: " https://monitor.example.com/ ", want: "https://monitor.example.com"},
		{in: "http://127.0.0.1:7700", want: "http://127.0.0.1:7700"},
		{in: "monitor.example.com", wantErr: true},
		{in: "ftp://monitor.example.com", wantErr: true},
		{in: "https://monitor.example.com/app", wantErr: true},
		{in: "https://monitor.example.com/?a=1", wantErr: true},
		{in: "https://user@monitor.example.com", wantErr: true},
		{in: `https://evil.com"><script>`, wantErr: true},
	}
	for _, tt := range tests {
		got, err := normalizePublicURL(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("normalizePublicURL(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got != tt.want {
			t.Fatalf("normalizePublicURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
