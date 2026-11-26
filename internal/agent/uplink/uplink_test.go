package uplink

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"google.golang.org/protobuf/proto"
)

func TestHandshakeAndHeartbeat(t *testing.T) {
	hub := newFakeHub(t)
	srv := hub.serve(true)
	a := startAgent(t, testConfig(srv.URL), nil, func(o *Options) { o.HeartbeatEvery = 30 * time.Millisecond })
	hello := hub.next(isHello).GetHello()
	if hello.ProtocolVersion != ProtocolVersion || hello.SessionId != a.c.SessionID() || hello.HostName != "prod-1" ||
		hello.AgentVersion != "test" || len(hello.Capabilities) != 1 {
		t.Fatalf("hello %v", hello)
	}
	a.await("connected:bidi")
	hb := hub.next(isHeartbeat).GetHeartbeat()
	if hb.GetStats().GetTransport() != TransportBidi || hb.GetStats().GetGoroutines() == 0 {
		t.Fatalf("heartbeat stats %v", hb.GetStats())
	}
	hub.mu.Lock()
	tok := hub.token
	hub.mu.Unlock()
	if tok != "Bearer asta_test" {
		t.Fatalf("authorization header %q", tok)
	}
	// A ping is answered with a heartbeat.
	hub.toAgnt <- &v1.HubFrame{Payload: &v1.HubFrame_Ping{Ping: &v1.Heartbeat{TsMs: 1}}}
	hub.next(isHeartbeat)
}

func TestAckFreesBufferedFrames(t *testing.T) {
	hub := newFakeHub(t)
	hub.autoAck.Store(false)
	srv := hub.serve(true)
	a := startAgent(t, testConfig(srv.URL), nil, nil)
	a.await("connected:bidi")
	a.sample("cpu.util", 0.25, time.Now())
	f := hub.next(isRows)
	if f.GetSeq() == 0 {
		t.Fatal("metrics frames must be sequenced")
	}
	if a.c.Stats().GetBufferBytes() == 0 {
		t.Fatal("unacked frame should stay buffered")
	}
	hub.toAgnt <- &v1.HubFrame{Payload: &v1.HubFrame_Ack{Ack: &v1.Ack{UptoSeq: f.GetSeq()}}}
	a.await(fmt.Sprintf("acked:%d", f.GetSeq()))
	if b := a.c.Stats().GetBufferBytes(); b != 0 {
		t.Fatalf("ack should free the buffer, %d bytes left", b)
	}
}

func TestReconnectReplaysAndResyncs(t *testing.T) {
	hub := newFakeHub(t)
	hub.autoAck.Store(false)
	srv := hub.serve(true)
	a := startAgent(t, testConfig(srv.URL), nil, nil)
	a.store.UpsertContainer(&v1.Container{Name: "shop-api-1", ContainerId: "abcdefabcdef"})
	a.await("connected:bidi")
	if d := hub.next(isInventory); !isDelta(d) || d.GetDelta().GetBaseVersion() != 0 {
		t.Fatalf("fresh hub with no versions: want delta from 0, got %v", d)
	}
	a.sample("cpu.util", 0.5, time.Now())
	first := hub.next(isRows)

	// Drop the connection: the unacked frame is replayed with the same seq,
	// after the full dictionary, and no inventory is resent (hub is current).
	srv.CloseClientConnections()
	a.await("disconnected")
	a.await("connected:bidi")
	hub.next(isHello)
	var sawDict bool
	for {
		f := hub.next(func(f *v1.AgentFrame) bool { return !isHeartbeat(f) })
		if isInventory(f) {
			t.Fatalf("hub already holds the current inventory, got %v", f)
		}
		if m := f.GetMetrics(); m != nil && f.GetSeq() == 0 {
			if len(m.GetDict()) == 0 || len(m.GetRows()) != 0 {
				t.Fatalf("connection dict frame: %v", m)
			}
			sawDict = true
			continue
		}
		if f.GetSeq() == first.GetSeq() {
			if !sawDict {
				t.Fatal("replay must come after the full dict")
			}
			break
		}
	}

	// A change while connected travels as a delta.
	a.store.UpsertContainer(&v1.Container{Name: "shop-web-1", ContainerId: "123412341234"})
	if d := hub.next(isInventory); !isDelta(d) || len(d.GetDelta().GetUpsertContainers()) != 1 {
		t.Fatalf("want a one-container delta, got %v", d)
	}

	// Hub restart: it knows nothing, so the agent sends a full snapshot.
	hub.forgetOnConnect.Store(true)
	srv.CloseClientConnections()
	a.await("connected:bidi")
	snap := hub.next(isInventory)
	if !isSnapshot(snap) || len(snap.GetSnapshot().GetContainers()) != 2 {
		t.Fatalf("want a snapshot with 2 containers, got %v", snap)
	}
	hub.forgetOnConnect.Store(false)
	hub.mu.Lock()
	got := hub.inv.Snapshot()
	hub.mu.Unlock()
	if !proto.Equal(got, a.store.Snapshot()) {
		t.Fatalf("hub inventory diverged:\n%v\n%v", got, a.store.Snapshot())
	}

	// A hub-requested resync is answered with a snapshot.
	hub.toAgnt <- &v1.HubFrame{Payload: &v1.HubFrame_Resync{Resync: &v1.ResyncRequest{Reason: "test"}}}
	if f := hub.next(isInventory); !isSnapshot(f) {
		t.Fatalf("resync: want snapshot, got %v", f)
	}
}

func TestDropOldestWhileHubIsDown(t *testing.T) {
	hub := newFakeHub(t)
	hub.reject.Store(true)
	srv := hub.serve(true)
	cfg := testConfig(srv.URL)
	cfg.Hub.Transport = "bidi"
	cfg.Hub.Buffer.MaxBytes = 4096
	a := startAgent(t, cfg, nil, nil)
	ids := make([]state.SeriesID, 200)
	for i := range ids {
		ids[i] = a.sink.Series(fmt.Sprintf("ctr:c%d", i), "cpu.util")
	}
	now := time.Now()
	for round := 0; round < 30; round++ {
		for i, id := range ids {
			a.sink.Set(id, float32(round*1000+i))
		}
		a.sink.Commit(now.Add(time.Duration(round)*time.Second), true)
		if round%5 == 4 {
			a.await("dropped")
		}
	}
	st := a.c.Stats()
	if st.GetFramesDropped() == 0 || st.GetBufferBytes() > 4096 {
		t.Fatalf("ring not bounded: %v", st)
	}
	hub.reject.Store(false)
	a.await("connected:bidi")
	marker := now.Add(time.Hour)
	a.sample("marker", 1, marker)
	var seqs []uint64
	for done := false; !done; {
		f := hub.next(isRows)
		seqs = append(seqs, f.GetSeq())
		for _, r := range f.GetMetrics().GetRows() {
			done = done || r.GetTsMs() == marker.UnixMilli()
		}
	}
	if seqs[0] == 1 {
		t.Fatalf("oldest frame should have been dropped, replay started at %d", seqs[0])
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("replay not oldest-first: %v", seqs)
		}
	}
	hub.mu.Lock()
	unknown := hub.dec.Unknown
	hub.mu.Unlock()
	if unknown != 0 {
		t.Fatalf("hub could not decode %d replayed samples (dict missing?)", unknown)
	}
}

type recordingHandler struct {
	calls   chan *v1.CommandRequest
	release chan struct{}
}

func (r *recordingHandler) Handle(_ context.Context, req *v1.CommandRequest) *v1.CommandResult {
	r.calls <- req
	if r.release != nil {
		<-r.release
	}
	return &v1.CommandResult{RequestId: req.GetRequestId(), Ok: true}
}

func command(id string, deadline time.Time) *v1.HubFrame {
	return &v1.HubFrame{Payload: &v1.HubFrame_Command{Command: &v1.CommandRequest{
		RequestId: id, Action: v1.ContainerAction_CONTAINER_ACTION_RESTART, ContainerId: "ctr:a", DeadlineMs: deadline.UnixMilli(),
	}}}
}

func TestCommandDedupeAndDeadline(t *testing.T) {
	hub := newFakeHub(t)
	srv := hub.serve(true)
	h := &recordingHandler{calls: make(chan *v1.CommandRequest, 128)}
	a := startAgent(t, testConfig(srv.URL), h, nil)
	a.await("connected:bidi")
	soon := time.Now().Add(time.Minute)
	hub.toAgnt <- command("r1", soon)
	if r := hub.next(isResult).GetCommandResult(); r.GetRequestId() != "r1" || !r.GetOk() {
		t.Fatalf("r1: %v", r)
	}
	hub.toAgnt <- command("r1", soon)
	if r := hub.next(isResult).GetCommandResult(); r.GetCode() != "already_exists" {
		t.Fatalf("duplicate r1: %v", r)
	}
	hub.toAgnt <- command("r2", time.Now().Add(-time.Second))
	if r := hub.next(isResult).GetCommandResult(); r.GetRequestId() != "r2" || r.GetCode() != "deadline_exceeded" {
		t.Fatalf("expired r2: %v", r)
	}
	if n := len(h.calls); n != 1 {
		t.Fatalf("handler ran %d times, want 1", n)
	}
	// The dedupe window remembers only the last 64 ids.
	for i := range DedupeWindow {
		hub.toAgnt <- command(fmt.Sprintf("x%d", i), soon)
		hub.next(isResult)
	}
	hub.toAgnt <- command("r1", soon)
	if r := hub.next(isResult).GetCommandResult(); !r.GetOk() {
		t.Fatalf("r1 should have aged out of the window: %v", r)
	}
}

func TestCommandResultNeverReplayed(t *testing.T) {
	hub := newFakeHub(t)
	srv := hub.serve(true)
	h := &recordingHandler{calls: make(chan *v1.CommandRequest, 8), release: make(chan struct{})}
	a := startAgent(t, testConfig(srv.URL), h, nil)
	a.await("connected:bidi")
	hub.toAgnt <- command("slow", time.Now().Add(time.Minute))
	<-h.calls // running
	srv.CloseClientConnections()
	a.await("connected:bidi")
	close(h.release) // finishes after the connection it arrived on is gone
	hub.toAgnt <- command("fresh", time.Now().Add(time.Minute))
	r := hub.next(isResult).GetCommandResult()
	if r.GetRequestId() != "fresh" {
		t.Fatalf("result of a command from a dropped connection was sent: %v", r)
	}
}

func TestFallbackToUnaryOnUnimplemented(t *testing.T) {
	hub := newFakeHub(t)
	hub.bidiUnimplemented.Store(true)
	srv := hub.serve(true)
	h := &recordingHandler{calls: make(chan *v1.CommandRequest, 8)}
	a := startAgent(t, testConfig(srv.URL), h, nil)
	a.await("connected:unary")
	a.sample("cpu.util", 1, time.Now())
	f := hub.next(isRows)
	a.await(fmt.Sprintf("acked:%d", f.GetSeq())) // Push response is the ack
	hub.toAgnt <- command("u1", time.Now().Add(time.Minute))
	if r := hub.next(isResult).GetCommandResult(); r.GetRequestId() != "u1" || !r.GetOk() {
		t.Fatalf("unary command: %v", r)
	}
	streams := hub.streams.Load()
	srv.CloseClientConnections()
	a.await("connected:unary")
	if hub.streams.Load() != streams {
		t.Fatal("after a successful fallback the agent must not retry bidi")
	}
}

func TestFallbackToUnaryOnHTTP1OnlyPath(t *testing.T) {
	hub := newFakeHub(t)
	srv := hub.serve(false) // no h2c: HTTP/1.1 only
	a := startAgent(t, testConfig(srv.URL), nil, nil)
	a.await("connected:unary")
	if a.c.Stats().GetTransport() != TransportUnary {
		t.Fatal("transport stat")
	}
}

func TestBidiOnlyDoesNotFallBack(t *testing.T) {
	hub := newFakeHub(t)
	hub.bidiUnimplemented.Store(true)
	srv := hub.serve(true)
	cfg := testConfig(srv.URL)
	cfg.Hub.Transport = "bidi"
	a := startAgent(t, cfg, nil, nil)
	a.await("error:")
	a.await("error:")
	if hub.pushes.Load() != 0 {
		t.Fatal("transport bidi must never use Push")
	}
}

func TestHintsReachScheduler(t *testing.T) {
	hub := newFakeHub(t)
	srv := hub.serve(true)
	got := make(chan state.Hints, 4)
	a := startAgent(t, testConfig(srv.URL), nil, func(o *Options) { o.OnHints = func(h state.Hints) { got <- h } })
	a.await("connected:bidi")
	hub.toAgnt <- &v1.HubFrame{Payload: &v1.HubFrame_Hints{Hints: &v1.AgentHints{LiveViewers: true, FocusEntityIds: []string{"ctr:a"}}}}
	select {
	case h := <-got:
		if !h.LiveViewers || h.Focus[0] != "ctr:a" {
			t.Fatalf("hints %+v", h)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hints not delivered")
	}
}

func TestLogRequestWithoutHandler(t *testing.T) {
	hub := newFakeHub(t)
	srv := hub.serve(true)
	a := startAgent(t, testConfig(srv.URL), nil, nil)
	a.await("connected:bidi")
	hub.toAgnt <- &v1.HubFrame{Payload: &v1.HubFrame_LogRequest{LogRequest: &v1.LogRequest{RequestId: "l1", WorkloadId: "ctr:a"}}}
	ch := hub.next(func(f *v1.AgentFrame) bool { return f.GetLogChunk() != nil }).GetLogChunk()
	if ch.GetRequestId() != "l1" || !ch.GetEof() || ch.GetError() == "" {
		t.Fatalf("log chunk %v", ch)
	}
}

func TestTLSWithCAAndPin(t *testing.T) {
	hub := newFakeHub(t)
	path, handler := asterismv1connect.NewAgentServiceHandler(hub)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	good := SPKIPin(srv.Certificate())

	cfg := testConfig(srv.URL)
	cfg.Hub.TLS = config.TLS{CAFile: caFile, PinSHA256: []string{good}}
	a := startAgent(t, cfg, nil, nil)
	a.await("connected:bidi")
	a.stop()

	cfg = testConfig(srv.URL)
	cfg.Hub.TLS = config.TLS{CAFile: caFile, PinSHA256: []string{"/HGJZsEB2BqPxAXFaS5wieyk9zmV2qq7Yn2opHrncf8="}}
	b := startAgent(t, cfg, nil, nil)
	if ev := b.await("error:"); !strings.Contains(ev, "pin_sha256") || !strings.Contains(ev, good) {
		t.Fatalf("want an actionable pin error naming the served key, got %q", ev)
	}

	// Without the CA the self-signed hub is refused.
	cfg = testConfig(srv.URL)
	c := startAgent(t, cfg, nil, nil)
	if ev := c.await("error:"); !strings.Contains(ev, "certificate") {
		t.Fatalf("want a certificate error, got %q", ev)
	}
}

func TestTLSConfigErrors(t *testing.T) {
	if _, err := TLSConfig(config.TLS{CAFile: "/nonexistent/ca.pem"}); err == nil {
		t.Error("missing ca_file")
	}
	if _, err := TLSConfig(config.TLS{ClientCert: "/nonexistent/c.pem", ClientKey: "/nonexistent/k.pem"}); err == nil {
		t.Error("missing client cert")
	}
	if _, err := TLSConfig(config.TLS{PinSHA256: []string{"nope"}}); err == nil {
		t.Error("bad pin")
	}
	cfg, err := TLSConfig(config.TLS{ServerName: "hub.internal"})
	if err != nil || cfg.ServerName != "hub.internal" || cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("server name: %v %v", cfg, err)
	}
}

func TestPlainHTTPRefusedForRemoteHub(t *testing.T) {
	cfg := testConfig("http://hub.example.com")
	if _, err := New(Options{Config: cfg, Token: "asta_x", Store: state.NewStore(state.Options{})}); err == nil {
		t.Fatal("plain http to a remote hub must be refused")
	}
	cfg.Hub.TLS.InsecureDev = true
	if _, err := New(Options{Config: cfg, Token: "asta_x", Store: state.NewStore(state.Options{})}); err != nil {
		t.Fatalf("insecure_dev: %v", err)
	}
}

func TestUnreachableHubIsNoFallbackReason(t *testing.T) {
	ctx := context.Background()
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	for name, err := range map[string]error{
		"refused": connect.NewError(connect.CodeUnavailable, fmt.Errorf("post: %w", refused)),
		"dns":     connect.NewError(connect.CodeUnavailable, &net.DNSError{Err: "no such host", Name: "hub"}),
	} {
		if fallbackCandidate(ctx, err) {
			t.Errorf("%s: an unreachable hub must not trigger the unary fallback", name)
		}
	}
	if !fallbackCandidate(ctx, connect.NewError(connect.CodeUnimplemented, errors.New("404"))) {
		t.Error("Unimplemented must trigger the unary fallback")
	}
}
