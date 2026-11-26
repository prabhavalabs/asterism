// Package docker is the agent's Docker (or Docker-compatible Podman)
// inventory source. It is event-driven: GET /events triggers one inspect of
// the changed container; GET /containers/json runs as a safety net every
// intervals.inventory_resync; images, volumes and networks refresh every
// intervals.docker_resources (sent only when their content hash changes);
// system df runs every intervals.docker_disk_usage (0 disables it).
//
// Containers are converted with dockercli.ContainerFromEngine, which
// reduces Config.Env to names while decoding; labels, command and
// healthcheck output are redacted (built-in rules plus redaction.*), and
// include/exclude and group_by are applied before anything reaches the
// store. Docker's /stats endpoint is never called: metrics come from the
// cgroup source, which reads the targets published here.
package docker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/agent/collect/redact"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Options configures a Source.
type Options struct {
	// Name is "docker" or "podman".
	Name     string
	Docker   config.Docker
	Interval config.Intervals
	Logs     config.Logs
	Client   *dockerapi.Client
	Registry *targets.Registry
	Redactor *redact.Redactor
	// After replaces time.After (tests).
	After func(time.Duration) <-chan time.Time
}

// Source is the Docker inventory collector.
type Source struct {
	opts Options
	log  *slog.Logger
	now  func() time.Time

	mu       sync.Mutex
	sink     state.Sink
	byID     map[string]*v1.Container // full id -> converted container (post-filter)
	tty      map[string]bool
	resHash  [32]byte
	engHash  [32]byte
	volSizes map[string]int64
	down     bool
	ready    chan struct{}

	// handled, when set (tests), is called after each event is processed.
	handled func()
}

// New returns a Docker source.
func New(o Options) *Source {
	if o.Name == "" {
		o.Name = "docker"
	}
	if o.After == nil {
		o.After = time.After
	}
	return &Source{opts: o, byID: map[string]*v1.Container{}, tty: map[string]bool{}, ready: make(chan struct{})}
}

// Name implements state.Source.
func (s *Source) Name() string { return s.opts.Name }

// Interval implements state.Source: 0, the source is event-driven.
func (s *Source) Interval() time.Duration { return 0 }

// Tick implements state.Source (unused: Interval is 0).
func (s *Source) Tick(time.Time, state.Sink) error { return nil }

// Client returns the Engine API client.
func (s *Source) Client() *dockerapi.Client { return s.opts.Client }

// Init pings the engine.
func (s *Source) Init(ctx context.Context, env state.Env) ([]string, error) {
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.now = env.Now
	if s.now == nil {
		s.now = time.Now
	}
	if s.opts.Client == nil {
		return nil, fmt.Errorf("%s: no client", s.opts.Name)
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.opts.Client.Ping(pctx); err != nil {
		return nil, fmt.Errorf("%s engine at %s did not answer /_ping: %w", s.opts.Name, s.opts.Client.Endpoint(), err)
	}
	caps := []string{s.opts.Name}
	if s.opts.Logs.Docker {
		caps = append(caps, "logs")
	}
	return caps, nil
}

// Run implements state.Runner.
func (s *Source) Run(ctx context.Context, sink state.Sink) error {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
	if err := s.Resync(ctx); err != nil {
		s.log.Warn("initial container sync failed", "err", err)
	}
	s.RefreshResources(ctx)
	close(s.ready)

	loop := &dockerapi.EventLoop{
		Client: s.opts.Client,
		OnEvent: func(raw []byte) {
			s.HandleEvent(ctx, raw)
			if s.handled != nil {
				s.handled()
			}
		},
		OnReconnect: func(ctx context.Context) { _ = s.Resync(ctx) },
		OnError: func(err error, attempt int) {
			if attempt == 1 || attempt%10 == 0 {
				s.log.Warn("docker event stream dropped; reconnecting", "err", err, "attempt", attempt)
			}
		},
		After: s.opts.After,
		Now:   s.now,
	}
	var wg sync.WaitGroup
	wg.Go(func() { _ = loop.Run(ctx) })
	defer wg.Wait()

	iv := s.opts.Interval
	resync := s.opts.After(orDefault(iv.InventoryResync.D(), 5*time.Minute))
	res := s.opts.After(orDefault(iv.DockerResources.D(), 5*time.Minute))
	var df <-chan time.Time
	if iv.DockerDiskUsage.D() > 0 {
		// The first `system df` waits a minute so start-up stays cheap.
		df = s.opts.After(min(time.Minute, iv.DockerDiskUsage.D()))
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-resync:
			if err := s.Resync(ctx); err != nil {
				s.log.Warn("container resync failed", "err", err)
			}
			resync = s.opts.After(orDefault(iv.InventoryResync.D(), 5*time.Minute))
		case <-res:
			s.RefreshResources(ctx)
			res = s.opts.After(orDefault(iv.DockerResources.D(), 5*time.Minute))
		case <-df:
			s.RefreshDiskUsage(ctx)
			df = s.opts.After(iv.DockerDiskUsage.D())
		}
	}
}

// Ready is closed after the initial sync.
func (s *Source) Ready() <-chan struct{} { return s.ready }

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// HandleEvent processes one raw /events message.
func (s *Source) HandleEvent(ctx context.Context, raw []byte) {
	ev, err := dockercli.ParseEngineEvent(raw)
	if err != nil || ev.Type != "container" || !dockercli.ValidContainerID(ev.ID) {
		return
	}
	switch a := ev.BaseAction(); a {
	case "destroy":
		s.remove(ev.ID)
	case "exec_create", "exec_start", "exec_die", "attach", "detach", "top", "resize", "export", "copy", "archive-path", "extract-to-dir", "commit":
		return
	default:
		s.refresh(ctx, ev.ID)
	}
}

// refresh inspects one container and applies it (404 removes it).
func (s *Source) refresh(ctx context.Context, id string) {
	raw, err := s.opts.Client.InspectContainer(ctx, id)
	if dockerapi.IsStatus(err, http.StatusNotFound) {
		s.remove(id)
		return
	}
	if err != nil {
		s.log.Debug("inspect failed", "id", dockercli.ShortID(id), "err", err)
		return
	}
	ec, err := dockercli.ContainerFromEngine(raw, s.now())
	if err != nil {
		s.log.Debug("decode container failed", "id", dockercli.ShortID(id), "err", err)
		return
	}
	s.apply(ec)
}

// Excluded reports whether a container is filtered out by include/exclude.
func Excluded(d config.Docker, name string, labels map[string]string) bool {
	if len(d.Include.Names) > 0 || len(d.Include.Labels) > 0 {
		if !matchSelector(d.Include, name, labels) {
			return true
		}
	}
	return matchSelector(d.Exclude, name, labels)
}

func matchSelector(sel config.Selector, name string, labels map[string]string) bool {
	for _, g := range sel.Names {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	for k, v := range sel.Labels {
		if got, ok := labels[k]; ok && (v == "" || v == got) {
			return true
		}
	}
	return false
}

// project applies sources.docker.group_by.
func project(groupBy string, c *v1.Container) {
	switch {
	case groupBy == "none":
		c.ProjectId = "proj:standalone"
	case len(groupBy) > len("label:") && groupBy[:len("label:")] == "label:":
		if v := c.Labels[groupBy[len("label:"):]]; v != "" {
			c.ProjectId = "proj:" + v
		} else {
			c.ProjectId = "proj:standalone"
		}
	}
}

func (s *Source) apply(ec dockercli.EngineContainer) {
	c := ec.Container
	if Excluded(s.opts.Docker, c.Name, c.Labels) {
		s.remove(ec.FullID)
		return
	}
	project(s.opts.Docker.GroupBy, c)
	s.opts.Redactor.Container(c)
	if s.opts.Name != "docker" {
		if c.Labels == nil {
			c.Labels = map[string]string{}
		}
		c.Labels["asterism.runtime"] = s.opts.Name
	}
	s.mu.Lock()
	old := s.byID[ec.FullID]
	s.byID[ec.FullID] = c
	s.tty[ec.FullID] = ec.Tty
	sink := s.sink
	s.mu.Unlock()
	if sink != nil {
		if old != nil && old.Id != c.Id {
			sink.RemoveContainer(old.Id) // renamed
		}
		sink.UpsertContainer(c)
	}
	if s.opts.Registry != nil {
		s.opts.Registry.SetContainer(target(ec.FullID, c))
	}
}

func target(fullID string, c *v1.Container) targets.Container {
	t := targets.Container{
		EntityID: c.Id,
		Name:     c.Name,
		FullID:   fullID,
		Pid:      c.Pid,
		Running:  c.State == v1.ContainerState_CONTAINER_STATE_RUNNING || c.State == v1.ContainerState_CONTAINER_STATE_PAUSED,
		OwnNetns: c.NetworkMode != "host" && !(len(c.NetworkMode) > 10 && c.NetworkMode[:10] == "container:"),
	}
	for _, p := range c.Ports {
		if p.Published && p.HostPort > 0 {
			if t.HostPorts == nil {
				t.HostPorts = map[uint32]uint32{}
			}
			t.HostPorts[p.HostPort] = p.ContainerPort
		}
	}
	return t
}

func (s *Source) remove(fullID string) {
	s.mu.Lock()
	old := s.byID[fullID]
	delete(s.byID, fullID)
	delete(s.tty, fullID)
	sink := s.sink
	s.mu.Unlock()
	if old != nil && sink != nil {
		sink.RemoveContainer(old.Id)
	}
	if s.opts.Registry != nil {
		s.opts.Registry.RemoveContainer(fullID)
	}
}

// Resync lists all containers and inspects new or changed ones; containers
// that disappeared are removed. It covers events missed while the stream
// was down.
func (s *Source) Resync(ctx context.Context) error {
	raw, err := s.opts.Client.ListContainers(ctx)
	if err != nil {
		s.markDown(err)
		return err
	}
	list, err := dockercli.ParseEngineContainerList(raw)
	if err != nil {
		return err
	}
	s.markUp()
	seen := make(map[string]bool, len(list))
	for _, sum := range list {
		seen[sum.ID] = true
		if sum.Name != "" && Excluded(s.opts.Docker, sum.Name, sum.Labels) {
			s.remove(sum.ID)
			continue
		}
		s.mu.Lock()
		known := s.byID[sum.ID]
		s.mu.Unlock()
		if known != nil && known.State == sum.State && known.Name == sum.Name && healthOf(sum.Status) == known.Health {
			continue
		}
		s.refresh(ctx, sum.ID)
	}
	s.mu.Lock()
	var gone []string
	for id := range s.byID {
		if !seen[id] {
			gone = append(gone, id)
		}
	}
	s.mu.Unlock()
	for _, id := range gone {
		s.remove(id)
	}
	return nil
}

// healthOf derives the health from a `docker ps` status string.
func healthOf(status string) v1.ContainerHealth {
	switch {
	case contains(status, "(healthy)"):
		return v1.ContainerHealth_CONTAINER_HEALTH_HEALTHY
	case contains(status, "(unhealthy)"):
		return v1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY
	case contains(status, "(health: starting)"):
		return v1.ContainerHealth_CONTAINER_HEALTH_STARTING
	default:
		return v1.ContainerHealth_CONTAINER_HEALTH_NONE
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Containers returns copies of the current containers sorted by name.
func (s *Source) Containers() []*v1.Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*v1.Container, 0, len(s.byID))
	for _, c := range s.byID {
		out = append(out, proto.Clone(c).(*v1.Container))
	}
	sortContainers(out)
	return out
}

func sortContainers(cs []*v1.Container) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].Name < cs[j-1].Name; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

func (s *Source) markDown(err error) {
	s.mu.Lock()
	wasDown := s.down
	s.down = true
	sink := s.sink
	s.mu.Unlock()
	if !wasDown && sink != nil {
		sink.ReplaceDockerResources(state.DockerResources{Engine: &v1.DockerEngine{
			Available:         false,
			UnavailableReason: fmt.Sprintf("%s engine unreachable: %v", s.opts.Name, err),
			CollectedAtMs:     s.now().UnixMilli(),
		}})
		s.mu.Lock()
		s.engHash = [32]byte{}
		s.mu.Unlock()
	}
}

func (s *Source) markUp() {
	s.mu.Lock()
	s.down = false
	s.mu.Unlock()
}

// RefreshResources collects engine info, images, volumes and networks and
// sends them when their content changed.
func (s *Source) RefreshResources(ctx context.Context) {
	now := s.now()
	c := s.opts.Client
	var res state.DockerResources
	if raw, err := c.Info(ctx); err == nil {
		if eng, err := dockercli.EngineInfoFromJSON(raw, now); err == nil {
			res.Engine = eng
		}
	} else {
		s.markDown(err)
		return
	}
	imgRaw, err1 := c.Images(ctx)
	volRaw, err2 := c.Volumes(ctx)
	netRaw, err3 := c.Networks(ctx)
	var images []*v1.DockerImage
	var volumes []*v1.DockerVolume
	var networks []*v1.DockerNetwork
	if err1 == nil {
		images, err1 = dockercli.ImagesFromEngine(imgRaw)
	}
	if err2 == nil {
		volumes, err2 = dockercli.VolumesFromEngine(volRaw)
	}
	if err3 == nil {
		networks, err3 = dockercli.NetworksFromEngine(netRaw)
	}
	containers := s.Containers()
	s.mu.Lock()
	sizes := s.volSizes
	s.mu.Unlock()
	dockercli.LinkUsage(containers, images, volumes)
	dockercli.LinkNetworks(containers, networks)
	dockercli.ApplyVolumeSizes(volumes, sizes)
	res.Images, res.ImagesSet = images, err1 == nil
	res.Volumes, res.VolumesSet = volumes, err2 == nil
	res.Networks, res.NetworksSet = networks, err3 == nil

	h := hashOf(msgs(images), msgs(volumes), msgs(networks))
	var eh [32]byte
	if res.Engine != nil {
		e := proto.Clone(res.Engine).(*v1.DockerEngine)
		e.CollectedAtMs = 0
		eh = hashOf([]proto.Message{e})
	}
	s.mu.Lock()
	send := s.sink != nil && (h != s.resHash || eh != s.engHash)
	if send {
		s.resHash, s.engHash = h, eh
	}
	sink := s.sink
	s.mu.Unlock()
	if send {
		sink.ReplaceDockerResources(res)
	}
}

// RefreshDiskUsage runs system df and re-sends volumes with their sizes.
func (s *Source) RefreshDiskUsage(ctx context.Context) {
	raw, err := s.opts.Client.SystemDF(ctx)
	if err != nil {
		s.log.Debug("system df failed", "err", err)
		return
	}
	du, sizes, err := dockercli.DiskUsageFromEngine(raw, s.now())
	if err != nil {
		return
	}
	s.mu.Lock()
	s.volSizes = sizes
	sink := s.sink
	s.mu.Unlock()
	if sink != nil {
		sink.ReplaceDockerResources(state.DockerResources{DiskUsage: du})
	}
	s.RefreshResources(ctx) // volume sizes changed
}

func msgs[T proto.Message](l []T) []proto.Message {
	out := make([]proto.Message, len(l))
	for i, m := range l {
		out[i] = m
	}
	return out
}

func hashOf(lists ...[]proto.Message) [32]byte {
	h := sha256.New()
	mo := proto.MarshalOptions{Deterministic: true}
	for _, l := range lists {
		for _, m := range l {
			b, _ := mo.Marshal(m)
			_, _ = h.Write(b)
		}
		_, _ = h.Write([]byte{0})
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// SyncOnce runs one full inventory pass (list, inspect, resources, and
// system df when enabled) into sink without starting the event loop; the
// discover command uses it.
func (s *Source) SyncOnce(ctx context.Context, sink state.Sink, diskUsage bool) error {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
	err := s.Resync(ctx)
	s.RefreshResources(ctx)
	if diskUsage {
		s.RefreshDiskUsage(ctx)
	}
	return err
}
