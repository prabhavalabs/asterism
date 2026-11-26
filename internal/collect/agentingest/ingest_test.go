package agentingest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestHandshakeAndKnownInventoryVersion(t *testing.T) {
	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "sess-a")
	if s.ack.GetHostId() != "prod-1" || s.ack.GetKnownInventoryVersion() != 0 || s.ack.GetHubVersion() != "test" || s.ack.GetServerTimeMs() != h.clock.Now().UnixMilli() {
		t.Fatalf("hello ack %v", s.ack)
	}
	s.send(snapshotFrame(3, shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)))
	s.sync()
	if got := h.reg.ListAgents().GetAgents()[0]; got.GetState() != "connected" || got.GetInventoryVersion() != 3 || got.GetTransport() != TransportBidi ||
		got.GetAgentVersion() != "0.2.0-test" || got.GetRemoteAddr() != "203.0.113.7:51000" || got.GetConfigHash() != "cafe" {
		t.Fatalf("status %v", got)
	}
	if err := s.hangup(); err != nil {
		t.Fatalf("clean hangup returned %v", err)
	}

	// Same agent process reconnects: the hub still holds version 3.
	s2 := h.mustDial("prod-1", "sess-a")
	if s2.ack.GetKnownInventoryVersion() != 3 {
		t.Fatalf("same session: known = %d, want 3", s2.ack.GetKnownInventoryVersion())
	}
	_ = s2.hangup()

	// A restarted agent process starts over.
	s3 := h.mustDial("prod-1", "sess-b")
	if s3.ack.GetKnownInventoryVersion() != 0 {
		t.Fatalf("new session: known = %d, want 0", s3.ack.GetKnownInventoryVersion())
	}
	if got := h.reg.ListAgents().GetAgents()[0].GetInventoryVersion(); got != 0 {
		t.Fatalf("new session keeps old inventory version %d", got)
	}
}

func TestTokenHostBinding(t *testing.T) {
	h := newTestHub(t, nil, Agent{Name: "prod-1"}, Agent{Name: "edge-2", MachineID: "AbC123"})
	tests := []struct {
		name  string
		host  string
		hello *asterismv1.AgentHello
		code  connect.Code
	}{
		{"agent reports another host", "prod-1", hello("prod-2", "s1"), connect.CodePermissionDenied},
		{"token host not enrolled", "ghost", hello("ghost", "s1"), connect.CodePermissionDenied},
		{"machine pin mismatch", "edge-2", func() *asterismv1.AgentHello { x := hello("edge-2", "s1"); x.MachineIdHash = "other"; return x }(), connect.CodePermissionDenied},
		{"old protocol", "prod-1", func() *asterismv1.AgentHello { x := hello("prod-1", "s1"); x.ProtocolVersion = 9; return x }(), connect.CodeFailedPrecondition},
		{"no session id", "prod-1", hello("prod-1", ""), connect.CodeInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.dial(tc.host, tc.hello, h.clock.Now())
			if codeOf(err) != tc.code {
				t.Fatalf("err = %v, want %v", err, tc.code)
			}
		})
	}
	// The pin matches case-insensitively; an empty host name is accepted.
	ok := hello("", "s9")
	ok.MachineIdHash = "abc123"
	if _, err := h.dial("edge-2", ok, h.clock.Now()); err != nil {
		t.Fatalf("pinned machine refused: %v", err)
	}
	for _, a := range h.reg.ListAgents().GetAgents() {
		if a.GetName() == "prod-1" && a.GetState() != "never" {
			t.Fatalf("refused handshakes must not mark prod-1 seen: %v", a)
		}
	}
}

func TestSeqDedupeAndAck(t *testing.T) {
	h := newTestHub(t, func(o *Options) { o.AckFrames = 4; o.AckEvery = 20 * time.Millisecond })
	drain := h.collectBus()
	s := h.mustDial("prod-1", "sess-a")
	dict := map[uint32][2]string{1: {"host", "cpu.util"}}
	ts := h.clock.Now().UnixMilli()
	for i := 1; i <= 4; i++ {
		s.send(s.seqFrame(metricsFrame(ts+int64(i), dict, map[uint32]float32{1: float32(i) / 10})))
	}
	if a := s.next(isAck).GetAck().GetUptoSeq(); a != 4 {
		t.Fatalf("ack after 4 frames = %d", a)
	}
	// A replay of seq 2 and 3 (e.g. after a reconnect) is dropped.
	for _, seq := range []uint64{2, 3} {
		f := metricsFrame(ts+100, dict, map[uint32]float32{1: 0.99})
		f.Seq = seq
		s.send(f)
	}
	s.send(s.seqFrame(metricsFrame(ts+5, dict, map[uint32]float32{1: 0.5})))
	// The periodic ack covers frames below the AckFrames threshold.
	s.next(func(f *asterismv1.HubFrame) bool { return f.GetAck().GetUptoSeq() == 5 })
	var vals []float64
	for _, sm := range drain().Samples {
		if sm.EntityID == "host" && sm.Metric == "cpu.util" {
			vals = append(vals, sm.Value)
		}
	}
	if len(vals) != 5 {
		t.Fatalf("host cpu samples %v (duplicates must be dropped)", vals)
	}
	for _, v := range vals {
		if v > 0.9 {
			t.Fatalf("replayed duplicate was applied: %v", vals)
		}
	}
	hs := h.reg.hosts["prod-1"]
	hs.mu.Lock()
	dups := hs.duplicates
	hs.mu.Unlock()
	if dups != 2 {
		t.Fatalf("duplicates = %d", dups)
	}
}

func TestDeltaVersionMismatchRequestsResync(t *testing.T) {
	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "sess-a")
	s.send(snapshotFrame(2, shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)))
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_Delta{Delta: &asterismv1.InventoryDelta{BaseVersion: 7, Version: 8}}})
	r := s.next(isResync)
	if !strings.Contains(r.GetResync().GetReason(), "mismatch") {
		t.Fatalf("resync reason %q", r.GetResync().GetReason())
	}
	// A matching delta applies.
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_Delta{Delta: &asterismv1.InventoryDelta{
		BaseVersion: 2, Version: 3,
		UpsertContainers: []*asterismv1.Container{shopContainer("shop-web-1", "dddddddddddd", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)},
	}}})
	s.sync()
	snap := h.reg.Primary().DockerSnapshot()
	if len(snap.Containers) != 2 {
		t.Fatalf("containers after delta: %d", len(snap.Containers))
	}
	if v := h.reg.ListAgents().GetAgents()[0].GetInventoryVersion(); v != 3 {
		t.Fatalf("inventory version %d", v)
	}
}

func TestStaleAndOfflineTransitions(t *testing.T) {
	h := newTestHub(t, func(o *Options) { o.StaleAfter = 30 * time.Second })
	drain := h.collectBus()
	state := func() string { return h.reg.ListAgents().GetAgents()[0].GetState() }
	if state() != "never" {
		t.Fatalf("initial state %q", state())
	}
	s := h.mustDial("prod-1", "sess-a")
	if state() != "connected" {
		t.Fatalf("state %q", state())
	}
	kinds := func() []string {
		var out []string
		for _, e := range drain().Events {
			out = append(out, e.GetKind())
		}
		return out
	}
	if k := kinds(); len(k) != 1 || k[0] != "agent.online" {
		t.Fatalf("events after first connect %v", k)
	}

	// A disconnect inside the grace window is "stale", without an event.
	_ = s.hangup()
	h.clock.Advance(10 * time.Second)
	h.reg.Sweep()
	if state() != "stale" || len(kinds()) != 0 {
		t.Fatalf("state %q", state())
	}
	// Reconnecting within the window is silent too.
	s = h.mustDial("prod-1", "sess-a")
	if k := kinds(); len(k) != 0 {
		t.Fatalf("flap raised %v", k)
	}

	// A connection that goes quiet for stale_after is closed and offline.
	h.clock.Advance(31 * time.Second)
	h.reg.Sweep()
	select {
	case err := <-s.done:
		if codeOf(err) != connect.CodeAborted {
			t.Fatalf("hung stream ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silent connection was not closed")
	}
	if state() != "offline" {
		t.Fatalf("state %q", state())
	}
	if k := kinds(); len(k) != 1 || k[0] != "agent.offline" {
		t.Fatalf("events %v", k)
	}
	h.reg.Sweep()
	if k := kinds(); len(k) != 0 {
		t.Fatalf("offline must be raised once, got %v", k)
	}
	if reason := h.reg.Primary().ActionsUnavailableReason(); !strings.Contains(reason, "offline") {
		t.Fatalf("actions reason %q", reason)
	}

	h.mustDial("prod-1", "sess-a")
	if k := kinds(); len(k) != 1 || k[0] != "agent.online" {
		t.Fatalf("events after recovery %v", k)
	}
	var lifecycle []string
	for _, e := range h.reg.Primary().Events() {
		if strings.HasPrefix(e.GetKind(), "agent.o") {
			lifecycle = append(lifecycle, e.GetKind())
		}
	}
	if strings.Join(lifecycle, ",") != "agent.online,agent.offline,agent.online" {
		t.Fatalf("primary view lifecycle events %v", lifecycle)
	}
}

func TestNewConnectionSupersedesOld(t *testing.T) {
	h := newTestHub(t, nil)
	old := h.mustDial("prod-1", "sess-a")
	h.mustDial("prod-1", "sess-a")
	select {
	case err := <-old.done:
		if codeOf(err) != connect.CodeAborted {
			t.Fatalf("old stream ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old connection kept running")
	}
	if st := h.reg.ListAgents().GetAgents()[0].GetState(); st != "connected" {
		t.Fatalf("state %q", st)
	}
}

func TestPerAgentCaps(t *testing.T) {
	t.Run("frame rate", func(t *testing.T) {
		h := newTestHub(t, func(o *Options) { o.FramesPerSec = 1; o.FrameBurst = 3 })
		s := h.mustDial("prod-1", "sess-a")
		for range 3 {
			s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_Heartbeat{Heartbeat: &asterismv1.AgentHeartbeat{}}})
		}
		s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_Heartbeat{Heartbeat: &asterismv1.AgentHeartbeat{}}})
		select {
		case err := <-s.done:
			if codeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("err %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("rate cap not enforced")
		}
		if st := h.reg.ListAgents().GetAgents()[0]; st.GetFramesRejected() != 1 || !strings.Contains(st.GetLastError(), "frame rate") {
			t.Fatalf("status %v", st)
		}
	})
	t.Run("frame size", func(t *testing.T) {
		h := newTestHub(t, func(o *Options) { o.MaxFrameBytes = 512 })
		s := h.mustDial("prod-1", "sess-a")
		big := &asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_Events{Events: &asterismv1.AgentEvents{Events: []*asterismv1.Event{{Message: strings.Repeat("x", 2048)}}}}}
		s.send(s.seqFrame(big))
		// An oversized replayable frame is skipped but acked, so it is not replayed forever.
		if a := s.next(isAck).GetAck().GetUptoSeq(); a != 1 {
			t.Fatalf("ack %d", a)
		}
		snap := snapshotFrame(1)
		snap.GetSnapshot().Containers = []*asterismv1.Container{{Name: strings.Repeat("y", 1024)}}
		s.send(snap)
		select {
		case err := <-s.done:
			if codeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("err %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("oversized snapshot accepted")
		}
	})
	t.Run("series", func(t *testing.T) {
		h := newTestHub(t, func(o *Options) { o.MaxSeries = 2 })
		drain := h.collectBus()
		s := h.mustDial("prod-1", "sess-a")
		dict := map[uint32][2]string{1: {"host", "cpu.util"}, 2: {"host", "load.1"}, 3: {"host", "mem.used_bytes"}, 4: {"@evil/ctr:x", "cpu.util"}}
		s.send(s.seqFrame(metricsFrame(h.clock.Now().UnixMilli(), dict, map[uint32]float32{1: 0.1, 2: 0.2, 3: 3, 4: 4})))
		s.sync()
		got := map[string]bool{}
		for _, sm := range drain().Samples {
			got[sm.EntityID+"/"+sm.Metric] = true
		}
		if len(got) != 2 || !got["host/cpu.util"] || !got["host/load.1"] {
			t.Fatalf("samples beyond the series cap: %v", got)
		}
		if st := h.reg.ListAgents().GetAgents()[0]; !strings.Contains(st.GetLastError(), "series cap") {
			t.Fatalf("status %v", st)
		}
	})
}

func TestMetricsFoldIntoViewsAndBus(t *testing.T) {
	h := newTestHub(t, func(o *Options) { o.PrimaryHost = "prod-1" }, Agent{Name: "prod-1"}, Agent{Name: "edge-2"})
	drain := h.collectBus()
	s := h.mustDial("prod-1", "sess-a")
	s.send(snapshotFrame(1, shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)))
	ts := h.clock.Now().UnixMilli()
	dict := map[uint32][2]string{
		1: {"host", "cpu.util"}, 2: {"host", "mem.used_bytes"}, 3: {"host", "load.1"},
		4: {"ctr:shop-api-1", "cpu.util"}, 5: {"ctr:shop-api-1", "mem.used_bytes"}, 6: {"ctr:shop-api-1", "net.rx_bps"}, 7: {"ctr:shop-api-1", "pids.count"},
	}
	s.send(s.seqFrame(metricsFrame(ts, dict, map[uint32]float32{1: 0.25, 2: 2 << 30, 3: 1.5, 4: 0.5, 5: 256 << 20, 6: 1200, 7: 9})))
	s.sync()

	got := map[string]float64{}
	for _, sm := range drain().Samples {
		got[sm.EntityID+" "+sm.Metric] = sm.Value
		if sm.TSMs != ts {
			t.Fatalf("sample %v has ts %d, want %d", sm, sm.TSMs, ts)
		}
	}
	for _, k := range []string{"host cpu.util", "ctr:shop-api-1 cpu.util", "ctr:shop-api-1 net.rx_bps", "proj:shop cpu.util", "proj:shop mem.used_bytes"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing bus sample %q in %v", k, got)
		}
	}
	v := h.reg.Primary()
	if hi := v.HostSnapshot(); hi.GetCpuUtil() != 0.25 || hi.GetMemUsedBytes() != 2<<30 || hi.GetLoad_1() != 1.5 || hi.GetSourceMode() != "agent" || hi.GetMemTotalBytes() != 8<<30 || hi.GetId() != "host" {
		t.Fatalf("host snapshot %v", hi)
	}
	c := v.DockerSnapshot().Containers[0]
	if c.GetStats().GetCpuUtil() != 0.5 || c.GetStats().GetMemUsedBytes() != 256<<20 || c.GetStats().GetPids() != 9 || c.GetStatus() == "" || c.GetRole() != asterismv1.WorkloadRole_WORKLOAD_ROLE_APP {
		t.Fatalf("container %v", c)
	}
	w, ok := v.Workload("ctr:shop-api-1")
	if !ok || w.GetCpuUtil() != 0.5 || w.GetProjectId() != "proj:shop" {
		t.Fatalf("workload %v", w)
	}
	if p := v.Projects(); len(p) != 1 || p[0].GetMemUsedBytes() != 256<<20 {
		t.Fatalf("projects %v", p)
	}

	// A non-primary host is ingested under @<host>/ and not shown.
	e := h.mustDial("edge-2", "sess-e")
	e.send(e.seqFrame(metricsFrame(ts, map[uint32][2]string{1: {"ctr:edge-proxy", "cpu.util"}}, map[uint32]float32{1: 0.1})))
	e.sync()
	var qualified bool
	for _, sm := range drain().Samples {
		if sm.EntityID == "@edge-2/ctr:edge-proxy" {
			qualified = true
		}
		if sm.EntityID == "ctr:edge-proxy" {
			t.Fatal("non-primary sample leaked into the primary namespace")
		}
	}
	if !qualified {
		t.Fatal("non-primary sample not stored under @edge-2/")
	}
}

func TestClockSkewIsRewritten(t *testing.T) {
	h := newTestHub(t, nil)
	drain := h.collectBus()
	behind := h.clock.Now().Add(-10 * time.Second)
	s, err := h.dial("prod-1", hello("prod-1", "sess-a"), behind)
	if err != nil {
		t.Fatal(err)
	}
	s.send(s.seqFrame(metricsFrame(behind.UnixMilli(), map[uint32][2]string{1: {"host", "cpu.util"}}, map[uint32]float32{1: 0.3})))
	s.sync()
	for _, sm := range drain().Samples {
		if sm.TSMs != h.clock.Now().UnixMilli() {
			t.Fatalf("sample ts %d not rewritten to hub time %d", sm.TSMs, h.clock.Now().UnixMilli())
		}
	}
	st := h.reg.ListAgents().GetAgents()[0]
	if st.GetClockSkewMs() != 10_000 || !strings.Contains(st.GetLastError(), "clock") {
		t.Fatalf("status %v", st)
	}

	// Below the 2 s threshold timestamps are kept.
	h2 := newTestHub(t, nil)
	drain2 := h2.collectBus()
	near := h2.clock.Now().Add(-time.Second)
	s2, _ := h2.dial("prod-1", hello("prod-1", "sess-a"), near)
	s2.send(s2.seqFrame(metricsFrame(near.UnixMilli(), map[uint32][2]string{1: {"host", "cpu.util"}}, map[uint32]float32{1: 0.3})))
	s2.sync()
	for _, sm := range drain2().Samples {
		if sm.TSMs != near.UnixMilli() {
			t.Fatalf("small skew rewritten: %d", sm.TSMs)
		}
	}
}

func TestStreamActionRoundTripAndDrop(t *testing.T) {
	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "sess-a")
	s.send(snapshotFrame(1, shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)))
	s.sync()
	v := h.reg.Primary()
	if got := v.SupportedContainerActions(); len(got) != 1 || got[0] != asterismv1.ContainerAction_CONTAINER_ACTION_RESTART {
		t.Fatalf("supported = %v (hub implements 5, agent allows restart)", got)
	}
	if r := v.ActionsUnavailableReason(); r != "" {
		t.Fatalf("unexpected reason %q", r)
	}

	type result struct {
		c     *asterismv1.Container
		attrs map[string]string
		err   error
	}
	run := func() chan result {
		ch := make(chan result, 1)
		go func() {
			c, attrs, err := v.ContainerActionAttributed(context.Background(), asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, "3f2a9c1b7d4e", 10*time.Second, "admin@example.com")
			ch <- result{c, attrs, err}
		}()
		return ch
	}

	ch := run()
	cmd := s.next(isCmd).GetCommand()
	if cmd.GetContainerId() != "3f2a9c1b7d4e" || cmd.GetActor() != "admin@example.com" || cmd.GetTimeoutSeconds() != 10 ||
		cmd.GetDeadlineMs() != h.clock.Now().Add(40*time.Second).UnixMilli() || cmd.GetRequestId() == "" {
		t.Fatalf("command %v", cmd)
	}
	after := shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)
	after.RestartCount = 1
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_CommandResult{CommandResult: &asterismv1.CommandResult{
		RequestId: cmd.GetRequestId(), Ok: true, Container: after, AuditId: "a1b2c3", DurationMs: 812,
	}}})
	res := <-ch
	if res.err != nil || res.c.GetRestartCount() != 1 || res.attrs["executed_by"] != "agent:prod-1" || res.attrs["agent_audit_id"] != "a1b2c3" {
		t.Fatalf("result %+v", res)
	}

	// An agent-side refusal keeps its code.
	ch = run()
	cmd = s.next(isCmd).GetCommand()
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_CommandResult{CommandResult: &asterismv1.CommandResult{
		RequestId: cmd.GetRequestId(), Code: "permission_denied", Error: "container \"shop-api-1\" matches actions.protect", AuditId: "d00d",
	}}})
	res = <-ch
	var ce *CommandError
	if !errors.As(res.err, &ce) || ce.ActionCode() != "permission_denied" || ce.AuditAttrs()["agent_audit_id"] != "d00d" {
		t.Fatalf("refusal %+v", res)
	}

	// The connection drops mid-command: the result is unknown.
	ch = run()
	s.next(isCmd)
	_ = s.hangup()
	res = <-ch
	if !errors.As(res.err, &ce) || ce.ActionCode() != "unavailable" || !strings.Contains(ce.Error(), "result unknown") {
		t.Fatalf("drop result %+v", res)
	}
	// Offline: nothing is sent.
	_, _, err := v.ContainerActionAttributed(context.Background(), asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, "3f2a9c1b7d4e", time.Second, "x")
	if !errors.As(err, &ce) || !strings.Contains(ce.Error(), "not connected") {
		t.Fatalf("offline err %v", err)
	}
}

func TestUnaryTransport(t *testing.T) {
	h := newTestHub(t, nil)
	ctx := context.Background()
	if _, err := h.reg.Push(ctx, "prod-1", &asterismv1.PushRequest{SessionId: "u1", Frames: []*asterismv1.AgentFrame{{Seq: 1}}}, "x"); codeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("push without hello: %v", err)
	}
	resp, err := h.reg.Push(ctx, "prod-1", &asterismv1.PushRequest{SessionId: "u1", Frames: []*asterismv1.AgentFrame{
		{SentAtMs: h.clock.Now().UnixMilli(), Payload: &asterismv1.AgentFrame_Hello{Hello: hello("prod-1", "u1")}},
	}}, "198.51.100.4:4000")
	if err != nil || len(resp.GetFrames()) != 1 || resp.GetFrames()[0].GetHelloAck().GetHostId() != "prod-1" {
		t.Fatalf("hello push %v %v", resp, err)
	}
	frames := []*asterismv1.AgentFrame{snapshotFrame(1, shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING))}
	for i := uint64(1); i <= 3; i++ {
		f := metricsFrame(h.clock.Now().UnixMilli(), map[uint32][2]string{1: {"host", "cpu.util"}}, map[uint32]float32{1: 0.1})
		f.Seq = i
		frames = append(frames, f)
	}
	resp, err = h.reg.Push(ctx, "prod-1", &asterismv1.PushRequest{SessionId: "u1", Frames: frames}, "x")
	if err != nil || resp.GetAckedSeq() != 3 {
		t.Fatalf("push %v %v", resp, err)
	}
	for _, f := range resp.GetFrames() {
		if f.GetAck() != nil {
			t.Fatal("unary acks travel in acked_seq, not as frames")
		}
	}

	sent := make(chan *asterismv1.HubFrame, 16)
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- h.reg.Commands(cctx, "prod-1", &asterismv1.CommandsRequest{SessionId: "u1"}, func(f *asterismv1.HubFrame) error { sent <- f; return nil })
	}()
	if f := <-sent; f.GetHints() == nil {
		t.Fatalf("first commands frame %v", f)
	}
	ch := make(chan error, 1)
	go func() {
		_, _, err := h.reg.Primary().ContainerActionAttributed(ctx, asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, "3f2a9c1b7d4e", time.Second, "op")
		ch <- err
	}()
	cmd := (<-sent).GetCommand()
	if cmd == nil {
		t.Fatal("command not delivered over the Commands stream")
	}
	if _, err := h.reg.Push(ctx, "prod-1", &asterismv1.PushRequest{SessionId: "u1", Frames: []*asterismv1.AgentFrame{
		{Payload: &asterismv1.AgentFrame_CommandResult{CommandResult: &asterismv1.CommandResult{RequestId: cmd.GetRequestId(), Ok: true}}},
	}}, "x"); err != nil {
		t.Fatal(err)
	}
	if err := <-ch; err != nil {
		t.Fatalf("unary action: %v", err)
	}
	if st := h.reg.ListAgents().GetAgents()[0]; st.GetTransport() != TransportUnary || st.GetState() != "connected" {
		t.Fatalf("status %v", st)
	}
	// Closing Commands ends the session.
	cancel()
	<-done
	if _, err := h.reg.Push(ctx, "prod-1", &asterismv1.PushRequest{SessionId: "u1", Frames: []*asterismv1.AgentFrame{{Seq: 9}}}, "x"); codeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("push after commands closed: %v", err)
	}
}

func TestHintsFollowLiveViewers(t *testing.T) {
	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "sess-a")
	if s.ack.GetHints().GetLiveViewers() {
		t.Fatal("no viewers yet")
	}
	h.reg.SetLiveViewers(1)
	if f := s.next(func(f *asterismv1.HubFrame) bool { return f.GetHints() != nil }); !f.GetHints().GetLiveViewers() || f.GetHints().GetFlushIntervalMs() != 2000 {
		t.Fatalf("hints %v", f)
	}
	h.reg.SetLiveViewers(2) // unchanged: no new frame
	h.reg.SetLiveViewers(0)
	if f := s.next(func(f *asterismv1.HubFrame) bool { return f.GetHints() != nil }); f.GetHints().GetLiveViewers() {
		t.Fatalf("hints %v", f)
	}
}

func TestViewGettersReturnCopies(t *testing.T) {
	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "sess-a")
	s.send(snapshotFrame(1, shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)))
	s.sync()
	v := h.reg.Primary()
	v.Projects()[0].Workloads[0].Name = "mutated"
	v.Workloads()[0].State = "mutated"
	w, _ := v.Workload("ctr:shop-api-1")
	w.Ports[0].HostPort = 1
	v.Ports()[0].HostIp = "mutated"
	v.DockerSnapshot().Containers[0].Labels["x"] = "y"
	v.HostSnapshot().Hostname = "mutated"
	got, _ := v.Workload("ctr:shop-api-1")
	if got.GetName() != "shop-api-1" || got.GetState() != "running" || got.GetPorts()[0].GetHostPort() != 8790 || v.Ports()[0].GetHostIp() == "mutated" ||
		v.DockerSnapshot().Containers[0].GetLabels()["x"] != "" || v.HostSnapshot().GetHostname() != "prod-1" {
		t.Fatal("a getter shares internal state")
	}
}

func TestHubRedactsAgentInventory(t *testing.T) {
	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "sess-a")
	c := shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)
	c.Labels["DB_PASSWORD"] = "s3cr3t-value"
	c.Labels["db.url"] = "postgres://app:s3cr3t-value@db:5432/app"
	c.Command = []string{"--token", "s3cr3t-value"}
	c.EnvKeys = []string{"API_KEY=s3cr3t-value", "PORT"}
	s.send(snapshotFrame(1, c))
	s.sync()
	got := h.reg.Primary().DockerSnapshot().Containers[0]
	w, _ := h.reg.Primary().Workload("ctr:shop-api-1")
	if strings.Contains(got.String()+w.String(), "s3cr3t-value") {
		t.Fatalf("secret reached the hub view: %v", got)
	}
	if got.GetRedactedLabelCount() != 1 || strings.Join(got.GetEnvKeys(), ",") != "API_KEY,PORT" {
		t.Fatalf("container %v", got)
	}
}
