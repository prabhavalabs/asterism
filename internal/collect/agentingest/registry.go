// Package agentingest is the hub side of the asterism-agent uplink.
//
// Agents dial the hub and push AgentFrames over a bidi stream or unary Push
// plus a Commands server stream (docs/BLUEPRINT.md decisions D22-D25). Both
// transports feed the same per-host session code in this package:
//
//   - The agent token binds a connection to one enrolled host (host_id).
//   - Metrics are decoded with the agent's reference decoder and published on
//     the bus under the same entity ids and metric names the SSH bridge uses,
//     so the UI charts work unchanged. The primary host keeps unqualified ids
//     ("ctr:shop-api-1"); other hosts are stored as "@<host>/ctr:shop-api-1".
//   - Inventory snapshots and deltas are applied to a versioned copy and
//     assembled with internal/collect/assemble into a HostView, which
//     implements the service's HostProvider, InventoryProvider,
//     DockerProvider, and ContainerActuator interfaces.
//   - Container actions travel to the agent as CommandRequests and are
//     audited on both sides.
package agentingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Per-agent caps (docs/BLUEPRINT.md §13 threat model).
const (
	DefaultMaxFrameBytes = 1 << 20
	DefaultFramesPerSec  = 50
	// DefaultFrameBurst lets a reconnecting agent replay its buffer: replay is
	// paced at 64 KiB/s by the agent, so bursts are short.
	DefaultFrameBurst = 500
	DefaultMaxSeries  = 5000
	// ClockSkewThreshold is the clock offset above which agent timestamps are
	// rewritten to hub time.
	ClockSkewThreshold = 2 * time.Second
	// ProtocolVersion is the uplink protocol this hub speaks.
	ProtocolVersion = 1
)

// Agent is one enrolled agent ([[agents]] in asterism.toml).
type Agent struct {
	Name string
	// MachineID optionally pins Hello.machine_id_hash.
	MachineID string
	// AllowActions is the hub-side per-agent action gate.
	AllowActions bool
}

// Options configures a Registry.
type Options struct {
	Agents      []Agent
	PrimaryHost string
	// StaleAfter is the silence after which an agent is offline (30s).
	StaleAfter time.Duration
	Bus        *bus.Bus
	Logger     *slog.Logger
	HubVersion string
	// Now is the clock (tests use a fake one).
	Now func() time.Time

	MaxFrameBytes  int
	FramesPerSec   float64
	FrameBurst     float64
	MaxSeries      int
	AckEvery       time.Duration // 5s
	AckFrames      int           // 32
	HandshakeAfter time.Duration // 15s: first frame must arrive by then
	LogTimeout     time.Duration // 5s: one-shot log request
}

// Registry holds every enrolled host's session state.
type Registry struct {
	opts  Options
	log   *slog.Logger
	bus   *bus.Bus
	now   func() time.Time
	hosts map[string]*hostState
	order []string

	evMu   sync.Mutex
	events []*asterismv1.Event // agent.* lifecycle events, newest last

	hintMu  sync.Mutex
	viewers int
	hints   *asterismv1.AgentHints

	connSeq uint64
}

const maxRegistryEvents = 200

// New builds a registry. The primary host is opts.PrimaryHost, or the first
// enrolled agent.
func New(opts Options) (*Registry, error) {
	if len(opts.Agents) == 0 {
		return nil, fmt.Errorf("agent ingest needs at least one enrolled agent")
	}
	if opts.Bus == nil {
		opts.Bus = bus.New(64)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.StaleAfter, 30*time.Second)
	def(&opts.AckEvery, 5*time.Second)
	def(&opts.HandshakeAfter, 15*time.Second)
	def(&opts.LogTimeout, 5*time.Second)
	if opts.MaxFrameBytes <= 0 {
		opts.MaxFrameBytes = DefaultMaxFrameBytes
	}
	if opts.FramesPerSec <= 0 {
		opts.FramesPerSec = DefaultFramesPerSec
	}
	if opts.FrameBurst <= 0 {
		opts.FrameBurst = DefaultFrameBurst
	}
	if opts.MaxSeries <= 0 {
		opts.MaxSeries = DefaultMaxSeries
	}
	if opts.AckFrames <= 0 {
		opts.AckFrames = 32
	}
	if opts.HubVersion == "" {
		opts.HubVersion = "dev"
	}
	if opts.PrimaryHost == "" {
		opts.PrimaryHost = opts.Agents[0].Name
	}
	r := &Registry{opts: opts, log: opts.Logger, bus: opts.Bus, now: opts.Now, hosts: map[string]*hostState{}}
	for _, a := range opts.Agents {
		if _, dup := r.hosts[a.Name]; dup {
			return nil, fmt.Errorf("agent %q enrolled twice", a.Name)
		}
		hs := newHostState(r, a, a.Name == opts.PrimaryHost)
		r.hosts[a.Name] = hs
		r.order = append(r.order, a.Name)
	}
	if _, ok := r.hosts[opts.PrimaryHost]; !ok {
		return nil, fmt.Errorf("primary host %q is not an enrolled agent", opts.PrimaryHost)
	}
	r.hints = hintsFor(0)
	return r, nil
}

// Primary returns the view of the primary host. It exists before the agent
// first connects, so it can be wired into the service at startup.
func (r *Registry) Primary() *HostView { return r.hosts[r.opts.PrimaryHost].view }

// PrimaryHost returns the primary host id.
func (r *Registry) PrimaryHost() string { return r.opts.PrimaryHost }

// View returns the view of any enrolled host.
func (r *Registry) View(host string) (*HostView, bool) {
	hs, ok := r.hosts[host]
	if !ok {
		return nil, false
	}
	return hs.view, true
}

// Run sweeps for silent agents until ctx ends.
func (r *Registry) Run(ctx context.Context) error {
	t := time.NewTicker(max(r.opts.StaleAfter/6, time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			r.Sweep()
		}
	}
}

// Sweep marks agents that have been silent for stale_after as offline,
// closing a connection that went quiet, and raises agent.offline once.
func (r *Registry) Sweep() {
	now := r.now()
	for _, name := range r.order {
		hs := r.hosts[name]
		hs.mu.Lock()
		silent := hs.everSeen && now.Sub(hs.lastSeen) >= r.opts.StaleAfter
		var hung *conn
		if silent && hs.conn != nil {
			hung = hs.conn
			hs.detachLocked(hung, fmt.Errorf("no frames for %s", r.opts.StaleAfter))
		}
		goOffline := silent && hs.online
		if goOffline {
			hs.online = false
		}
		last := hs.lastSeen
		hs.mu.Unlock()
		if hung != nil {
			hung.close(errStale)
		}
		if goOffline {
			r.log.Warn("agent offline", "host", name, "last_seen", last.Format(time.RFC3339))
			r.emit(&asterismv1.Event{
				Kind:     "agent.offline",
				Severity: "warning",
				EntityId: "agent:" + name, EntityName: name,
				Message: fmt.Sprintf("Agent %s is offline: no data for %s (last seen %s)", name, now.Sub(last).Round(time.Second), last.Format(time.RFC3339)),
				Attrs:   map[string]string{"host_id": name},
			})
		}
	}
}

// SetLiveViewers updates the sampling hints from the LiveService subscriber
// count and pushes them to every connected agent when they change.
func (r *Registry) SetLiveViewers(n int) {
	r.hintMu.Lock()
	prev := r.viewers > 0
	r.viewers = n
	changed := prev != (n > 0)
	if changed {
		r.hints = hintsFor(n)
	}
	h := r.hints
	r.hintMu.Unlock()
	if !changed {
		return
	}
	for _, name := range r.order {
		hs := r.hosts[name]
		hs.mu.Lock()
		c := hs.conn
		hs.mu.Unlock()
		if c != nil {
			c.trySend(&asterismv1.HubFrame{Payload: &asterismv1.HubFrame_Hints{Hints: proto.Clone(h).(*asterismv1.AgentHints)}})
		}
	}
}

// Hints returns the current sampling hints.
func (r *Registry) Hints() *asterismv1.AgentHints {
	r.hintMu.Lock()
	defer r.hintMu.Unlock()
	return proto.Clone(r.hints).(*asterismv1.AgentHints)
}

func hintsFor(viewers int) *asterismv1.AgentHints {
	if viewers <= 0 {
		return &asterismv1.AgentHints{}
	}
	return &asterismv1.AgentHints{LiveViewers: true, HostIntervalMs: 2000, ContainerIntervalMs: 5000, FlushIntervalMs: 2000}
}

// emit records an agent lifecycle event and publishes it.
func (r *Registry) emit(e *asterismv1.Event) {
	now := r.now()
	e.TsMs = now.UnixMilli()
	if e.Id == "" {
		e.Id = fmt.Sprintf("evt:%s:%s:%d-%s", e.Kind, e.EntityName, e.TsMs, randID(4))
	}
	r.evMu.Lock()
	r.events = append(r.events, e)
	if len(r.events) > maxRegistryEvents {
		r.events = slices.Delete(r.events, 0, len(r.events)-maxRegistryEvents)
	}
	r.evMu.Unlock()
	r.bus.Publish(bus.Message{Events: []*asterismv1.Event{proto.Clone(e).(*asterismv1.Event)}})
}

func (r *Registry) lifecycleEvents() []*asterismv1.Event {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	out := make([]*asterismv1.Event, len(r.events))
	for i, e := range r.events {
		out[i] = proto.Clone(e).(*asterismv1.Event)
	}
	return out
}

// ListAgents returns every enrolled agent's status in configuration order.
func (r *Registry) ListAgents() *asterismv1.ListAgentsResponse {
	now := r.now()
	resp := &asterismv1.ListAgentsResponse{PrimaryHostId: r.opts.PrimaryHost}
	for _, name := range r.order {
		resp.Agents = append(resp.Agents, r.hosts[name].status(now))
	}
	return resp
}

func randID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// qualify returns the stored entity id for an agent entity.
func qualify(primary bool, host, entity string) string {
	if primary {
		return entity
	}
	return "@" + host + "/" + entity
}
