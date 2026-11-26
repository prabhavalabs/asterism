package agentingest

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Transport names (AgentStatus.transport).
const (
	TransportBidi  = "bidi"
	TransportUnary = "unary"
)

var (
	errSuperseded = errors.New("superseded by a newer connection from the same agent")
	errStale      = errors.New("agent went silent")
)

// conn is one transport connection of a host session. Hub frames for the
// agent (commands, hints, resync, log requests) queue on out; the transport
// loop owns sending.
type conn struct {
	id        uint64
	transport string
	sessionID string
	out       chan *asterismv1.HubFrame
	done      chan struct{}
	once      sync.Once
	err       error

	// Guarded by hostState.mu.
	unacked   int
	lastAcked uint64
}

func newConn(id uint64, transport, session string) *conn {
	return &conn{id: id, transport: transport, sessionID: session, out: make(chan *asterismv1.HubFrame, 64), done: make(chan struct{})}
}

func (c *conn) close(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
	})
}

// trySend queues f without blocking; it reports false when the queue is full
// or the connection is closed.
func (c *conn) trySend(f *asterismv1.HubFrame) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.out <- f:
		return true
	default:
		return false
	}
}

type pendingCmd struct {
	conn *conn
	ch   chan *asterismv1.CommandResult
}

type pendingLog struct {
	conn *conn
	ch   chan *asterismv1.LogChunk
}

type pendingIcon struct {
	conn *conn
	ch   chan *asterismv1.IconData
}

// hostState is one enrolled host's session. mu serialises every frame of the
// host, so per-session state needs no further locking.
type hostState struct {
	reg     *Registry
	agent   Agent
	primary bool
	view    *HostView

	mu        sync.Mutex
	sessionID string
	inv       *state.Inventory
	dec       *state.Decoder
	lastSeq   uint64
	conn      *conn

	everSeen       bool
	online         bool
	lastSeen       time.Time
	connectedSince time.Time
	agentVersion   string
	transport      string
	remoteAddr     string
	lagMs          int64
	skewMs         int64
	skewAdjMs      int64
	stats          *asterismv1.AgentStats
	caps           []string
	configHash     string
	lastError      string
	allowed        []asterismv1.ContainerAction
	bytesReceived  uint64
	framesRejected uint64
	duplicates     uint64
	seriesCapped   bool
	machineLogged  bool

	tokens   float64
	lastFill time.Time

	pending map[string]*pendingCmd
	logs    map[string]*pendingLog
	icons   map[string]*pendingIcon
}

func newHostState(r *Registry, a Agent, primary bool) *hostState {
	hs := &hostState{
		reg: r, agent: a, primary: primary,
		inv: state.NewInventory(), dec: state.NewDecoder(),
		pending: map[string]*pendingCmd{}, logs: map[string]*pendingLog{}, icons: map[string]*pendingIcon{},
		tokens: r.opts.FrameBurst,
	}
	hs.view = newHostView(hs)
	return hs
}

func (hs *hostState) qualify(entity string) string { return qualify(hs.primary, hs.agent.Name, entity) }

// stateName is "never", "connected", "stale" (disconnected, within the
// reconnect grace of stale_after) or "offline".
func (hs *hostState) stateNameLocked(now time.Time) string {
	switch {
	case !hs.everSeen:
		return "never"
	case now.Sub(hs.lastSeen) >= hs.reg.opts.StaleAfter || !hs.online:
		return "offline"
	case hs.conn != nil:
		return "connected"
	default:
		return "stale"
	}
}

func (hs *hostState) status(now time.Time) *asterismv1.AgentStatus {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	st := &asterismv1.AgentStatus{
		HostId:           hs.agent.Name,
		Name:             hs.agent.Name,
		State:            hs.stateNameLocked(now),
		AgentVersion:     hs.agentVersion,
		Transport:        hs.transport,
		RemoteAddr:       hs.remoteAddr,
		LagMs:            hs.lagMs,
		ClockSkewMs:      hs.skewMs,
		Capabilities:     slices.Clone(hs.caps),
		ConfigHash:       hs.configHash,
		LastError:        hs.lastError,
		ActionsEnabled:   hs.agent.AllowActions && len(hs.allowed) > 0,
		BytesReceived:    hs.bytesReceived,
		InventoryVersion: hs.inv.Version,
		FramesRejected:   hs.framesRejected,
		AllowedActions:   slices.Clone(hs.allowed),
	}
	if hs.everSeen {
		st.LastSeenMs = hs.lastSeen.UnixMilli()
	}
	if hs.conn != nil {
		st.ConnectedSinceMs = hs.connectedSince.UnixMilli()
	}
	if hs.stats != nil {
		st.Stats = proto.Clone(hs.stats).(*asterismv1.AgentStats)
	}
	return st
}

// allowFrameLocked is a token bucket of FramesPerSec with FrameBurst.
func (hs *hostState) allowFrameLocked(now time.Time) bool {
	o := hs.reg.opts
	if !hs.lastFill.IsZero() {
		hs.tokens = math.Min(o.FrameBurst, hs.tokens+now.Sub(hs.lastFill).Seconds()*o.FramesPerSec)
	}
	hs.lastFill = now
	if hs.tokens < 1 {
		return false
	}
	hs.tokens--
	return true
}

// detachLocked ends c if it is the attached connection: pending commands on
// it fail with "result unknown" (their waiters observe c.done).
func (hs *hostState) detachLocked(c *conn, err error) {
	if hs.conn != c || c == nil {
		return
	}
	hs.conn = nil
	if err != nil {
		hs.lastError = err.Error()
	}
}

// attach validates a Hello and makes a new connection the host's current
// one. It returns the HelloAck to send.
func (r *Registry) attach(host string, f *asterismv1.AgentFrame, transport, remote string) (*hostState, *conn, *asterismv1.HelloAck, error) {
	hs, ok := r.hosts[host]
	if !ok {
		return nil, nil, nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("agent token for %q is not enrolled on this hub", host))
	}
	h := f.GetHello()
	if h == nil {
		return nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the first frame of a connection must be hello"))
	}
	if h.GetProtocolVersion() != ProtocolVersion {
		return nil, nil, nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("agent speaks uplink protocol %d; this hub speaks %d: upgrade the older side", h.GetProtocolVersion(), ProtocolVersion))
	}
	if h.GetSessionId() == "" || len(h.GetSessionId()) > 128 {
		return nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("hello.session_id must be 1-128 characters"))
	}
	if name := h.GetHostName(); name != "" && name != host {
		return nil, nil, nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"this token is bound to host %q but the agent reports host.name %q: set host.name: %s in agent.yaml or create a token for %q", host, name, host, name))
	}
	if pin := strings.ToLower(strings.TrimSpace(hs.agent.MachineID)); pin != "" && !strings.EqualFold(pin, h.GetMachineIdHash()) {
		return nil, nil, nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"machine_id_hash %q does not match the machine_id pinned for agent %q: the token is being used on another machine", h.GetMachineIdHash(), host))
	}

	now := r.now()
	hs.mu.Lock()
	r.connSeq++
	c := newConn(r.connSeq, transport, h.GetSessionId())
	old := hs.conn
	if old != nil {
		hs.detachLocked(old, nil)
	}
	known := uint64(0)
	if h.GetSessionId() == hs.sessionID {
		known = hs.inv.Version
	} else {
		// A new agent process: its seq numbers, series ids and inventory
		// versions start over.
		hs.sessionID = h.GetSessionId()
		hs.inv = state.NewInventory()
		hs.dec = state.NewDecoder()
		hs.lastSeq = 0
		hs.seriesCapped = false
	}
	hs.skewMs, hs.skewAdjMs = 0, 0
	if sent := f.GetSentAtMs(); sent > 0 {
		hs.skewMs = now.UnixMilli() - sent
		if time.Duration(math.Abs(float64(hs.skewMs)))*time.Millisecond > ClockSkewThreshold {
			hs.skewAdjMs = hs.skewMs
		}
	}
	wasOnline := hs.online
	hs.conn = c
	hs.everSeen, hs.online = true, true
	hs.lastSeen, hs.connectedSince = now, now
	hs.agentVersion = h.GetAgentVersion()
	hs.transport = transport
	hs.remoteAddr = remote
	hs.caps = slices.Clone(h.GetCapabilities())
	hs.configHash = h.GetConfigHash()
	hs.allowed = normalizeActions(h.GetAllowedActions())
	hs.lastError = ""
	if hs.skewAdjMs != 0 {
		hs.lastError = fmt.Sprintf("agent clock differs from the hub by %s; timestamps are rewritten to hub time (check NTP on the agent host)",
			(time.Duration(hs.skewMs) * time.Millisecond).Round(time.Millisecond))
	}
	machineObserved := !hs.machineLogged && hs.agent.MachineID == "" && h.GetMachineIdHash() != ""
	hs.machineLogged = hs.machineLogged || machineObserved
	hs.view.setHello(h)
	skew := hs.skewMs
	hs.mu.Unlock()

	if old != nil {
		old.close(errSuperseded)
	}
	attrs := []any{"host", host, "transport", transport, "agent_version", h.GetAgentVersion(), "remote", remote,
		"known_inventory_version", known, "session", h.GetSessionId()}
	if machineObserved {
		attrs = append(attrs, "machine_id_hash", h.GetMachineIdHash(), "hint", "set machine_id under this [[agents]] entry to pin the token to this machine")
	}
	r.log.Info("agent connected", attrs...)
	if d := time.Duration(skew) * time.Millisecond; d > ClockSkewThreshold || d < -ClockSkewThreshold {
		r.log.Warn("agent clock skew; rewriting agent timestamps to hub time", "host", host, "skew", d.Round(time.Millisecond))
	}
	if !wasOnline {
		r.emit(&asterismv1.Event{
			Kind: "agent.online", Severity: "info",
			EntityId: "agent:" + host, EntityName: host,
			Message: fmt.Sprintf("Agent %s connected (%s, %s transport)", host, orUnknown(h.GetAgentVersion()), transport),
			Attrs:   map[string]string{"host_id": host, "transport": transport, "agent_version": h.GetAgentVersion()},
		})
	}
	ack := &asterismv1.HelloAck{
		HostId:                host,
		HubVersion:            r.opts.HubVersion,
		ServerTimeMs:          now.UnixMilli(),
		KnownInventoryVersion: known,
		Hints:                 r.Hints(),
	}
	return hs, c, ack, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown version"
	}
	return s
}

// detach ends c (if still attached) and wakes everything waiting on it.
func (r *Registry) detach(hs *hostState, c *conn, err error) {
	hs.mu.Lock()
	hs.detachLocked(c, err)
	hs.mu.Unlock()
	if err == nil {
		err = errors.New("connection closed")
	}
	c.close(err)
}

// ingest processes one non-hello frame and returns hub frames to send back
// (acks, resync). An error ends the connection.
func (r *Registry) ingest(hs *hostState, c *conn, f *asterismv1.AgentFrame) ([]*asterismv1.HubFrame, error) {
	now := r.now()
	size := proto.Size(f)

	hs.mu.Lock()
	if hs.conn != c {
		hs.mu.Unlock()
		return nil, connect.NewError(connect.CodeAborted, errSuperseded)
	}
	hs.lastSeen = now
	hs.bytesReceived += uint64(size)
	if !hs.allowFrameLocked(now) {
		hs.framesRejected++
		hs.lastError = fmt.Sprintf("frame rate above %.0f/s; connection closed so the agent backs off and replays", r.opts.FramesPerSec)
		msg := hs.lastError
		hs.mu.Unlock()
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New(msg))
	}
	if f.GetHello() != nil {
		hs.mu.Unlock()
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("hello is only valid as the first frame of a connection"))
	}
	if f.GetSentAtMs() > 0 {
		hs.lagMs = max(0, now.UnixMilli()-(f.GetSentAtMs()+hs.skewAdjMs))
	}

	var replies []*asterismv1.HubFrame
	seq := f.GetSeq()
	if size > r.opts.MaxFrameBytes {
		hs.framesRejected++
		hs.lastError = fmt.Sprintf("dropped a %d-byte frame (limit %d bytes)", size, r.opts.MaxFrameBytes)
		if seq == 0 {
			msg := hs.lastError
			hs.mu.Unlock()
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New(msg))
		}
		// A replayable frame is skipped and acked so it is not replayed forever.
		hs.lastSeq = max(hs.lastSeq, seq)
		replies = append(replies, hs.ackLocked(c, true)...)
		hs.mu.Unlock()
		return replies, nil
	}
	if seq > 0 {
		if seq <= hs.lastSeq {
			hs.duplicates++
			hs.mu.Unlock()
			return nil, nil
		}
		hs.lastSeq = seq
		c.unacked++
	}

	var samples []bus.MetricSample
	var events []*asterismv1.Event
	var err error
	switch p := f.GetPayload().(type) {
	case *asterismv1.AgentFrame_Metrics:
		samples, err = hs.applyMetricsLocked(p.Metrics)
	case *asterismv1.AgentFrame_Snapshot:
		hs.inv.ApplySnapshot(p.Snapshot)
		hs.view.rebuild(hs.inv, now)
	case *asterismv1.AgentFrame_Delta:
		if derr := hs.inv.ApplyDelta(p.Delta); derr != nil {
			if !errors.Is(derr, state.ErrVersionMismatch) {
				err = connect.NewError(connect.CodeInvalidArgument, derr)
				break
			}
			replies = append(replies, &asterismv1.HubFrame{Payload: &asterismv1.HubFrame_Resync{Resync: &asterismv1.ResyncRequest{Reason: derr.Error()}}})
		} else {
			hs.view.rebuild(hs.inv, now)
		}
	case *asterismv1.AgentFrame_Events:
		events = hs.view.addEvents(p.Events.GetEvents(), hs.skewAdjMs)
	case *asterismv1.AgentFrame_Heartbeat:
		if s := p.Heartbeat.GetStats(); s != nil {
			hs.stats = proto.Clone(s).(*asterismv1.AgentStats)
		}
	case *asterismv1.AgentFrame_CommandResult:
		res := p.CommandResult
		if pc, ok := hs.pending[res.GetRequestId()]; ok && pc.conn == c {
			delete(hs.pending, res.GetRequestId())
			pc.ch <- res
		}
	case *asterismv1.AgentFrame_IconData:
		d := p.IconData
		if pi, ok := hs.icons[d.GetRequestId()]; ok && pi.conn == c {
			delete(hs.icons, d.GetRequestId())
			pi.ch <- d
		}
	case *asterismv1.AgentFrame_LogChunk:
		ch := p.LogChunk
		if pl, ok := hs.logs[ch.GetRequestId()]; ok && pl.conn == c {
			select {
			case pl.ch <- ch:
			default: // the reader is gone or slow; lines are best-effort
			}
		}
	}
	if err != nil {
		hs.lastError = err.Error()
		hs.mu.Unlock()
		return nil, err
	}
	if seq > 0 {
		replies = append(replies, hs.ackLocked(c, false)...)
	}
	hs.mu.Unlock()

	if len(samples) > 0 || len(events) > 0 {
		r.bus.Publish(bus.Message{Samples: samples, Events: events})
	}
	return replies, nil
}

// ackLocked returns an Ack when AckFrames frames are unacked (or force).
func (hs *hostState) ackLocked(c *conn, force bool) []*asterismv1.HubFrame {
	if hs.lastSeq <= c.lastAcked || (!force && c.unacked < hs.reg.opts.AckFrames) {
		return nil
	}
	c.unacked = 0
	c.lastAcked = hs.lastSeq
	return []*asterismv1.HubFrame{{Payload: &asterismv1.HubFrame_Ack{Ack: &asterismv1.Ack{UptoSeq: hs.lastSeq}}}}
}

// pendingAck returns an Ack for everything processed but not yet acked (the
// periodic 5 s ack of the bidi transport).
func (r *Registry) pendingAck(hs *hostState, c *conn) *asterismv1.HubFrame {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if a := hs.ackLocked(c, true); len(a) > 0 {
		return a[0]
	}
	return nil
}

// applyMetricsLocked decodes metrics, enforcing the series cap, and returns
// the bus samples (agent series plus project rollups).
func (hs *hostState) applyMetricsLocked(m *asterismv1.AgentMetrics) ([]bus.MetricSample, error) {
	limit := hs.reg.opts.MaxSeries
	if room := limit - hs.dec.Len(); len(m.GetDict()) > 0 {
		var dict []*asterismv1.SeriesEntry
		var groups []uint32
		for i, e := range m.GetDict() {
			_, known := hs.dec.Lookup(e.GetSeriesId())
			if !known {
				if room <= 0 || strings.HasPrefix(e.GetEntityId(), "@") || e.GetEntityId() == "" || e.GetMetric() == "" {
					if room <= 0 && !hs.seriesCapped {
						hs.seriesCapped = true
						hs.lastError = fmt.Sprintf("series cap of %d reached; further series from this agent are ignored", limit)
						hs.reg.log.Warn("agent series cap reached", "host", hs.agent.Name, "limit", limit)
					}
					continue
				}
				room--
			}
			dict = append(dict, e)
			if i < len(m.GetDictGroups()) {
				groups = append(groups, m.GetDictGroups()[i])
			}
		}
		if len(dict) != len(m.GetDict()) && len(m.GetDictGroups()) == len(m.GetDict()) {
			m = &asterismv1.AgentMetrics{Dict: dict, DictGroups: groups, Rows: m.GetRows(), RowGroups: m.GetRowGroups(), KeyframeRows: m.GetKeyframeRows()}
		}
	}
	var pts []state.Point
	if err := hs.dec.Apply(m, func(p state.Point) { pts = append(pts, p) }); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if len(pts) == 0 {
		return nil, nil
	}
	for i := range pts {
		pts[i].TsMs += hs.skewAdjMs
	}
	samples := make([]bus.MetricSample, 0, len(pts)+8)
	for _, p := range pts {
		v := float64(p.Value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		samples = append(samples, bus.MetricSample{EntityID: hs.qualify(p.Key.Entity), Metric: p.Key.Metric, TSMs: p.TsMs, Value: v})
	}
	for _, s := range hs.view.applyMetrics(pts) {
		s.EntityID = hs.qualify(s.EntityID)
		samples = append(samples, s)
	}
	return samples, nil
}

func normalizeActions(in []asterismv1.ContainerAction) []asterismv1.ContainerAction {
	out := make([]asterismv1.ContainerAction, 0, len(in))
	for _, a := range in {
		if a != asterismv1.ContainerAction_CONTAINER_ACTION_UNSPECIFIED && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}
