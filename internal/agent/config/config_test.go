package config

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func envMap(m map[string]string) LookupEnv {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestExamplesParseAndValidate(t *testing.T) {
	files, err := filepath.Glob("testdata/examples/*.yaml")
	if err != nil || len(files) != 3 {
		t.Fatalf("want 3 examples, got %d (%v)", len(files), err)
	}
	env := envMap(map[string]string{EnvToken: "asta_exampletoken"})
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			cfg, err := LoadFile(f, false, env)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if errs := cfg.Validate().Errors(); len(errs) > 0 {
				t.Fatalf("validate: %v", errs)
			}
			if cfg.Intervals.Host.D() != 2*time.Second || cfg.Hub.Buffer.MaxBytes != 1<<20 {
				t.Fatalf("defaults lost: host=%s buffer=%s", cfg.Intervals.Host, cfg.Hub.Buffer.MaxBytes)
			}
		})
	}
}

func TestExampleValues(t *testing.T) {
	env := envMap(map[string]string{EnvToken: "asta_x"})
	a, err := LoadFile("testdata/examples/a-compose.yaml", false, env)
	if err != nil {
		t.Fatal(err)
	}
	if a.Host.Name != "prod-1" || a.Host.Labels["provider"] != "hetzner" {
		t.Fatalf("host: %+v", a.Host)
	}
	if !a.Sources.Systemd.Enabled.Off() || !a.Sources.Proxies.Nginx.Enabled.Off() || a.Sources.Proxies.HAProxy.Enabled != TriAuto {
		t.Fatalf("tri-states: %+v", a.Sources)
	}
	if !a.Actions.Enabled || !reflect.DeepEqual(a.Actions.Allow, []string{"restart"}) || a.Actions.Protect[0] != "*postgres*" {
		t.Fatalf("actions: %+v", a.Actions)
	}
	if len(a.Projects) != 2 || a.Projects[1].Members.Containers[2] != "loki*" {
		t.Fatalf("projects: %+v", a.Projects)
	}
	b, err := LoadFile("testdata/examples/b-baremetal.yaml", false, env)
	if err != nil {
		t.Fatal(err)
	}
	if b.Sources.Processes.Match[0].Cmdline != "*billing/server.js*" || b.Sources.Logs.Files[0].Workload != "proc:billing" {
		t.Fatalf("processes/logs: %+v", b.Sources)
	}
	c, err := LoadFile("testdata/examples/c-mixed.yaml", false, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hub.TLS.PinSHA256) != 1 || c.Sources.Routes[0].Upstream != "http://127.0.0.1:3001" {
		t.Fatalf("tls/routes: %+v %+v", c.Hub.TLS, c.Sources.Routes)
	}
}

func TestInvalidGolden(t *testing.T) {
	files, err := filepath.Glob("testdata/invalid/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no invalid fixtures: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			cfg, err := LoadFile(f, false, envMap(nil))
			var got Issues
			if err != nil {
				is, ok := err.(Issues)
				if !ok {
					t.Fatalf("want Issues, got %T: %v", err, err)
				}
				got = is
			} else {
				got = cfg.Validate()
			}
			if !got.HasErrors() {
				t.Fatalf("want errors, got none")
			}
			var b strings.Builder
			for _, i := range got {
				b.WriteString(i.String())
				b.WriteByte('\n')
			}
			golden := strings.TrimSuffix(f, ".yaml") + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with -update): %v", err)
			}
			if b.String() != string(want) {
				t.Fatalf("issues mismatch\n--- got\n%s--- want\n%s", b.String(), want)
			}
		})
	}
}

func TestEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokFile, []byte("asta_fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yml := []byte(`hub: {url: https://yaml.example.com, token: asta_yaml}
host: {name: yaml-host, root: /}
sources: {docker: {endpoint: "unix:///yaml.sock"}}
actions: {enabled: true, allow: [restart], containers: ["*"]}
agent: {log_level: info}
`)
	cfg, err := Parse(yml, "agent.yaml", envMap(map[string]string{
		EnvHubURL:         "https://env.example.com",
		EnvHostName:       "env-host",
		EnvHostRoot:       "/host",
		EnvDockerEndpoint: "tcp://socket-proxy:2375",
		EnvLogLevel:       "debug",
		EnvAllowActions:   "false",
		EnvTokenFile:      tokFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub.URL != "https://env.example.com" || cfg.Host.Name != "env-host" || cfg.Host.Root != "/host" ||
		cfg.Sources.Docker.Endpoint != "tcp://socket-proxy:2375" || cfg.Agent.LogLevel != "debug" || cfg.Actions.Enabled {
		t.Fatalf("env overrides not applied: %+v", cfg)
	}
	if tok, err := cfg.ResolveToken(); err != nil || tok != "asta_fromfile" {
		t.Fatalf("token file env should beat yaml token: %q %v", tok, err)
	}
	cfg, err = Parse(yml, "agent.yaml", envMap(map[string]string{EnvToken: "asta_env", EnvTokenFile: tokFile, EnvAllowActions: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if tok, _ := cfg.ResolveToken(); tok != "asta_env" || cfg.TokenSource != "env:"+EnvToken {
		t.Fatalf("env token should win: %q %s", tok, cfg.TokenSource)
	}
	if !cfg.Actions.Enabled {
		t.Fatal("ALLOW_ACTIONS=true must not change the YAML value")
	}
	if w := cfg.Validate().Warnings(); len(w) == 0 || !strings.Contains(w[0].String(), "can only disable") {
		t.Fatalf("want ALLOW_ACTIONS warning, got %v", w)
	}
}

func TestExpandOnlyInHub(t *testing.T) {
	yml := []byte("hub: {url: \"https://${HUB_HOST}\", token: \"${TOK}\"}\nhost: {name: \"${NOT_EXPANDED}\"}\n")
	_, err := Parse(yml, "agent.yaml", envMap(map[string]string{"HUB_HOST": "hub.example.com", "TOK": "asta_t"}))
	// host.name "${NOT_EXPANDED}" is kept literally and is then an invalid name.
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := Parse(yml, "agent.yaml", envMap(map[string]string{"HUB_HOST": "hub.example.com", "TOK": "asta_t"}))
	if cfg.Hub.URL != "https://hub.example.com" || cfg.Hub.Token != "asta_t" || cfg.Host.Name != "${NOT_EXPANDED}" {
		t.Fatalf("expansion: %+v %+v", cfg.Hub, cfg.Host)
	}
}

func TestMissingOptionalFileUsesDefaults(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "absent.yaml"), true, envMap(map[string]string{EnvHubURL: "https://h.example.com", EnvToken: "asta_x"}))
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Validate().Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, err := LoadFile(filepath.Join(t.TempDir(), "absent.yaml"), false, nil); err == nil {
		t.Fatal("explicit missing file must fail")
	}
}

func TestPrintConfigRedactsAndRoundTrips(t *testing.T) {
	cfg, err := Parse([]byte("hub: {url: https://h.example.com, token: asta_supersecret}\n"), "agent.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.PrintConfig()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "supersecret") || !strings.Contains(s, RedactedToken) || !strings.Contains(s, "# config_hash: sha256:") {
		t.Fatalf("print-config output:\n%s", s)
	}
	// The printed YAML parses back to the same effective configuration.
	back, err := Parse(out, "printed.yaml", nil)
	if err != nil {
		t.Fatalf("re-parse printed config: %v\n%s", err, s)
	}
	back.Hub.Token = cfg.Hub.Token
	if back.Hash() != cfg.Hash() {
		t.Fatalf("hash changed after round trip: %s vs %s", back.Hash(), cfg.Hash())
	}
	// The hash ignores the token.
	other := *cfg
	other.Hub.Token = "asta_different"
	if other.Hash() != cfg.Hash() {
		t.Fatal("hash must not depend on the token")
	}
}

func TestDefaultsValidateWithToken(t *testing.T) {
	cfg := Default()
	cfg.Hub.URL = "http://127.0.0.1:7700"
	cfg.Hub.Token = "asta_x"
	if errs := cfg.Validate().Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestScalars(t *testing.T) {
	for in, want := range map[string]time.Duration{"2s": 2 * time.Second, "0": 0, "5m": 5 * time.Minute, "1.5": 1500 * time.Millisecond} {
		got, err := ParseDuration(in)
		if err != nil || got.D() != want {
			t.Errorf("ParseDuration(%q) = %v, %v", in, got, err)
		}
	}
	for d, want := range map[time.Duration]string{10 * time.Second: "10s", 5 * time.Minute: "5m", time.Hour: "1h", 90 * time.Second: "1m30s", 0: "0"} {
		if got := Duration(d).String(); got != want {
			t.Errorf("Duration(%v).String() = %q, want %q", d, got, want)
		}
	}
	for in, want := range map[string]ByteSize{"1MiB": 1 << 20, "64KiB": 64 << 10, "12MiB": 12 << 20, "1000": 1000, "1MB": 1e6} {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := DecodePin("sha256//HGJZsEB2BqPxAXFaS5wieyk9zmV2qq7Yn2opHrncf8="); err != nil {
		t.Errorf("pin with sha256/ prefix: %v", err)
	}
}
