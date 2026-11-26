package agentingest

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

type testHub struct {
	t     *testing.T
	reg   *Registry
	bus   *bus.Bus
	clock *fakeClock
}

func newTestHub(t *testing.T, tweak func(*Options), agents ...Agent) *testHub {
	t.Helper()
	if len(agents) == 0 {
		agents = []Agent{{Name: "prod-1", AllowActions: true}}
	}
	clk := newClock()
	b := bus.New(1024)
	opts := Options{
		Agents: agents, Bus: b, Now: clk.Now, HubVersion: "test", AckFrames: 1,
		Logger: slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if tweak != nil {
		tweak(&opts)
	}
	reg, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return &testHub{t: t, reg: reg, bus: b, clock: clk}
}

// fakeStream is an in-memory AgentService.Stream.
type fakeStream struct {
	in  chan *asterismv1.AgentFrame
	out chan *asterismv1.HubFrame
	eof chan struct{}
	one sync.Once
}

func (f *fakeStream) Receive() (*asterismv1.AgentFrame, error) {
	select {
	case fr := <-f.in:
		return fr, nil
	case <-f.eof:
		return nil, io.EOF
	}
}

func (f *fakeStream) Send(h *asterismv1.HubFrame) error {
	f.out <- h
	return nil
}

type session struct {
	t      *testing.T
	s      *fakeStream
	done   chan error
	cancel context.CancelFunc
	ack    *asterismv1.HelloAck
	seq    uint64
}

func hello(host, sessionID string) *asterismv1.AgentHello {
	return &asterismv1.AgentHello{ProtocolVersion: 1, AgentVersion: "0.2.0-test", SessionId: sessionID, HostName: host,
		Capabilities: []string{"docker", "action:restart"}, ConfigHash: "cafe",
		AllowedActions: []asterismv1.ContainerAction{asterismv1.ContainerAction_CONTAINER_ACTION_RESTART}}
}

// dial starts a bidi connection and returns after the hub answered hello
// (or with the handshake error).
func (h *testHub) dial(host string, hl *asterismv1.AgentHello, sentAt time.Time) (*session, error) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{t: h.t, s: &fakeStream{in: make(chan *asterismv1.AgentFrame), out: make(chan *asterismv1.HubFrame, 512), eof: make(chan struct{})}, done: make(chan error, 1), cancel: cancel}
	go func() { s.done <- h.reg.ServeStream(ctx, host, s.s, "203.0.113.7:51000") }()
	h.t.Cleanup(s.close)
	s.s.in <- &asterismv1.AgentFrame{SentAtMs: sentAt.UnixMilli(), Payload: &asterismv1.AgentFrame_Hello{Hello: hl}}
	select {
	case f := <-s.s.out:
		if f.GetHelloAck() == nil {
			h.t.Fatalf("first hub frame %v", f)
		}
		s.ack = f.GetHelloAck()
		return s, nil
	case err := <-s.done:
		return nil, err
	case <-time.After(5 * time.Second):
		h.t.Fatal("no hello ack")
		return nil, nil
	}
}

func (h *testHub) mustDial(host, sessionID string) *session {
	h.t.Helper()
	s, err := h.dial(host, hello(host, sessionID), h.clock.Now())
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}
	return s
}

func (s *session) close() {
	s.s.one.Do(func() { close(s.s.eof) })
	s.cancel()
}

// hangup ends the agent side and waits for ServeStream to return.
func (s *session) hangup() error {
	s.t.Helper()
	s.s.one.Do(func() { close(s.s.eof) })
	select {
	case err := <-s.done:
		return err
	case <-time.After(5 * time.Second):
		s.t.Fatal("ServeStream did not return")
		return nil
	}
}

func (s *session) send(f *asterismv1.AgentFrame) {
	s.t.Helper()
	select {
	case s.s.in <- f:
	case err := <-s.done:
		s.t.Fatalf("stream ended before frame was taken: %v", err)
	case <-time.After(5 * time.Second):
		s.t.Fatal("frame not taken")
	}
}

// next returns the next hub frame accepted by keep.
func (s *session) next(keep func(*asterismv1.HubFrame) bool) *asterismv1.HubFrame {
	s.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-s.s.out:
			if keep(f) {
				return f
			}
		case err := <-s.done:
			s.t.Fatalf("stream ended: %v", err)
		case <-deadline:
			s.t.Fatal("timed out waiting for a hub frame")
		}
	}
}

// sync sends a sequenced empty events frame and waits for its ack: every
// earlier frame has been processed once it arrives (AckFrames = 1).
func (s *session) sync() uint64 {
	s.t.Helper()
	s.seq++
	seq := s.seq
	s.send(&asterismv1.AgentFrame{Seq: seq, Payload: &asterismv1.AgentFrame_Events{Events: &asterismv1.AgentEvents{}}})
	s.next(func(f *asterismv1.HubFrame) bool { return f.GetAck().GetUptoSeq() >= seq })
	return seq
}

func (s *session) seqFrame(f *asterismv1.AgentFrame) *asterismv1.AgentFrame {
	s.seq++
	f.Seq = s.seq
	return f
}

func isAck(f *asterismv1.HubFrame) bool    { return f.GetAck() != nil }
func isResync(f *asterismv1.HubFrame) bool { return f.GetResync() != nil }
func isCmd(f *asterismv1.HubFrame) bool    { return f.GetCommand() != nil }

func codeOf(err error) connect.Code { return connect.CodeOf(err) }

// collectBus subscribes to the hub bus and returns a drain function.
func (h *testHub) collectBus() func() bus.Message {
	ch, unsub := h.bus.Subscribe()
	h.t.Cleanup(unsub)
	return func() bus.Message {
		var all bus.Message
		for {
			select {
			case m := <-ch:
				all.Samples = append(all.Samples, m.Samples...)
				all.Events = append(all.Events, m.Events...)
			default:
				return all
			}
		}
	}
}

func shopContainer(name, id string, state asterismv1.ContainerState) *asterismv1.Container {
	return &asterismv1.Container{
		Id: "ctr:" + name, ContainerId: id, Name: name, Image: "ghcr.io/acme/shop-api:1.4",
		State: state, ComposeProject: "shop", ComposeService: strings.TrimSuffix(strings.TrimPrefix(name, "shop-"), "-1"),
		StartedAtMs: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC).UnixMilli(),
		Ports:       []*asterismv1.ContainerPort{{HostIp: "127.0.0.1", HostPort: 8790, ContainerPort: 8080, Proto: "tcp", Published: true}},
		Labels:      map[string]string{"com.docker.compose.project": "shop"},
	}
}

func snapshotFrame(version uint64, cs ...*asterismv1.Container) *asterismv1.AgentFrame {
	return &asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_Snapshot{Snapshot: &asterismv1.InventorySnapshot{
		Version:    version,
		Host:       &asterismv1.HostInfo{Hostname: "prod-1", Os: "Ubuntu 24.04", Cores: 4, MemTotalBytes: 8 << 30},
		Engine:     &asterismv1.DockerEngine{Available: true, ServerVersion: "27.3.1"},
		Containers: cs,
	}}}
}

// metricsFrame announces dict entries (id -> entity/metric) in group 1 and
// one row with values, at tsMs.
func metricsFrame(tsMs int64, dict map[uint32][2]string, vals map[uint32]float32) *asterismv1.AgentFrame {
	m := &asterismv1.AgentMetrics{}
	for id := uint32(1); id <= 64; id++ {
		if e, ok := dict[id]; ok {
			m.Dict = append(m.Dict, &asterismv1.SeriesEntry{SeriesId: id, EntityId: e[0], Metric: e[1]})
			m.DictGroups = append(m.DictGroups, 1)
		}
	}
	if len(vals) > 0 {
		row := &asterismv1.MetricBatch{TsMs: tsMs}
		for id := uint32(1); id <= 64; id++ {
			if v, ok := vals[id]; ok {
				row.SeriesIds = append(row.SeriesIds, id)
				row.Values = append(row.Values, v)
			}
		}
		m.Rows = append(m.Rows, row)
		m.RowGroups = append(m.RowGroups, 1)
	}
	return &asterismv1.AgentFrame{SentAtMs: tsMs, Payload: &asterismv1.AgentFrame_Metrics{Metrics: m}}
}
