package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/api"
	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/showcase"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

type fakeHost struct{}

func (fakeHost) HostSnapshot() *asterismv1.HostInfo {
	return &asterismv1.HostInfo{Id: "host", Hostname: "vps-test", Os: "ubuntu 24.04", Cores: 4,
		MemTotalBytes: 8 << 30, MemUsedBytes: 6 << 30, CpuUtil: 0.25, UptimeSeconds: 3 * 86400}
}

// newHub serves the real API handlers over the showcase collector behind
// token auth, and returns the URL and a read-scoped token.
func newHub(t *testing.T) (string, string) {
	t.Helper()
	tok, digest, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := auth.NewManager(auth.Options{
		AdminEmail: "a@example.com", AdminPassword: "pw", SessionScopes: []auth.Scope{auth.ScopeRead},
		Tokens: []auth.APIToken{{Name: "cli", SHA256: digest, Scopes: []auth.Scope{auth.ScopeRead}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(64)
	r := ring.New(900)
	sc := showcase.New(b, r)
	svc := service.New(b, r, fakeHost{}, sc, "showcase")
	svc.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.ConfigureDocker(sc, sc, service.ActionPolicy{DefaultTimeout: time.Second})
	mux := http.NewServeMux()
	api.New(svc).Register(mux, mgr.RequireSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, tok
}

func run(ctx context.Context, args ...string) (string, error) {
	root := &cobra.Command{Use: "asterism", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(Commands()...)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

func TestCommandsAgainstHub(t *testing.T) {
	t.Setenv("ASTERISM_TOKEN", "")
	url, tok := newHub(t)
	conn := []string{"--server", url, "--token", tok}
	cases := []struct {
		args     []string
		contains []string
	}{
		{[]string{"status"}, []string{"vps-test", "CATEGORY", "PROJECT"}},
		{[]string{"ps", "--project", "nova-core"}, []string{"ctr:nova-api", "ctr:postgres-primary"}},
		{[]string{"ports", "--exposure", "public"}, []string{"public", "sshd"}},
		{[]string{"ingress"}, []string{"PROXY", "├─▶", "routes"}},
		{[]string{"topo", "--around", "proj:nova-core"}, []string{"nova-api", "└─▶"}},
		{[]string{"events", "--since", "24h", "--severity", "warning", "-n", "5"}, []string{"SEVERITY", "of"}},
		{[]string{"logs", "nova-api", "--tail", "3"}, []string{"nova-api"}},
		{[]string{"top", "--by", "mem"}, []string{"MEM▼", "q quit"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			out, err := run(t.Context(), append(tc.args, conn...)...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "\x1b") {
				t.Error("non-terminal output must be plain")
			}
			for _, want := range tc.contains {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			jsonOut, err := run(t.Context(), append(append(tc.args, "--json"), conn...)...)
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(jsonOut), &v); err != nil {
				t.Fatalf("--json is not one JSON object: %v\n%s", err, jsonOut)
			}
		})
	}
}

func TestLogsTailCount(t *testing.T) {
	url, tok := newHub(t)
	out, err := run(t.Context(), "logs", "ctr:nova-api", "-n", "4", "--server", url, "--token", tok)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "\n"); n != 4 {
		t.Errorf("got %d lines, want 4:\n%s", n, out)
	}
}

func TestLogsFollowStopsCleanly(t *testing.T) {
	old := followInterval
	followInterval = 5 * time.Millisecond
	t.Cleanup(func() { followInterval = old })
	url, tok := newHub(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out, err := run(ctx, "logs", "ctr:nova-api", "-f", "-n", "2", "--json", "--server", url, "--token", tok)
	if err != nil {
		t.Fatalf("follow should end cleanly on cancel: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("want at least the 2 initial lines, got %q", out)
	}
	for _, l := range lines {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil || v["text"] == nil {
			t.Errorf("not an NDJSON LogLine: %q", l)
		}
	}
}

func TestActionableErrors(t *testing.T) {
	t.Setenv("ASTERISM_TOKEN", "")
	url, tok := newHub(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no token", []string{"status", "--server", url, "--token-file", "/nonexistent/asterism.token"}, "read token file"},
		{"unauthenticated", []string{"status", "--server", url, "--token", "asterism_wrong"}, "asterism token generate"},
		{"unreachable", []string{"status", "--server", "http://127.0.0.1:1", "--token", tok}, "daemon not reachable at http://127.0.0.1:1"},
		{"unknown workload", []string{"logs", "ctr:nope", "--server", url, "--token", tok}, "asterism ps"},
		{"ambiguous workload", []string{"logs", "nova", "--server", url, "--token", tok}, "pick one"},
		{"bad exposure", []string{"ports", "--exposure", "world", "--server", url}, "loopback"},
		{"bad around", []string{"topo", "--around", "ctr:nope", "--server", url, "--token", tok}, "proj:<name>"},
		{"bad sort", []string{"top", "--by", "disk", "--server", url}, "cpu or mem"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(t.Context(), tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestResolveServerURL(t *testing.T) {
	t.Setenv("ASTERISM_URL", "")
	t.Setenv("ASTERISM_LISTEN", "0.0.0.0:7732")
	if got, err := ResolveServerURL("", "", "ASTERISM_URL"); err != nil || got != "http://127.0.0.1:7732" {
		t.Errorf("from listen: %q %v", got, err)
	}
	t.Setenv("ASTERISM_URL", "http://env:1")
	if got, _ := ResolveServerURL("", "", "ASTERISM_URL"); got != "http://env:1" {
		t.Errorf("env: %q", got)
	}
	if got, _ := ResolveServerURL("http://flag:2", "", "ASTERISM_URL"); got != "http://flag:2" {
		t.Errorf("flag: %q", got)
	}
}
