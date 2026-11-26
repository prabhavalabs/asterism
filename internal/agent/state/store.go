package state

import (
	"math"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// MaxSeries caps the number of interned series per process (the hub refuses
// more than 5,000 per agent).
const MaxSeries = 5000

// SeriesKey names a series.
type SeriesKey struct {
	Entity string
	Metric string
}

// Options configures a Store.
type Options struct {
	// KeyframeEvery forces a full row per group at this cadence (60 s).
	KeyframeEvery time.Duration
	// MaxEvents bounds events pending between flushes (drop oldest).
	MaxEvents int
	// Projects are resolved against the inventory into ProjectRules.
	Projects []config.Project
	// AnnotateContainer, when set, runs on every upserted container copy
	// (the action gate marks containers outside the allow-list protected).
	AnnotateContainer func(*v1.Container)
}

type seriesInfo struct {
	key   SeriesKey
	group int32 // -1 until first Set
	alive bool
	last  float32
	mark  uint32 // commit generation that last saw this series
}

type group struct {
	idx     uint32
	name    string
	members []SeriesID
	lastKF  time.Time
	forceKF bool
}

// Store is the agent state shared by sources, the scheduler and the uplink.
// It is safe for concurrent use.
type Store struct {
	opts Options

	mu            sync.Mutex
	ids           map[SeriesKey]SeriesID
	series        []seriesInfo // index = SeriesID; [0] unused
	groups        []*group
	gen           uint32
	newDict       []SeriesID
	rows          []*v1.MetricBatch
	rowGroups     []uint32
	kfRows        []uint32
	seriesDropped uint64
	scratchIDs    []uint32
	scratchVals   []float32

	events        []*v1.Event
	eventsDropped uint64

	inv inventory
}

// NewStore returns an empty Store.
func NewStore(opts Options) *Store {
	if opts.KeyframeEvery <= 0 {
		opts.KeyframeEvery = 60 * time.Second
	}
	if opts.MaxEvents <= 0 {
		opts.MaxEvents = 500
	}
	s := &Store{
		opts:   opts,
		ids:    make(map[SeriesKey]SeriesID, 256),
		series: make([]seriesInfo, 1, 256),
	}
	s.inv.init()
	return s
}

// Sink returns a new per-source handle. Each handle is one sampling group.
func (s *Store) Sink(name string) *Handle {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &group{idx: uint32(len(s.groups)), name: name, forceKF: true}
	s.groups = append(s.groups, g)
	return &Handle{st: s, g: g}
}

// Handle is a Source's Sink. Series values set between commits form one row.
type Handle struct {
	st   *Store
	g    *group
	ids  []SeriesID
	vals []float32
}

var _ Sink = (*Handle)(nil)

// Name returns the group name.
func (h *Handle) Name() string { return h.g.name }

// Group returns the wire group index.
func (h *Handle) Group() uint32 { return h.g.idx }

// Series interns (entity, metric).
func (h *Handle) Series(entity, metric string) SeriesID { return h.st.Series(entity, metric) }

// Set records the current tick value of id.
func (h *Handle) Set(id SeriesID, v float32) {
	if id == InvalidSeries {
		return
	}
	h.ids = append(h.ids, id)
	h.vals = append(h.vals, v)
}

// Commit turns the values set since the previous commit into one row at now.
// complete=false (the tick failed part-way) keeps unset series alive and
// defers the keyframe.
func (h *Handle) Commit(now time.Time, complete bool) {
	h.st.commit(h, now, complete)
	h.ids = h.ids[:0]
	h.vals = h.vals[:0]
}

// UpsertContainer implements Sink.
func (h *Handle) UpsertContainer(c *v1.Container) { h.st.UpsertContainer(c) }

// RemoveContainer implements Sink.
func (h *Handle) RemoveContainer(id string) { h.st.RemoveContainer(id) }

// UpsertWorkload implements Sink.
func (h *Handle) UpsertWorkload(w *v1.Workload) { h.st.UpsertWorkload(w) }

// RemoveWorkload implements Sink.
func (h *Handle) RemoveWorkload(id string) { h.st.RemoveWorkload(id) }

// ReplaceListeners implements Sink.
func (h *Handle) ReplaceListeners(l []*v1.PortBinding) { h.st.ReplaceListeners(l) }

// ReplaceRoutes implements Sink.
func (h *Handle) ReplaceRoutes(r []*v1.IngressRoute) { h.st.ReplaceRoutes(r) }

// ReplaceDockerResources implements Sink.
func (h *Handle) ReplaceDockerResources(r DockerResources) { h.st.ReplaceDockerResources(r) }

// Emit implements Sink.
func (h *Handle) Emit(e *v1.Event) { h.st.Emit(e) }

// SetHostInfo implements Sink.
func (h *Handle) SetHostInfo(hi *v1.HostInfo) { h.st.SetHostInfo(hi) }

// ReplaceFilesystems implements Sink.
func (h *Handle) ReplaceFilesystems(f []*v1.FilesystemUsage) { h.st.ReplaceFilesystems(f) }

// Series interns (entity, metric) and returns its stable ID, or
// InvalidSeries once MaxSeries is reached.
func (s *Store) Series(entity, metric string) SeriesID {
	k := SeriesKey{Entity: entity, Metric: metric}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.ids[k]; ok {
		return id
	}
	if len(s.series) > MaxSeries {
		s.seriesDropped++
		return InvalidSeries
	}
	id := SeriesID(len(s.series))
	s.series = append(s.series, seriesInfo{key: k, group: -1})
	s.ids[k] = id
	return id
}

func (s *Store) commit(h *Handle, now time.Time, complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := h.g
	s.gen++
	if s.gen == 0 { // wrapped: clear marks
		for i := range s.series {
			s.series[i].mark = 0
		}
		s.gen = 1
	}
	kf := complete && (g.forceKF || now.Sub(g.lastKF) >= s.opts.KeyframeEvery)
	// Build into reusable scratch, then copy exactly once (allocation budget).
	s.scratchIDs, s.scratchVals = s.scratchIDs[:0], s.scratchVals[:0]
	add := func(id SeriesID, v float32) {
		s.scratchIDs = append(s.scratchIDs, uint32(id))
		s.scratchVals = append(s.scratchVals, v)
	}
	// Walk backwards so the last Set of a series in this tick wins.
	for i := len(h.ids) - 1; i >= 0; i-- {
		id, v := h.ids[i], h.vals[i]
		if int(id) >= len(s.series) || math.IsNaN(float64(v)) {
			continue
		}
		si := &s.series[id]
		if si.mark == s.gen {
			continue
		}
		switch {
		case si.group < 0:
			si.group = int32(g.idx)
			g.members = append(g.members, id)
			s.newDict = append(s.newDict, id)
		case si.group != int32(g.idx):
			continue // owned by another source
		}
		si.mark = s.gen
		if kf || !si.alive || math.Float32bits(si.last) != math.Float32bits(v) {
			add(id, v)
			si.last = v
			si.alive = true
		}
	}
	if complete {
		nan := float32(math.NaN())
		for _, id := range g.members {
			si := &s.series[id]
			if si.alive && si.mark != s.gen {
				add(id, nan)
				si.alive = false
			}
		}
	}
	if kf {
		g.lastKF = now
		g.forceKF = false
	}
	var row *v1.MetricBatch
	if n := len(s.scratchIDs); n > 0 {
		row = &v1.MetricBatch{TsMs: now.UnixMilli(), SeriesIds: make([]uint32, n), Values: make([]float32, n)}
		copy(row.SeriesIds, s.scratchIDs)
		copy(row.Values, s.scratchVals)
	}
	if row == nil {
		// Nothing changed: still emit an empty row (just the timestamp) while
		// the group has live series, so the hub can carry them forward at
		// this tick instead of leaving a gap.
		live := false
		for _, id := range g.members {
			if s.series[id].alive {
				live = true
				break
			}
		}
		if !live {
			return
		}
		row = &v1.MetricBatch{TsMs: now.UnixMilli()}
	}
	if kf {
		s.kfRows = append(s.kfRows, uint32(len(s.rows)))
	}
	s.rows = append(s.rows, row)
	s.rowGroups = append(s.rowGroups, g.idx)
}

// TakeMetrics returns the dict additions and rows committed since the
// previous call, or nil when there are none.
func (s *Store) TakeMetrics() *v1.AgentMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 && len(s.newDict) == 0 {
		return nil
	}
	m := &v1.AgentMetrics{
		Rows:         s.rows,
		RowGroups:    s.rowGroups,
		KeyframeRows: s.kfRows,
	}
	s.appendDictLocked(m, s.newDict)
	s.rows, s.rowGroups, s.kfRows, s.newDict = nil, nil, nil, nil
	return m
}

// FullDict returns every announced series, sent at the start of each
// connection so a hub that lost state can decode buffered rows.
func (s *Store) FullDict() *v1.AgentMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]SeriesID, 0, len(s.series))
	for id := 1; id < len(s.series); id++ {
		if s.series[id].group >= 0 {
			ids = append(ids, SeriesID(id))
		}
	}
	m := &v1.AgentMetrics{}
	s.appendDictLocked(m, ids)
	return m
}

func (s *Store) appendDictLocked(m *v1.AgentMetrics, ids []SeriesID) {
	if len(ids) == 0 {
		return
	}
	m.Dict = make([]*v1.SeriesEntry, 0, len(ids))
	m.DictGroups = make([]uint32, 0, len(ids))
	for _, id := range ids {
		si := s.series[id]
		m.Dict = append(m.Dict, &v1.SeriesEntry{SeriesId: uint32(id), EntityId: si.key.Entity, Metric: si.key.Metric})
		m.DictGroups = append(m.DictGroups, uint32(si.group))
	}
}

// ForceKeyframes makes the next committed row of every group a keyframe
// (after a reconnect or a dropped frame).
func (s *Store) ForceKeyframes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.groups {
		g.forceKF = true
	}
}

// Emit queues a non-Docker event for the next flush (drop oldest when full).
func (s *Store) Emit(e *v1.Event) {
	if e == nil {
		return
	}
	c := cloneEvent(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= s.opts.MaxEvents {
		copy(s.events, s.events[1:])
		s.events = s.events[:len(s.events)-1]
		s.eventsDropped++
	}
	s.events = append(s.events, c)
}

// TakeEvents returns and clears the pending events.
func (s *Store) TakeEvents() []*v1.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := s.events
	s.events = nil
	return ev
}

// StoreStats are counters for AgentStats.
type StoreStats struct {
	Series        int
	SeriesDropped uint64
	EventsDropped uint64
}

// Stats returns the current counters.
func (s *Store) Stats() StoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StoreStats{Series: len(s.series) - 1, SeriesDropped: s.seriesDropped, EventsDropped: s.eventsDropped}
}
