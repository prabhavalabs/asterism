// Package collecttest provides a recording state.Sink for collector tests.
package collecttest

import (
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Sink records everything a source writes. Values holds the last tick's
// metric values keyed by "entity|metric"; call Reset between ticks.
type Sink struct {
	mu         sync.Mutex
	keys       []state.SeriesKey
	ids        map[state.SeriesKey]state.SeriesID
	Values     map[string]float32
	Containers map[string]*v1.Container
	Workloads  map[string]*v1.Workload
	Listeners  []*v1.PortBinding
	Routes     []*v1.IngressRoute
	Docker     state.DockerResources
	Events     []*v1.Event
	Host       *v1.HostInfo
	FS         []*v1.FilesystemUsage
	// Calls counts inventory method calls by name.
	Calls map[string]int
}

// New returns an empty Sink.
func New() *Sink {
	return &Sink{
		ids:        map[state.SeriesKey]state.SeriesID{},
		Values:     map[string]float32{},
		Containers: map[string]*v1.Container{},
		Workloads:  map[string]*v1.Workload{},
		Calls:      map[string]int{},
	}
}

var _ state.Sink = (*Sink)(nil)

// Reset clears the recorded metric values (start of a tick).
func (s *Sink) Reset() {
	s.mu.Lock()
	clear(s.Values)
	s.mu.Unlock()
}

// Series implements state.Sink.
func (s *Sink) Series(entity, metric string) state.SeriesID {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := state.SeriesKey{Entity: entity, Metric: metric}
	if id, ok := s.ids[k]; ok {
		return id
	}
	s.keys = append(s.keys, k)
	id := state.SeriesID(len(s.keys))
	s.ids[k] = id
	return id
}

// Set implements state.Sink.
func (s *Sink) Set(id state.SeriesID, v float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == 0 || int(id) > len(s.keys) {
		return
	}
	k := s.keys[id-1]
	s.Values[k.Entity+"|"+k.Metric] = v
}

// Value returns a recorded value.
func (s *Sink) Value(entity, metric string) (float32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Values[entity+"|"+metric]
	return v, ok
}

// Keys returns the recorded value keys, sorted.
func (s *Sink) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.Values))
	for k := range s.Values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Sink) call(name string) { s.Calls[name]++ }

// UpsertContainer implements state.Sink.
func (s *Sink) UpsertContainer(c *v1.Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("UpsertContainer")
	s.Containers[c.Id] = proto.Clone(c).(*v1.Container)
}

// RemoveContainer implements state.Sink.
func (s *Sink) RemoveContainer(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("RemoveContainer")
	delete(s.Containers, id)
}

// UpsertWorkload implements state.Sink.
func (s *Sink) UpsertWorkload(w *v1.Workload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("UpsertWorkload")
	s.Workloads[w.Id] = proto.Clone(w).(*v1.Workload)
}

// RemoveWorkload implements state.Sink.
func (s *Sink) RemoveWorkload(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("RemoveWorkload")
	delete(s.Workloads, id)
}

// ReplaceListeners implements state.Sink.
func (s *Sink) ReplaceListeners(l []*v1.PortBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("ReplaceListeners")
	s.Listeners = l
}

// ReplaceRoutes implements state.Sink.
func (s *Sink) ReplaceRoutes(r []*v1.IngressRoute) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("ReplaceRoutes")
	s.Routes = r
}

// ReplaceDockerResources implements state.Sink (merging like the store).
func (s *Sink) ReplaceDockerResources(r state.DockerResources) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("ReplaceDockerResources")
	if r.Engine != nil {
		s.Docker.Engine = r.Engine
	}
	if r.ImagesSet {
		s.Docker.Images, s.Docker.ImagesSet = r.Images, true
	}
	if r.VolumesSet {
		s.Docker.Volumes, s.Docker.VolumesSet = r.Volumes, true
	}
	if r.NetworksSet {
		s.Docker.Networks, s.Docker.NetworksSet = r.Networks, true
	}
	if r.DiskUsage != nil {
		s.Docker.DiskUsage = r.DiskUsage
	}
}

// Emit implements state.Sink.
func (s *Sink) Emit(e *v1.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Events = append(s.Events, e)
}

// SetHostInfo implements state.Sink.
func (s *Sink) SetHostInfo(h *v1.HostInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("SetHostInfo")
	s.Host = h
}

// ReplaceFilesystems implements state.Sink.
func (s *Sink) ReplaceFilesystems(f []*v1.FilesystemUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("ReplaceFilesystems")
	s.FS = f
}

// Snapshot locks and returns a copy of the container map.
func (s *Sink) Snapshot() map[string]*v1.Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*v1.Container, len(s.Containers))
	for k, v := range s.Containers {
		out[k] = v
	}
	return out
}

// CallCount returns how often an inventory method was called.
func (s *Sink) CallCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Calls[name]
}
