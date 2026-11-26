// Package uplink connects the agent to the hub (outbound only).
//
// The primary transport is one connect-rpc bidi stream (AgentService.Stream)
// over HTTP/2: TLS with ALPN for https://, h2c prior knowledge for loopback
// or insecure_dev http://. In transport "auto", a failed bidi handshake is
// followed by an attempt over unary Push plus a server-streaming Commands
// call; once that works the choice is kept for the process lifetime.
//
// Replayable frames (metrics, events) sit in a byte- and count-bounded ring
// until the hub acks them, and are replayed oldest-first after a reconnect.
// Inventory is state, not a log: every connection starts with a snapshot or
// a delta from the version the hub reports in HelloAck. Commands are never
// replayed, and their results are only sent on the connection that carried
// the command.
package uplink

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/sched"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"google.golang.org/protobuf/proto"
)

// ProtocolVersion is sent in AgentHello.protocol_version.
const ProtocolVersion = 1

// Transport names reported in AgentStats.transport.
const (
	TransportBidi  = "bidi"
	TransportUnary = "unary"
)

// Options configures a Client.
type Options struct {
	Config *config.Config
	Token  string
	Store  *state.Store
	// Hello returns the static part of AgentHello for each connection; the
	// client fills protocol_version, session_id and inventory_version.
	Hello    func() *v1.AgentHello
	Commands CommandHandler
	Logs     LogHandler
	// OnHints receives clamped hub hints (wire it to Scheduler.SetHints).
	OnHints func(state.Hints)
	// Stats fills process-level fields (rss, cpu, collector errors) of the
	// heartbeat; the client adds its own counters.
	Stats   func(*v1.AgentStats)
	Logger  *slog.Logger
	Version string

	// Tunables (defaults in parentheses); tests shorten them.
	HandshakeTimeout  time.Duration  // 15s
	HeartbeatEvery    time.Duration  // 10s
	StableAfter       time.Duration  // 60s: backoff resets after this long connected
	ReplayBytesPerSec int            // 64 KiB/s
	MaxPushBytes      int            // 512 KiB per unary Push
	Rand              func() float64 // backoff jitter
}

// Client is the uplink. Run it on one goroutine.
type Client struct {
	opts  Options
	cfg   *config.Config
	log   *slog.Logger
	store *state.Store

	cls        *clients
	bidiClient asterismv1connect.AgentServiceClient
	unaryCl    asterismv1connect.AgentServiceClient
	sessionID  string

	ring        *Ring
	seq         uint64
	gen         atomic.Uint64
	out         chan outMsg
	disp        *dispatcher
	backoff     Backoff
	hints       state.Hints
	flushEvery  time.Duration
	unaryLocked bool
	tokens      float64
	lastFill    time.Time

	mu    sync.Mutex
	stats clientStats

	logMu      sync.Mutex
	logCancels map[string]context.CancelFunc

	testHook func(event string)
}

type clientStats struct {
	framesSent    uint64
	bytesSent     uint64
	reconnects    uint32
	transport     string
	bufferBytes   uint64
	dropped       uint64
	droppedEvents uint64
	connected     bool
}

// New validates options and builds the HTTP transports. It does not dial.
func New(opts Options) (*Client, error) {
	if opts.Config == nil || opts.Store == nil {
		return nil, errors.New("uplink: Config and Store are required")
	}
	if opts.Token == "" {
		return nil, errors.New("uplink: no hub token")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.HandshakeTimeout, 15*time.Second)
	def(&opts.HeartbeatEvery, 10*time.Second)
	def(&opts.StableAfter, 60*time.Second)
	if opts.ReplayBytesPerSec <= 0 {
		opts.ReplayBytesPerSec = 64 << 10
	}
	if opts.MaxPushBytes <= 0 {
		opts.MaxPushBytes = 512 << 10
	}
	cfg := opts.Config
	ua := "asterism-agent/" + opts.Version
	cls, err := newClients(cfg.Hub, opts.Token, ua)
	if err != nil {
		return nil, err
	}
	copts := []connect.ClientOption{connect.WithReadMaxBytes(8 << 20)}
	if cfg.Hub.Compression == "gzip" {
		copts = append(copts, lowMemoryGzip(), connect.WithSendCompression("gzip"), connect.WithCompressMinBytes(512))
	}
	url := trimURL(cfg.Hub.URL)
	c := &Client{
		opts:       opts,
		cfg:        cfg,
		log:        opts.Logger,
		store:      opts.Store,
		cls:        cls,
		bidiClient: asterismv1connect.NewAgentServiceClient(cls.bidi, url, copts...),
		unaryCl:    asterismv1connect.NewAgentServiceClient(cls.unary, url, copts...),
		sessionID:  newSessionID(),
		ring:       NewRing(int(cfg.Hub.Buffer.MaxBytes), cfg.Hub.Buffer.MaxEvents),
		out:        make(chan outMsg, 64),
		backoff:    Backoff{Initial: cfg.Hub.Backoff.Initial.D(), Max: cfg.Hub.Backoff.Max.D(), Rand: opts.Rand},
		flushEvery: sched.FlushInterval(state.Hints{}, cfg.Intervals),
	}
	c.disp = newDispatcher(opts.Commands, c.out, &c.gen, time.Now)
	return c, nil
}

// SessionID is the random id sent in Hello for this process.
func (c *Client) SessionID() string { return c.sessionID }

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (c *Client) hook(ev string) {
	if c.testHook != nil {
		c.testHook(ev)
	}
}

// Run connects, serves and reconnects until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	defer c.cls.close()
	var wg sync.WaitGroup
	wg.Go(func() { c.disp.run(ctx) })
	defer wg.Wait()

	flush := time.NewTimer(c.flushEvery)
	defer flush.Stop()
	first := true
	for {
		if !first {
			c.mu.Lock()
			c.stats.reconnects++
			c.mu.Unlock()
		}
		first = false
		at, err := c.connect(ctx, flush)
		c.gen.Add(1) // results and log chunks of that connection are now stale
		c.setConnected(false, "")
		if ctx.Err() != nil {
			return nil
		}
		if !at.IsZero() && time.Since(at) >= c.opts.StableAfter {
			c.backoff.Reset()
		}
		delay := c.backoff.Next()
		c.logDisconnect(err, at, delay)
		if err != nil {
			c.hook("error:" + err.Error())
		}
		c.hook("disconnected")
		wait := time.NewTimer(delay)
	waitLoop:
		for {
			select {
			case <-ctx.Done():
				wait.Stop()
				return nil
			case <-wait.C:
				break waitLoop
			case <-flush.C:
				c.produce()
				flush.Reset(c.flushEvery)
			case <-c.out: // stale: never replayed
			}
		}
	}
}

func (c *Client) logDisconnect(err error, at time.Time, delay time.Duration) {
	attrs := []any{"retry_in", delay.Round(time.Millisecond), "attempt", c.backoff.Attempt()}
	if !at.IsZero() {
		attrs = append(attrs, "connected_for", time.Since(at).Round(time.Second))
	}
	switch code := connect.CodeOf(err); {
	case err == nil:
		c.log.Info("hub connection closed", attrs...)
	case code == connect.CodeUnauthenticated || code == connect.CodePermissionDenied:
		c.log.Error("hub rejected the agent token: check hub.token_file and that the token was created for this host", append(attrs, "err", err)...)
	default:
		var pin *PinMismatchError
		if errors.As(err, &pin) {
			c.log.Error("hub TLS pin mismatch", append(attrs, "err", pin.Error())...)
			return
		}
		c.log.Warn("hub connection failed", append(attrs, "err", err)...)
	}
}

// connect runs one connection attempt (with the auto fallback) and returns
// when it ends. at is when the handshake completed (zero if it never did).
func (c *Client) connect(ctx context.Context, flush *time.Timer) (time.Time, error) {
	mode := c.cfg.Hub.Transport
	if mode == TransportUnary || c.unaryLocked {
		return c.session(ctx, flush, c.newUnary)
	}
	at, err := c.session(ctx, flush, c.newBidi)
	if !at.IsZero() || err == nil || mode != "auto" || !fallbackCandidate(ctx, err) {
		return at, err
	}
	c.log.Info("bidi stream unavailable; trying the unary transport", "err", err)
	uat, uerr := c.session(ctx, flush, c.newUnary)
	if uat.IsZero() {
		return uat, errors.Join(err, uerr)
	}
	return uat, uerr
}

func fallbackCandidate(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return false
	}
	var pin *PinMismatchError
	if errors.As(err, &pin) {
		return false
	}
	// A hub that cannot be reached at all (connection refused, DNS, dial
	// timeout: it is down or restarting) says nothing about HTTP/2 support.
	// Falling back then could lock the process to unary if the hub came
	// back between the two attempts.
	var op *net.OpError
	var dns *net.DNSError
	return !errors.As(err, &op) && !errors.As(err, &dns)
}

func (c *Client) setConnected(ok bool, transport string) {
	c.mu.Lock()
	c.stats.connected = ok
	c.stats.transport = transport
	c.mu.Unlock()
}

// session runs one connection until it fails or ctx ends.
func (c *Client) session(ctx context.Context, flush *time.Timer, mk func(context.Context) conn) (time.Time, error) {
	gen := c.gen.Add(1)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cn := mk(sctx)
	defer cn.close()

	hello := c.helloFrame()
	ack, extra, err := cn.handshake(sctx, hello, c.opts.HandshakeTimeout)
	if err != nil {
		return time.Time{}, err
	}
	at := time.Now()
	if cn.name() == TransportUnary && c.cfg.Hub.Transport == "auto" && !c.unaryLocked {
		c.unaryLocked = true
		c.log.Info("using the unary transport for the rest of this process (HTTP/2 bidi streams are not available through the path to the hub)")
	}
	c.setConnected(true, cn.name())
	c.log.Info("connected to hub", "transport", cn.name(), "host_id", ack.GetHostId(), "hub_version", ack.GetHubVersion(),
		"known_inventory_version", ack.GetKnownInventoryVersion(), "resync", ack.GetResync())
	if st := ack.GetServerTimeMs(); st != 0 {
		if skew := time.Duration(time.Now().UnixMilli()-st) * time.Millisecond; skew > 2*time.Second || skew < -2*time.Second {
			c.log.Warn("agent clock differs from the hub clock; the hub rewrites timestamps", "skew", skew.Round(time.Millisecond))
		}
	}
	c.hook("connected:" + cn.name())
	if ack.GetHints() != nil {
		c.applyHints(ack.GetHints(), flush)
	}

	// Connection start: inventory, then the full series dictionary, then
	// buffered frames oldest-first.
	c.ring.ResetSent()
	c.store.ForceKeyframes()
	c.tokens, c.lastFill = float64(c.opts.ReplayBytesPerSec), time.Now()
	hubVer := ack.GetKnownInventoryVersion()
	var initial []*v1.AgentFrame
	initial, hubVer = c.inventoryFrames(initial, hubVer, ack.GetResync())
	if d := c.store.FullDict(); len(d.GetDict()) > 0 {
		initial = append(initial, c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_Metrics{Metrics: d}}))
	}
	c.produce()
	if err := c.send(sctx, cn, initial, &hubVer, gen, flush); err != nil {
		return at, err
	}
	for _, f := range extra {
		if err := c.handleHub(sctx, cn, f, &hubVer, gen, flush); err != nil {
			return at, err
		}
	}
	replay := time.NewTimer(time.Hour)
	replay.Stop()
	defer replay.Stop()
	if err := c.pump(sctx, cn, replay, &hubVer, gen, flush); err != nil {
		return at, err
	}
	hb := time.NewTicker(c.opts.HeartbeatEvery)
	defer hb.Stop()
	for {
		select {
		case <-sctx.Done():
			return at, ctx.Err()
		case err := <-cn.errc():
			return at, err
		case f := <-cn.recv():
			if err := c.handleHub(sctx, cn, f, &hubVer, gen, flush); err != nil {
				return at, err
			}
		case <-flush.C:
			c.produce()
			var fs []*v1.AgentFrame
			fs, hubVer = c.inventoryFrames(fs, hubVer, false)
			flush.Reset(c.flushEvery)
			if err := c.send(sctx, cn, fs, &hubVer, gen, flush); err != nil {
				return at, err
			}
			if err := c.pump(sctx, cn, replay, &hubVer, gen, flush); err != nil {
				return at, err
			}
		case <-replay.C:
			if err := c.pump(sctx, cn, replay, &hubVer, gen, flush); err != nil {
				return at, err
			}
		case <-hb.C:
			if err := c.send(sctx, cn, []*v1.AgentFrame{c.heartbeatFrame()}, &hubVer, gen, flush); err != nil {
				return at, err
			}
		case m := <-c.out:
			if m.gen != gen {
				continue // belongs to an older connection: never replayed
			}
			if err := c.send(sctx, cn, []*v1.AgentFrame{c.frame(m.frame)}, &hubVer, gen, flush); err != nil {
				return at, err
			}
		}
	}
}

func (c *Client) helloFrame() *v1.AgentFrame {
	var h *v1.AgentHello
	if c.opts.Hello != nil {
		h = proto.Clone(c.opts.Hello()).(*v1.AgentHello)
	} else {
		h = &v1.AgentHello{}
	}
	h.ProtocolVersion = ProtocolVersion
	h.SessionId = c.sessionID
	if h.AgentVersion == "" {
		h.AgentVersion = c.opts.Version
	}
	h.InventoryVersion = c.store.InventoryVersion()
	return c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_Hello{Hello: h}})
}

// frame stamps sent_at_ms on an unsequenced frame.
func (c *Client) frame(f *v1.AgentFrame) *v1.AgentFrame {
	if f.SentAtMs == 0 {
		f.SentAtMs = time.Now().UnixMilli()
	}
	return f
}

func (c *Client) heartbeatFrame() *v1.AgentFrame {
	return c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_Heartbeat{Heartbeat: &v1.AgentHeartbeat{Stats: c.Stats()}}})
}

// inventoryFrames appends a snapshot or delta that brings the hub from
// version known to the current version, and returns the new hub version.
func (c *Client) inventoryFrames(fs []*v1.AgentFrame, known uint64, force bool) ([]*v1.AgentFrame, uint64) {
	snap, delta := c.store.TakeInventory(known, force)
	switch {
	case snap != nil:
		return append(fs, c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_Snapshot{Snapshot: snap}})), snap.GetVersion()
	case delta != nil:
		return append(fs, c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_Delta{Delta: delta}})), delta.GetVersion()
	default:
		return fs, known
	}
}

// produce moves committed metric rows and events from the store into the
// replay ring. It runs whether or not a connection is up.
func (c *Client) produce() {
	now := time.Now().UnixMilli()
	if m := c.store.TakeMetrics(); m != nil {
		c.seq++
		c.pushRing(&v1.AgentFrame{Seq: c.seq, SentAtMs: now, Payload: &v1.AgentFrame_Metrics{Metrics: m}})
	}
	if ev := c.store.TakeEvents(); len(ev) > 0 {
		c.seq++
		c.pushRing(&v1.AgentFrame{Seq: c.seq, SentAtMs: now, Payload: &v1.AgentFrame_Events{Events: &v1.AgentEvents{Events: ev}}})
	}
}

func (c *Client) pushRing(f *v1.AgentFrame) {
	if n := c.ring.Push(f); n > 0 {
		// Carry-forward state on the hub is now stale for the dropped rows.
		c.store.ForceKeyframes()
		c.hook("dropped")
	}
	c.syncRingStats()
}

func (c *Client) syncRingStats() {
	c.mu.Lock()
	c.stats.bufferBytes = uint64(c.ring.Bytes())
	c.stats.dropped = c.ring.Dropped()
	c.stats.droppedEvents = c.ring.DroppedEvents()
	c.mu.Unlock()
}

// pump sends unsent ring frames within the replay byte budget and arms
// replay when more remain.
func (c *Client) pump(ctx context.Context, cn conn, replay *time.Timer, hubVer *uint64, gen uint64, flush *time.Timer) error {
	rate := float64(c.opts.ReplayBytesPerSec)
	now := time.Now()
	c.tokens = min(rate, c.tokens+rate*now.Sub(c.lastFill).Seconds())
	c.lastFill = now
	var batch []*v1.AgentFrame
	for {
		f, size, ok := c.ring.NextUnsent()
		if !ok {
			break
		}
		if need := min(float64(size), rate); c.tokens < need {
			wait := time.Duration((need - c.tokens) / rate * float64(time.Second))
			replay.Reset(max(wait, 10*time.Millisecond))
			break
		}
		c.tokens -= float64(size)
		c.ring.MarkSent(f.GetSeq())
		batch = append(batch, f)
	}
	return c.send(ctx, cn, batch, hubVer, gen, flush)
}

// send transmits frames and processes any hub frames the transport returns
// inline (unary acks and piggy-backed frames).
func (c *Client) send(ctx context.Context, cn conn, fs []*v1.AgentFrame, hubVer *uint64, gen uint64, flush *time.Timer) error {
	if len(fs) == 0 {
		return nil
	}
	back, err := cn.send(ctx, fs)
	if err != nil {
		return err
	}
	n := 0
	for _, f := range fs {
		n += proto.Size(f)
	}
	c.mu.Lock()
	c.stats.framesSent += uint64(len(fs))
	c.stats.bytesSent += uint64(n)
	c.mu.Unlock()
	for _, f := range back {
		if err := c.handleHub(ctx, cn, f, hubVer, gen, flush); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) handleHub(ctx context.Context, cn conn, f *v1.HubFrame, hubVer *uint64, gen uint64, flush *time.Timer) error {
	switch p := f.GetPayload().(type) {
	case *v1.HubFrame_Ack:
		if n := c.ring.Ack(p.Ack.GetUptoSeq()); n > 0 {
			c.syncRingStats()
		}
		c.hook(fmt.Sprintf("acked:%d", p.Ack.GetUptoSeq()))
	case *v1.HubFrame_Hints:
		c.applyHints(p.Hints, flush)
	case *v1.HubFrame_Resync:
		c.log.Info("hub requested an inventory resync", "reason", p.Resync.GetReason())
		var fs []*v1.AgentFrame
		fs, *hubVer = c.inventoryFrames(fs, *hubVer, true)
		if d := c.store.FullDict(); len(d.GetDict()) > 0 {
			fs = append(fs, c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_Metrics{Metrics: d}}))
		}
		c.store.ForceKeyframes()
		return c.send(ctx, cn, fs, hubVer, gen, flush)
	case *v1.HubFrame_Command:
		if res := c.disp.submit(gen, p.Command); res != nil {
			c.log.Info("audit", "request_id", res.GetRequestId(), "actor", p.Command.GetActor(),
				"action", p.Command.GetAction().String(), "target_ref", p.Command.GetContainerId(), "result", "refused", "code", res.GetCode())
			return c.send(ctx, cn, []*v1.AgentFrame{c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_CommandResult{CommandResult: res}})}, hubVer, gen, flush)
		}
	case *v1.HubFrame_Ping:
		return c.send(ctx, cn, []*v1.AgentFrame{c.heartbeatFrame()}, hubVer, gen, flush)
	case *v1.HubFrame_LogRequest:
		return c.startLogs(ctx, cn, p.LogRequest, hubVer, gen, flush)
	case *v1.HubFrame_LogCancel:
		c.cancelLogs(p.LogCancel.GetRequestId())
	case *v1.HubFrame_HelloAck:
		// Only valid during the handshake; ignore repeats.
	}
	return nil
}

func (c *Client) applyHints(h *v1.AgentHints, flush *time.Timer) {
	c.hints = state.HintsFromProto(h)
	next := sched.FlushInterval(c.hints, c.cfg.Intervals)
	if next < c.flushEvery {
		flush.Reset(next)
	}
	c.flushEvery = next
	if c.opts.OnHints != nil {
		c.opts.OnHints(c.hints)
	}
	c.hook("hints")
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

func (c *Client) startLogs(ctx context.Context, cn conn, req *v1.LogRequest, hubVer *uint64, gen uint64, flush *time.Timer) error {
	if c.opts.Logs == nil {
		chunk := &v1.LogChunk{RequestId: req.GetRequestId(), Eof: true, Error: "logs are not available on this agent"}
		return c.send(ctx, cn, []*v1.AgentFrame{c.frame(&v1.AgentFrame{Payload: &v1.AgentFrame_LogChunk{LogChunk: chunk}})}, hubVer, gen, flush)
	}
	lctx, cancel := context.WithCancel(ctx)
	c.logMu.Lock()
	if c.logCancels == nil {
		c.logCancels = map[string]context.CancelFunc{}
	}
	c.logCancels[req.GetRequestId()] = cancel
	c.logMu.Unlock()
	go func() {
		defer c.cancelLogs(req.GetRequestId())
		c.opts.Logs.Logs(lctx, req, func(ch *v1.LogChunk) bool {
			ch.RequestId = req.GetRequestId()
			f := &v1.AgentFrame{SentAtMs: time.Now().UnixMilli(), Payload: &v1.AgentFrame_LogChunk{LogChunk: ch}}
			select {
			case c.out <- outMsg{gen: gen, frame: f}:
				return lctx.Err() == nil
			case <-lctx.Done():
				return false
			}
		})
	}()
	return nil
}

func (c *Client) cancelLogs(id string) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	if cancel, ok := c.logCancels[id]; ok {
		cancel()
		delete(c.logCancels, id)
	}
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

// Stats returns the heartbeat statistics. Safe from any goroutine.
func (c *Client) Stats() *v1.AgentStats {
	s := &v1.AgentStats{}
	if c.opts.Stats != nil {
		c.opts.Stats(s)
	}
	c.mu.Lock()
	s.FramesSent = c.stats.framesSent
	s.FramesDropped = c.stats.dropped
	s.BytesSent = c.stats.bytesSent
	s.BufferBytes = c.stats.bufferBytes
	s.Reconnects = c.stats.reconnects
	s.Transport = c.stats.transport
	dropped := c.stats.droppedEvents
	c.mu.Unlock()
	st := c.store.Stats()
	s.Series = uint32(st.Series)
	s.EventsDropped = st.EventsDropped + dropped
	s.Goroutines = uint32(runtime.NumGoroutine())
	return s
}

// Connected reports whether a hub session is currently established.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats.connected
}

// lowMemoryGzip replaces connect's default gzip (DefaultCompression, about
// 1.1 MB of encoder state per pooled writer) with BestSpeed (about 0.8 MB).
// Frames are 2-6 KB, so the ratio loss is about 10% of a ~100 B/s stream,
// while the saving is a sizeable part of the 15 MB RSS budget.
func lowMemoryGzip() connect.ClientOption {
	return connect.WithAcceptCompression("gzip",
		func() connect.Decompressor { return &gzip.Reader{} },
		func() connect.Compressor {
			w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
			return w
		})
}
