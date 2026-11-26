package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	tok := filepath.Join(dir, "agent.token")
	if err := os.WriteFile(tok, []byte("asta_cmdtest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "agent.yaml")
	body = strings.ReplaceAll(body, "TOKEN_FILE", tok)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVersionAndUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runMain([]string{"version"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "asterism-agent ") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	if code := runMain([]string{"bogus"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("unknown: %d %q", code, errb.String())
	}
}

func TestValidateAndPrintConfig(t *testing.T) {
	good := writeConfig(t, "hub: {url: \"http://127.0.0.1:7700\", token_file: TOKEN_FILE}\nhost: {root: /}\n")
	var out, errb bytes.Buffer
	if code := runMain([]string{"validate", "--config", good}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "ok: ") {
		t.Fatalf("validate good: %d\n%s\n%s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := runMain([]string{"print-config", "--config", good}, &out, &errb); code != 0 {
		t.Fatalf("print-config: %d %s", code, errb.String())
	}
	if strings.Contains(out.String(), "asta_cmdtest") || !strings.Contains(out.String(), "config_hash: sha256:") {
		t.Fatalf("print-config leaked or missing hash:\n%s", out.String())
	}
	bad := writeConfig(t, "hub: {url: \"ftp://x\", tokn: x}\n")
	errb.Reset()
	if code := runMain([]string{"validate", "--config", bad}, &out, &errb); code != 1 || !strings.Contains(errb.String(), `did you mean "token"`) {
		t.Fatalf("validate bad: %d\n%s", code, errb.String())
	}
	errb.Reset()
	if code := runMain([]string{"validate", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, &out, &errb); code != 1 {
		t.Fatalf("explicit missing config should fail: %d", code)
	}
}

func TestApplyRuntimeEnvWins(t *testing.T) {
	a := config.Default().Agent
	env := func(k string) (string, bool) {
		if k == "GOMAXPROCS" {
			return "3", true
		}
		return "", false
	}
	before := applyRuntime(config.Agent{}, func(string) (string, bool) { return "", false })
	tun := applyRuntime(a, env)
	if tun.memoryLimit != int64(a.MemoryLimit) || tun.gcPercent != a.GCPercent {
		t.Fatalf("tuning not applied: %+v", tun)
	}
	if tun.gomaxprocs != before.gomaxprocs {
		t.Fatalf("GOMAXPROCS env set: YAML must not override (%d -> %d)", before.gomaxprocs, tun.gomaxprocs)
	}
	applyRuntime(config.Agent{GCPercent: 100, MemoryLimit: config.ByteSize(1<<62 - 1)}, env)
}

// miniHub answers the handshake, pings once, and reports what it saw.
type miniHub struct {
	asterismv1connect.UnimplementedAgentServiceHandler
	hello     chan *v1.AgentHello
	heartbeat chan *v1.AgentHeartbeat
}

func (h *miniHub) Stream(ctx context.Context, s *connect.BidiStream[v1.AgentFrame, v1.HubFrame]) error {
	f, err := s.Receive()
	if err != nil {
		return err
	}
	if f.GetHello() == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("want hello"))
	}
	h.hello <- f.GetHello()
	if err := s.Send(&v1.HubFrame{Payload: &v1.HubFrame_HelloAck{HelloAck: &v1.HelloAck{HostId: "h", ServerTimeMs: time.Now().UnixMilli()}}}); err != nil {
		return err
	}
	if err := s.Send(&v1.HubFrame{Payload: &v1.HubFrame_Ping{Ping: &v1.Heartbeat{TsMs: 1}}}); err != nil {
		return err
	}
	for {
		f, err := s.Receive()
		if err != nil {
			return nil
		}
		if hb := f.GetHeartbeat(); hb != nil {
			select {
			case h.heartbeat <- hb:
			default:
			}
		}
	}
}

// TestRunWithoutCollectors: with no collectors registered the agent still
// connects over h2c, handshakes and heartbeats, then shuts down cleanly.
func TestRunWithoutCollectors(t *testing.T) {
	hub := &miniHub{hello: make(chan *v1.AgentHello, 1), heartbeat: make(chan *v1.AgentHeartbeat, 1)}
	mux := http.NewServeMux()
	mux.Handle(asterismv1connect.NewAgentServiceHandler(hub))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()

	p := writeConfig(t, "hub: {url: \""+srv.URL+"\", token_file: TOKEN_FILE}\nhost: {name: test-1, labels: {env: test}}\n"+
		"actions: {enabled: true, allow: [restart], containers: [\"shop-*\"]}\n")
	cfg, err := config.LoadFile(p, false, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	tok, err := cfg.ResolveToken()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := newAgent(ctx, cfg, tok, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.run(ctx) }()
	select {
	case h := <-hub.hello:
		if h.HostName != "test-1" || h.Labels["env"] != "test" || !strings.HasPrefix(h.ConfigHash, "sha256:") ||
			h.ProtocolVersion != 1 || len(h.AllowedActions) != 1 || !contains(h.Capabilities, "action:restart") {
			t.Fatalf("hello %v", h)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no hello")
	}
	select {
	case hb := <-hub.heartbeat:
		if hb.GetStats().GetTransport() != "bidi" {
			t.Fatalf("heartbeat %v", hb)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no heartbeat")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("agent did not shut down")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
