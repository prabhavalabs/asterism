package config

import (
	"os"
	"path/filepath"
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

func TestDataDirAndAlertsConfig(t *testing.T) {
	d := Default()
	if d.Server.DataDir != "data" || !d.Alerts.Enabled || d.Alerts.EvaluationInterval != 15*time.Second {
		t.Fatalf("defaults = %+v / %+v", d.Server, d.Alerts)
	}
	dir := filepath.Join(t.TempDir(), "state", "asterism")
	t.Setenv("ASTERISM_DATA_DIR", dir)
	t.Setenv("ASTERISM_ALERTS_EVALUATION_INTERVAL", "30s")
	t.Setenv("ASTERISM_ALERTS_ALLOW_PRIVATE_TARGETS", "true")
	cfg, _, err := Load(filepath.Join(t.TempDir(), "missing-ok.toml"))
	if err == nil {
		t.Fatal("expected error for a missing explicit config path")
	}
	path := filepath.Join(t.TempDir(), "a.toml")
	if err := os.WriteFile(path, []byte("[alerts]\nenabled = false\ngroup_wait = \"10s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alerts.Enabled || cfg.Alerts.EvaluationInterval != 30*time.Second || cfg.Alerts.GroupWait != 10*time.Second || !cfg.Alerts.AllowPrivateTargets || cfg.Alerts.RepeatInterval != 4*time.Hour {
		t.Fatalf("alerts = %+v", cfg.Alerts)
	}
	got, err := cfg.EnsureDataDir()
	if err != nil || got != dir {
		t.Fatalf("EnsureDataDir = %q, %v", got, err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode = %v, %v", st.Mode().Perm(), err)
	}
	if err := os.WriteFile(path, []byte("[alerts]\nevaluation_interval = \"1s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTERISM_ALERTS_EVALUATION_INTERVAL", "")
	if _, _, err := Load(path); err == nil {
		t.Fatal("1s evaluation interval accepted")
	}
}

func TestFaviconFetchConfig(t *testing.T) {
	path := t.TempDir() + "/asterism.toml"
	body := "[projects]\nfavicon_fetch = true\n\n[projects.\"shop\"]\nicon = \"postgres\"\nfavicon_fetch = false\n\n[projects.\"blog\"]\ncolor = \"chart-1\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Projects["favicon_fetch"]; ok {
		t.Fatal("favicon_fetch decoded as a project")
	}
	if !cfg.FaviconFetch || cfg.FaviconFetchFor("shop") || !cfg.FaviconFetchFor("blog") || !cfg.FaviconFetchFor("other") {
		t.Fatalf("favicon fetch = %v shop=%v blog=%v", cfg.FaviconFetch, cfg.FaviconFetchFor("shop"), cfg.FaviconFetchFor("blog"))
	}
	if cfg.Projects["shop"].Icon != "postgres" {
		t.Fatalf("shop = %+v", cfg.Projects["shop"])
	}
	t.Setenv("ASTERISM_FAVICON_FETCH", "false")
	if cfg, _, _ = Load(path); cfg.FaviconFetch || cfg.FaviconFetchFor("blog") {
		t.Fatal("env override ignored")
	}

	bad := t.TempDir() + "/bad.toml"
	if err := os.WriteFile(bad, []byte("[projects.\"shop\"]\nicon = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(bad); err == nil {
		t.Fatal("non-string icon accepted")
	}
}

func TestEnsureDataDir(t *testing.T) {
	if Default().Server.DataDir != "data" {
		t.Fatal("default data_dir")
	}
	dir := t.TempDir() + "/state/nested"
	t.Setenv("ASTERISM_DATA_DIR", dir)
	cfg := Default()
	applyEnv(&cfg)
	got, err := cfg.EnsureDataDir()
	if err != nil || got != dir {
		t.Fatalf("EnsureDataDir = %q, %v", got, err)
	}
	st, err := os.Stat(got)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("stat = %v, %v", st, err)
	}
}
