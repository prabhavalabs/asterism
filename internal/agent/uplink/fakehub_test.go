package uplink

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
)

// fakeHub is an in-process AgentService: it applies inventory with the
// reference Inventory, decodes metrics with the reference Decoder, acks
// replayable frames, and lets tests push HubFrames to the live connection.
type fakeHub struct {
	asterismv1connect.UnimplementedAgentServiceHandler

	t      *testing.T
	frames chan *v1.AgentFrame // every agent frame, in arrival order
	toAgnt chan *v1.HubFrame   // frames for the current connection

	bidiUnimplemented atomic.Bool
	reject            atomic.Bool // every RPC fails with Unavailable
	autoAck           atomic.Bool
	forgetOnConnect   atomic.Bool // simulate a hub restart (known=0)
	streams           atomic.Int32
	pushes            atomic.Int32

	mu      sync.Mutex
	inv     *state.Inventory
	dec     *state.Decoder
	lastSeq uint64
	seqs    []uint64
	points  []state.Point
	token   string
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{
		t:      t,
		frames: make(chan *v1.AgentFrame, 4096),
		toAgnt: make(chan *v1.HubFrame, 16),
		inv:    state.NewInventory(),
		dec:    state.NewDecoder(),
	}
	h.autoAck.Store(true)
	return h
}

// serve starts an httptest server. h2c enables HTTP/2 without TLS.
func (h *fakeHub) serve(h2c bool) *httptest.Server {
	mux := http.NewServeMux()
	path, handler := asterismv1connect.NewAgentServiceHandler(h)
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.token = r.Header.Get("Authorization")
		h.mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	if h2c {
		srv.Config.Protocols.SetUnencryptedHTTP2(true)
	}
	srv.Start()
	h.t.Cleanup(srv.Close)
	return srv
}

func (h *fakeHub) knownVersion() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.forgetOnConnect.Load() {
		h.inv = state.NewInventory()
		h.dec = state.NewDecoder()
		h.lastSeq = 0
	}
	return h.inv.Version
}

// ingest applies one agent frame and returns the ack to send (0 = none) and
// whether a resync is needed.
func (h *fakeHub) ingest(f *v1.AgentFrame) (ack uint64, resync bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case h.frames <- f:
	default:
		h.t.Error("fake hub frame log full")
	}
	if seq := f.GetSeq(); seq > 0 {
		if seq <= h.lastSeq {
			return h.lastSeq, false // replay of something already processed
		}
		h.lastSeq = seq
		h.seqs = append(h.seqs, seq)
	}
	switch p := f.GetPayload().(type) {
	case *v1.AgentFrame_Snapshot:
		h.inv.ApplySnapshot(p.Snapshot)
	case *v1.AgentFrame_Delta:
		if err := h.inv.ApplyDelta(p.Delta); err != nil {
			resync = true
		}
	case *v1.AgentFrame_Metrics:
		if err := h.dec.Apply(p.Metrics, func(pt state.Point) { h.points = append(h.points, pt) }); err != nil {
			h.t.Errorf("hub decode: %v", err)
		}
	}
	if f.GetSeq() > 0 && h.autoAck.Load() {
		return h.lastSeq, resync
	}
	return 0, resync
}

func (h *fakeHub) helloAck() *v1.HubFrame {
	return &v1.HubFrame{Payload: &v1.HubFrame_HelloAck{HelloAck: &v1.HelloAck{
		HostId: "prod-1", HubVersion: "test", ServerTimeMs: time.Now().UnixMilli(),
		KnownInventoryVersion: h.knownVersion(),
	}}}
}

func (h *fakeHub) Stream(ctx context.Context, s *connect.BidiStream[v1.AgentFrame, v1.HubFrame]) error {
	h.streams.Add(1)
	if h.reject.Load() {
		return connect.NewError(connect.CodeUnavailable, errors.New("hub warming up"))
	}
	if h.bidiUnimplemented.Load() {
		return connect.NewError(connect.CodeUnimplemented, errors.New("bidi disabled by proxy"))
	}
	first, err := s.Receive()
	if err != nil {
		return err
	}
	if first.GetHello() == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("want hello"))
	}
	h.ingest(first)
	if err := s.Send(h.helloAck()); err != nil {
		return err
	}
	out := make(chan *v1.HubFrame, 64)
	errc := make(chan error, 1)
	go func() {
		for {
			f, err := s.Receive()
			if err != nil {
				errc <- err
				return
			}
			ack, resync := h.ingest(f)
			if ack > 0 {
				out <- &v1.HubFrame{Payload: &v1.HubFrame_Ack{Ack: &v1.Ack{UptoSeq: ack}}}
			}
			if resync {
				out <- &v1.HubFrame{Payload: &v1.HubFrame_Resync{Resync: &v1.ResyncRequest{Reason: "version mismatch"}}}
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "EOF") {
				return nil
			}
			return err
		case f := <-out:
			if err := s.Send(f); err != nil {
				return err
			}
		case f := <-h.toAgnt:
			if err := s.Send(f); err != nil {
				return err
			}
		}
	}
}

func (h *fakeHub) Push(_ context.Context, req *connect.Request[v1.PushRequest]) (*connect.Response[v1.PushResponse], error) {
	h.pushes.Add(1)
	if h.reject.Load() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("hub warming up"))
	}
	resp := &v1.PushResponse{}
	for _, f := range req.Msg.GetFrames() {
		if f.GetHello() != nil {
			h.ingest(f)
			resp.Frames = append(resp.Frames, h.helloAck())
			continue
		}
		ack, resync := h.ingest(f)
		if ack > resp.AckedSeq {
			resp.AckedSeq = ack
		}
		if resync {
			resp.Frames = append(resp.Frames, &v1.HubFrame{Payload: &v1.HubFrame_Resync{Resync: &v1.ResyncRequest{Reason: "version mismatch"}}})
		}
	}
	return connect.NewResponse(resp), nil
}

func (h *fakeHub) Commands(ctx context.Context, _ *connect.Request[v1.CommandsRequest], s *connect.ServerStream[v1.HubFrame]) error {
	if h.reject.Load() {
		return connect.NewError(connect.CodeUnavailable, errors.New("hub warming up"))
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-h.toAgnt:
			if err := s.Send(f); err != nil {
				return err
			}
		}
	}
}

// next returns the next agent frame accepted by keep, skipping others.
func (h *fakeHub) next(keep func(*v1.AgentFrame) bool) *v1.AgentFrame {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f := <-h.frames:
			if keep(f) {
				return f
			}
		case <-deadline:
			h.t.Fatal("timed out waiting for an agent frame")
			return nil
		}
	}
}

func isHello(f *v1.AgentFrame) bool     { return f.GetHello() != nil }
func isHeartbeat(f *v1.AgentFrame) bool { return f.GetHeartbeat() != nil }
func isSnapshot(f *v1.AgentFrame) bool  { return f.GetSnapshot() != nil }
func isDelta(f *v1.AgentFrame) bool     { return f.GetDelta() != nil }
func isInventory(f *v1.AgentFrame) bool { return isSnapshot(f) || isDelta(f) }
func isRows(f *v1.AgentFrame) bool      { return len(f.GetMetrics().GetRows()) > 0 }
func isResult(f *v1.AgentFrame) bool    { return f.GetCommandResult() != nil }

// ---------------------------------------------------------------------------
// Agent harness
// ---------------------------------------------------------------------------

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

type agentHarness struct {
	t      *testing.T
	c      *Client
	store  *state.Store
	sink   *state.Handle
	events chan string
	cancel context.CancelFunc
	done   chan struct{}
}

func testConfig(url string) *config.Config {
	cfg := config.Default()
	cfg.Hub.URL = url
	cfg.Hub.Token = "asta_test"
	cfg.Hub.Backoff.Initial = config.Duration(5 * time.Millisecond)
	cfg.Hub.Backoff.Max = config.Duration(20 * time.Millisecond)
	cfg.Intervals.Flush = config.Duration(20 * time.Millisecond)
	cfg.Intervals.Min = config.Duration(10 * time.Millisecond)
	return cfg
}

func startAgent(t *testing.T, cfg *config.Config, cmds CommandHandler, tweak func(*Options)) *agentHarness {
	t.Helper()
	st := state.NewStore(state.Options{})
	opts := Options{
		Config: cfg, Token: cfg.Hub.Token, Store: st, Commands: cmds, Version: "test",
		Hello:            func() *v1.AgentHello { return &v1.AgentHello{HostName: "prod-1", Capabilities: []string{"docker"}} },
		HandshakeTimeout: 2 * time.Second,
		HeartbeatEvery:   time.Hour,
		StableAfter:      time.Hour,
		Logger:           slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if tweak != nil {
		tweak(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	h := &agentHarness{t: t, c: c, store: st, sink: st.Sink("host"), events: make(chan string, 4096), done: make(chan struct{})}
	c.testHook = func(ev string) {
		select {
		case h.events <- ev:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		_ = c.Run(ctx)
	}()
	t.Cleanup(h.stop)
	return h
}

func (h *agentHarness) stop() {
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		h.t.Error("uplink did not stop")
	}
}

// await waits for a hook event with the given prefix.
func (h *agentHarness) await(prefix string) string {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if strings.HasPrefix(ev, prefix) {
				return ev
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for %q", prefix)
			return ""
		}
	}
}

// sample commits one host row.
func (h *agentHarness) sample(metric string, v float32, at time.Time) {
	id := h.sink.Series("host", metric)
	h.sink.Set(id, v)
	h.sink.Commit(at, true)
}
