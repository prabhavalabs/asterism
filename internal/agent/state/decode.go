package state

import (
	"errors"
	"fmt"
	"math"
	"sort"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"google.golang.org/protobuf/proto"
)

// This file is the reference decoder for the agent wire format. The hub's
// ingest session uses it unchanged: one Decoder and one Inventory per agent
// session (Hello.session_id).

// Point is one decoded sample.
type Point struct {
	ID    uint32
	Key   SeriesKey
	TsMs  int64
	Value float32
}

type decSeries struct {
	key   SeriesKey
	group uint32
	alive bool
	value float32
	seen  uint64
}

// Decoder expands AgentMetrics into points, carrying forward series that a
// row omits. It is not safe for concurrent use.
type Decoder struct {
	series  map[uint32]*decSeries
	members map[uint32][]uint32
	rowGen  uint64
	// Unknown counts samples for series ids not (yet) in the dictionary,
	// e.g. buffered rows that reference a dict frame the hub never saw.
	Unknown uint64
}

// NewDecoder returns an empty decoder.
func NewDecoder() *Decoder {
	return &Decoder{series: map[uint32]*decSeries{}, members: map[uint32][]uint32{}}
}

// ErrMalformed is wrapped by Apply for structurally invalid messages.
var ErrMalformed = errors.New("malformed agent metrics")

// Apply merges m's dictionary and expands each row: emit is called once for
// every live series of the row's group (changed values and carried-forward
// ones), in dictionary order. It validates the message before applying any
// of it.
func (d *Decoder) Apply(m *v1.AgentMetrics, emit func(Point)) error {
	if m == nil {
		return nil
	}
	if len(m.GetDictGroups()) != len(m.GetDict()) {
		return fmt.Errorf("%w: %d dict entries but %d dict_groups", ErrMalformed, len(m.GetDict()), len(m.GetDictGroups()))
	}
	if len(m.GetRowGroups()) != len(m.GetRows()) {
		return fmt.Errorf("%w: %d rows but %d row_groups", ErrMalformed, len(m.GetRows()), len(m.GetRowGroups()))
	}
	kf := make(map[int]bool, len(m.GetKeyframeRows()))
	for _, i := range m.GetKeyframeRows() {
		if int(i) >= len(m.GetRows()) {
			return fmt.Errorf("%w: keyframe row %d out of range", ErrMalformed, i)
		}
		kf[int(i)] = true
	}
	for i, r := range m.GetRows() {
		if len(r.GetSeriesIds()) != len(r.GetValues()) {
			return fmt.Errorf("%w: row %d has %d ids but %d values", ErrMalformed, i, len(r.GetSeriesIds()), len(r.GetValues()))
		}
	}
	for i, e := range m.GetDict() {
		if e.GetSeriesId() == 0 {
			return fmt.Errorf("%w: series id 0 in dict", ErrMalformed)
		}
		g := m.GetDictGroups()[i]
		if s, ok := d.series[e.GetSeriesId()]; ok {
			s.key = SeriesKey{Entity: e.GetEntityId(), Metric: e.GetMetric()}
			if s.group == g {
				continue
			}
			d.removeMember(s.group, e.GetSeriesId())
			s.group = g
		} else {
			d.series[e.GetSeriesId()] = &decSeries{key: SeriesKey{Entity: e.GetEntityId(), Metric: e.GetMetric()}, group: g}
		}
		d.members[g] = append(d.members[g], e.GetSeriesId())
	}
	for i, r := range m.GetRows() {
		g := m.GetRowGroups()[i]
		d.rowGen++
		for j, id := range r.GetSeriesIds() {
			s, ok := d.series[id]
			if !ok || s.group != g {
				d.Unknown++
				continue
			}
			v := r.GetValues()[j]
			s.seen = d.rowGen
			if math.IsNaN(float64(v)) {
				s.alive = false
				continue
			}
			s.alive, s.value = true, v
		}
		for _, id := range d.members[g] {
			s := d.series[id]
			if kf[i] && s.seen != d.rowGen {
				s.alive = false
			}
			if s.alive && emit != nil {
				emit(Point{ID: id, Key: s.key, TsMs: r.GetTsMs(), Value: s.value})
			}
		}
	}
	return nil
}

func (d *Decoder) removeMember(g, id uint32) {
	ms := d.members[g]
	for i, m := range ms {
		if m == id {
			d.members[g] = append(ms[:i], ms[i+1:]...)
			return
		}
	}
}

// Lookup returns the key of a series id.
func (d *Decoder) Lookup(id uint32) (SeriesKey, bool) {
	s, ok := d.series[id]
	if !ok {
		return SeriesKey{}, false
	}
	return s.key, true
}

// Value returns the carried-forward value of a live series.
func (d *Decoder) Value(id uint32) (float32, bool) {
	s, ok := d.series[id]
	if !ok || !s.alive {
		return 0, false
	}
	return s.value, true
}

// Len returns the number of known series.
func (d *Decoder) Len() int { return len(d.series) }

// ---------------------------------------------------------------------------
// Inventory
// ---------------------------------------------------------------------------

// ErrVersionMismatch is returned by Inventory.ApplyDelta when the delta's
// base_version is not the version held; the hub then sends ResyncRequest.
var ErrVersionMismatch = errors.New("inventory delta base version mismatch")

// Inventory is the hub-side copy of one agent's inventory.
type Inventory struct {
	Version     uint64
	Host        *v1.HostInfo
	Engine      *v1.DockerEngine
	Containers  map[string]*v1.Container
	Workloads   map[string]*v1.Workload
	Images      []*v1.DockerImage
	Volumes     []*v1.DockerVolume
	Networks    []*v1.DockerNetwork
	DiskUsage   *v1.DockerDiskUsage
	Listeners   []*v1.PortBinding
	Routes      []*v1.IngressRoute
	Rules       []*v1.ProjectRule
	Filesystems []*v1.FilesystemUsage
}

// NewInventory returns an empty inventory at version 0.
func NewInventory() *Inventory {
	return &Inventory{Containers: map[string]*v1.Container{}, Workloads: map[string]*v1.Workload{}}
}

// ApplySnapshot replaces everything.
func (inv *Inventory) ApplySnapshot(s *v1.InventorySnapshot) {
	*inv = *NewInventory()
	inv.Version = s.GetVersion()
	inv.Host, inv.Engine, inv.DiskUsage = s.GetHost(), s.GetEngine(), s.GetDiskUsage()
	for _, c := range s.GetContainers() {
		inv.Containers[c.GetId()] = c
	}
	for _, w := range s.GetWorkloads() {
		inv.Workloads[w.GetId()] = w
	}
	inv.Images, inv.Volumes, inv.Networks = s.GetImages(), s.GetVolumes(), s.GetNetworks()
	inv.Listeners, inv.Routes, inv.Rules, inv.Filesystems = s.GetListeners(), s.GetRoutes(), s.GetProjectRules(), s.GetFilesystems()
}

// ApplyDelta applies d, or returns ErrVersionMismatch without changes.
func (inv *Inventory) ApplyDelta(d *v1.InventoryDelta) error {
	if d.GetBaseVersion() != inv.Version {
		return fmt.Errorf("%w: hub holds %d, delta is based on %d", ErrVersionMismatch, inv.Version, d.GetBaseVersion())
	}
	for _, c := range d.GetUpsertContainers() {
		inv.Containers[c.GetId()] = c
	}
	for _, id := range d.GetRemovedContainerIds() {
		delete(inv.Containers, id)
	}
	for _, w := range d.GetUpsertWorkloads() {
		inv.Workloads[w.GetId()] = w
	}
	for _, id := range d.GetRemovedWorkloadIds() {
		delete(inv.Workloads, id)
	}
	if r := d.GetReplace(); r != nil {
		if r.GetHost() != nil {
			inv.Host = r.GetHost()
		}
		if r.GetEngine() != nil {
			inv.Engine = r.GetEngine()
		}
		if r.GetDiskUsage() != nil {
			inv.DiskUsage = r.GetDiskUsage()
		}
		if r.GetImagesSet() {
			inv.Images = r.GetImages()
		}
		if r.GetVolumesSet() {
			inv.Volumes = r.GetVolumes()
		}
		if r.GetNetworksSet() {
			inv.Networks = r.GetNetworks()
		}
		if r.GetListenersSet() {
			inv.Listeners = r.GetListeners()
		}
		if r.GetRoutesSet() {
			inv.Routes = r.GetRoutes()
		}
		if r.GetProjectRulesSet() {
			inv.Rules = r.GetProjectRules()
		}
		if r.GetFilesystemsSet() {
			inv.Filesystems = r.GetFilesystems()
		}
	}
	inv.Version = d.GetVersion()
	return nil
}

// Snapshot renders the inventory in the agent's canonical snapshot order.
func (inv *Inventory) Snapshot() *v1.InventorySnapshot {
	s := &v1.InventorySnapshot{
		Version: inv.Version, Host: inv.Host, Engine: inv.Engine, DiskUsage: inv.DiskUsage,
		Images: inv.Images, Volumes: inv.Volumes, Networks: inv.Networks,
		Listeners: inv.Listeners, Routes: inv.Routes, ProjectRules: inv.Rules, Filesystems: inv.Filesystems,
	}
	for _, k := range sortedMapKeys(inv.Containers) {
		s.Containers = append(s.Containers, inv.Containers[k])
	}
	for _, k := range sortedMapKeys(inv.Workloads) {
		s.Workloads = append(s.Workloads, inv.Workloads[k])
	}
	return proto.Clone(s).(*v1.InventorySnapshot)
}

func sortedMapKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
