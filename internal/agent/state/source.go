// Package state holds the agent's current view of the host: interned metric
// series with carry-forward encoding, and a versioned inventory that is sent
// as one snapshot per connection followed by deltas.
//
// Collectors (Sources) write through a Sink. The scheduler gives every
// Source its own Sink handle, so metric rows are grouped per collector tick.
package state

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// SeriesID identifies an interned (entity, metric) series. IDs are stable for
// the lifetime of the agent process. 0 is never a valid ID.
type SeriesID uint32

// InvalidSeries is returned when the series cap is reached; Set ignores it.
const InvalidSeries SeriesID = 0

// Sink receives a collector's output. It is implemented by the per-source
// handles that Store.Sink returns.
//
// Metric values set during one Tick form one row. A series of this sink that
// is not Set during a successful Tick is considered gone (sent as NaN once);
// setting it again later revives it. Set(NaN) is the same as not setting it.
//
// Inventory methods may be called from any goroutine. Series and Set are for
// the goroutine that runs Tick only (Run goroutines must not call Set).
type Sink interface {
	Series(entity, metric string) SeriesID
	Set(id SeriesID, v float32)

	UpsertContainer(c *v1.Container)
	RemoveContainer(id string)
	UpsertWorkload(w *v1.Workload)
	RemoveWorkload(id string)
	ReplaceListeners(l []*v1.PortBinding)
	ReplaceRoutes(r []*v1.IngressRoute)
	ReplaceDockerResources(r DockerResources)
	Emit(e *v1.Event)

	// SetHostInfo replaces the static host facts (os, kernel, cpu model,
	// totals). Fast-changing values belong in metric series, not here.
	SetHostInfo(h *v1.HostInfo)
	// ReplaceFilesystems replaces the statfs results.
	ReplaceFilesystems(f []*v1.FilesystemUsage)
}

// DockerResources replaces Docker engine-level sets. A nil pointer or a false
// *Set flag leaves that part unchanged, so collectors on different cadences
// (resources every 5 m, disk usage every 1 h) can update independently.
type DockerResources struct {
	Engine      *v1.DockerEngine
	Images      []*v1.DockerImage
	ImagesSet   bool
	Volumes     []*v1.DockerVolume
	VolumesSet  bool
	Networks    []*v1.DockerNetwork
	NetworksSet bool
	DiskUsage   *v1.DockerDiskUsage
}

// Source is one collector.
type Source interface {
	// Name is a short stable identifier ("host", "cgroup", "docker", ...),
	// used in logs and in AgentStats.collector_errors.
	Name() string
	// Init probes the environment once and returns the capabilities it
	// contributes to Hello (e.g. "docker", "cgroup_v2"). An error disables
	// the source for this process; it is logged and counted.
	Init(ctx context.Context, env Env) (caps []string, err error)
	// Interval is the base tick interval; 0 means the source is purely
	// event-driven and must implement Runner.
	Interval() time.Duration
	// Tick samples once. It is called on the scheduler goroutine and must
	// not block (no network waits, bounded file reads only).
	Tick(now time.Time, s Sink) error
}

// Runner is implemented by event-driven sources (for example Docker events).
// The scheduler starts Run on its own goroutine after Init and cancels ctx on
// shutdown. Run should update inventory and emit events only.
type Runner interface {
	Run(ctx context.Context, s Sink) error
}

// Class tells the scheduler which hub hint applies to a source's interval.
type Class int

// Source classes.
const (
	ClassOther      Class = iota // idle stretching only
	ClassHost                    // AgentHints.host_interval_ms
	ClassContainers              // AgentHints.container_interval_ms and focus
)

// Classed is optionally implemented by sources that follow hub hints.
type Classed interface {
	Class() Class
}

// HintAware is optionally implemented by sources that want the hub's hints,
// e.g. to sample focused containers more often. Called on the scheduler
// goroutine between ticks.
type HintAware interface {
	ApplyHints(h Hints)
}

// Hints are the hub's sampling hints after local clamping.
type Hints struct {
	LiveViewers       bool
	HostInterval      time.Duration // 0 = no hint
	ContainerInterval time.Duration // 0 = no hint
	FlushInterval     time.Duration // 0 = no hint
	Focus             []string      // focused entity ids
}

// HintsFromProto converts hub hints (nil means no viewers, no hints).
func HintsFromProto(h *v1.AgentHints) Hints {
	if h == nil {
		return Hints{}
	}
	ms := func(v uint32) time.Duration { return time.Duration(v) * time.Millisecond }
	return Hints{
		LiveViewers:       h.GetLiveViewers(),
		HostInterval:      ms(h.GetHostIntervalMs()),
		ContainerInterval: ms(h.GetContainerIntervalMs()),
		FlushInterval:     ms(h.GetFlushIntervalMs()),
		Focus:             append([]string(nil), h.GetFocusEntityIds()...),
	}
}

// DockerAPI is the shared Docker Engine API client handed to sources. The
// concrete client (internal/agent/dockerapi) has many more methods; sources
// that need them type-assert to the concrete type or a narrower interface.
type DockerAPI interface {
	Ping(ctx context.Context) error
}

// Env is what a Source receives at Init.
type Env struct {
	// Config is the effective agent configuration (read-only).
	Config *config.Config
	// Logger is scoped to the source ("source" attribute).
	Logger *slog.Logger
	// Root is the host root prefix ("/" or "/host" in a container).
	Root string
	// Docker is nil when the Docker source is disabled or unavailable.
	Docker DockerAPI
	// Now returns the current time (fake in tests).
	Now func() time.Time
}

// HostPath prefixes an absolute host path with Root, e.g. "/proc/stat" ->
// "/host/proc/stat".
func (e Env) HostPath(p string) string {
	if e.Root == "" || e.Root == "/" {
		return p
	}
	return filepath.Join(e.Root, p)
}
