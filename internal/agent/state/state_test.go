package state

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"google.golang.org/protobuf/proto"
)

type sample struct {
	id uint32
	v  float32
}

func sortSamples(s []sample) {
	sort.Slice(s, func(a, b int) bool { return s[a].id < s[b].id })
}

// wire round-trips a message through the protobuf encoding, like the uplink.
func wire[T proto.Message](t *testing.T, m T) T {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	out := m.ProtoReflect().New().Interface().(T)
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMetricsRoundTripProperty: agent encode -> wire -> reference decode
// reproduces, for every tick of every group, exactly the live series and
// values the sources produced (carry-forward, NaN retirement, keyframes,
// partial ticks, forced keyframes, duplicate sets).
func TestMetricsRoundTripProperty(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { metricsProperty(t, seed) })
	}
}

func metricsProperty(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, seed*7919))
	st := NewStore(Options{KeyframeEvery: 60 * time.Second})
	const groups = 3
	handles := make([]*Handle, groups)
	cands := make([][]SeriesID, groups)
	for g := range groups {
		handles[g] = st.Sink(fmt.Sprintf("src%d", g))
		n := 1 + rng.IntN(12)
		for i := range n {
			cands[g] = append(cands[g], handles[g].Series(fmt.Sprintf("ctr:g%d-e%d", g, i/3), fmt.Sprintf("m%d", i%3)))
		}
	}
	values := []float32{0, 1, 2.5, -3, 1e9, float32(math.Inf(1))}
	truth := make([]map[SeriesID]float32, groups) // alive values per group
	for g := range truth {
		truth[g] = map[SeriesID]float32{}
	}
	want := map[int64][]sample{}
	got := map[int64][]sample{}
	dec := NewDecoder()
	take := func() {
		m := st.TakeMetrics()
		if m == nil {
			return
		}
		if err := dec.Apply(wire(t, m), func(p Point) {
			got[p.TsMs] = append(got[p.TsMs], sample{p.ID, p.Value})
		}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.UnixMilli(1_700_000_000_000)
	for tick := 0; tick < 400; tick++ {
		now = now.Add(time.Duration(1+rng.IntN(9000)) * time.Millisecond)
		g := rng.IntN(groups)
		h := handles[g]
		complete := rng.IntN(10) != 0
		set := map[SeriesID]float32{}
		for _, id := range cands[g] {
			switch r := rng.IntN(10); {
			case r < 6: // set, often to the previous value
				v := values[rng.IntN(len(values))]
				if old, ok := truth[g][id]; ok && rng.IntN(2) == 0 {
					v = old
				}
				if rng.IntN(8) == 0 { // duplicate set in one tick: last wins
					h.Set(id, v+100)
				}
				h.Set(id, v)
				set[id] = v
			case r == 6:
				h.Set(id, float32(math.NaN())) // same as unset
			}
		}
		h.Set(InvalidSeries, 1) // ignored
		h.Commit(now, complete)
		for id, v := range set {
			truth[g][id] = v
		}
		if complete {
			for id := range truth[g] {
				if _, ok := set[id]; !ok {
					delete(truth[g], id)
				}
			}
		}
		if len(truth[g]) > 0 {
			var w []sample
			for id, v := range truth[g] {
				w = append(w, sample{uint32(id), v})
			}
			want[now.UnixMilli()] = w
		}
		if rng.IntN(4) == 0 {
			take()
		}
		if rng.IntN(50) == 0 {
			st.ForceKeyframes()
		}
	}
	take()
	if dec.Unknown != 0 {
		t.Fatalf("decoder saw %d unknown samples", dec.Unknown)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d ticks, want %d", len(got), len(want))
	}
	for ts, w := range want {
		g := got[ts]
		sortSamples(w)
		sortSamples(g)
		if fmt.Sprint(w) != fmt.Sprint(g) {
			t.Fatalf("ts %d: got %v want %v", ts, g, w)
		}
	}
}

func TestCarryForwardOmitsUnchangedAndKeyframes(t *testing.T) {
	st := NewStore(Options{KeyframeEvery: 60 * time.Second})
	h := st.Sink("host")
	a, b := h.Series("host", "cpu.util"), h.Series("host", "mem.used_bytes")
	if a2 := h.Series("host", "cpu.util"); a2 != a {
		t.Fatalf("interning not stable: %d vs %d", a, a2)
	}
	t0 := time.UnixMilli(1_000_000)
	h.Set(a, 0.5)
	h.Set(b, 100)
	h.Commit(t0, true) // first row: keyframe
	h.Set(a, 0.5)
	h.Set(b, 101)
	h.Commit(t0.Add(2*time.Second), true) // only b changed
	h.Set(a, 0.5)
	h.Set(b, 101)
	h.Commit(t0.Add(4*time.Second), true) // nothing changed: empty row
	h.Set(a, 0.5)
	h.Set(b, 101)
	h.Commit(t0.Add(62*time.Second), true) // keyframe again
	m := st.TakeMetrics()
	if len(m.Dict) != 2 || len(m.Rows) != 4 {
		t.Fatalf("dict=%d rows=%d", len(m.Dict), len(m.Rows))
	}
	if n := len(m.Rows[1].SeriesIds); n != 1 || m.Rows[1].SeriesIds[0] != uint32(b) {
		t.Fatalf("row 1 should carry only b: %v", m.Rows[1].SeriesIds)
	}
	if len(m.Rows[2].SeriesIds) != 0 {
		t.Fatalf("row 2 should be empty: %v", m.Rows[2].SeriesIds)
	}
	if fmt.Sprint(m.KeyframeRows) != "[0 3]" {
		t.Fatalf("keyframe rows %v", m.KeyframeRows)
	}
	if st.TakeMetrics() != nil {
		t.Fatal("second take should be empty")
	}
	full := st.FullDict()
	if len(full.Dict) != 2 || len(full.Rows) != 0 {
		t.Fatalf("full dict %v", full)
	}
}

func TestDecoderFreshHubJoinsMidSession(t *testing.T) {
	st := NewStore(Options{})
	h := st.Sink("host")
	a := h.Series("host", "cpu.util")
	t0 := time.UnixMilli(5_000)
	h.Set(a, 1)
	h.Commit(t0, true)
	_ = st.TakeMetrics() // lost with the old hub
	// New hub: full dict first, then rows that omit the unchanged value.
	dec := NewDecoder()
	if err := dec.Apply(st.FullDict(), nil); err != nil {
		t.Fatal(err)
	}
	h.Set(a, 1)
	h.Commit(t0.Add(time.Second), true)
	var pts []Point
	_ = dec.Apply(st.TakeMetrics(), func(p Point) { pts = append(pts, p) })
	if len(pts) != 0 {
		t.Fatalf("unknown value must not be emitted before a keyframe/change: %v", pts)
	}
	st.ForceKeyframes()
	h.Set(a, 1)
	h.Commit(t0.Add(2*time.Second), true)
	_ = dec.Apply(st.TakeMetrics(), func(p Point) { pts = append(pts, p) })
	if len(pts) != 1 || pts[0].Value != 1 || pts[0].Key.Metric != "cpu.util" {
		t.Fatalf("keyframe should establish the value: %v", pts)
	}
}

func TestDecoderRejectsMalformed(t *testing.T) {
	d := NewDecoder()
	bad := []*v1.AgentMetrics{
		{Dict: []*v1.SeriesEntry{{SeriesId: 1}}},
		{Rows: []*v1.MetricBatch{{}}},
		{Rows: []*v1.MetricBatch{{SeriesIds: []uint32{1}}}, RowGroups: []uint32{0}},
		{KeyframeRows: []uint32{0}},
	}
	for i, m := range bad {
		if err := d.Apply(m, nil); !errors.Is(err, ErrMalformed) {
			t.Errorf("case %d: want ErrMalformed, got %v", i, err)
		}
	}
}

func TestSeriesCap(t *testing.T) {
	st := NewStore(Options{})
	h := st.Sink("x")
	var last SeriesID
	for i := range MaxSeries + 10 {
		last = h.Series(fmt.Sprint("e", i), "m")
	}
	if last != InvalidSeries || st.Stats().SeriesDropped != 10 || st.Stats().Series != MaxSeries {
		t.Fatalf("cap: last=%d stats=%+v", last, st.Stats())
	}
}

func TestEventsDropOldest(t *testing.T) {
	st := NewStore(Options{MaxEvents: 3})
	for i := range 5 {
		st.Emit(&v1.Event{Id: fmt.Sprint(i)})
	}
	ev := st.TakeEvents()
	if len(ev) != 3 || ev[0].Id != "2" || st.Stats().EventsDropped != 2 {
		t.Fatalf("events %v stats %+v", ev, st.Stats())
	}
}

// TestInventoryRoundTripProperty: random mutations, lost deltas, resyncs
// and reconnects always converge to the agent's snapshot on the hub.
func TestInventoryRoundTripProperty(t *testing.T) {
	for seed := uint64(1); seed <= 30; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { inventoryProperty(t, seed) })
	}
}

func inventoryProperty(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 99))
	st := NewStore(Options{Projects: []config.Project{
		{Name: "shop", Members: config.Members{Compose: []string{"shop"}, Units: []string{"nginx*"}}},
	}})
	hub := NewInventory()
	var lastSent uint64 // what the agent handed out last on this connection
	deliver := func(snap *v1.InventorySnapshot, delta *v1.InventoryDelta, lost bool) {
		if lost {
			return
		}
		if snap != nil {
			hub.ApplySnapshot(wire(t, snap))
		}
		if delta != nil {
			if err := hub.ApplyDelta(wire(t, delta)); err != nil {
				if !errors.Is(err, ErrVersionMismatch) {
					t.Fatal(err)
				}
				// Hub sends ResyncRequest; the agent answers with a snapshot.
				s, _ := st.TakeInventory(0, true)
				hub.ApplySnapshot(wire(t, s))
			}
		}
	}
	for step := 0; step < 300; step++ {
		switch rng.IntN(9) {
		case 0, 1:
			n := rng.IntN(6)
			st.UpsertContainer(&v1.Container{Id: fmt.Sprintf("ctr:c%d", n), Name: fmt.Sprintf("c%d", n),
				ComposeProject: []string{"shop", "other"}[rng.IntN(2)], RestartCount: uint32(rng.IntN(3))})
		case 2:
			st.RemoveContainer(fmt.Sprintf("ctr:c%d", rng.IntN(6)))
		case 3:
			n := rng.IntN(4)
			st.UpsertWorkload(&v1.Workload{Id: fmt.Sprintf("unit:nginx%d.service", n), Name: fmt.Sprintf("nginx%d.service", n),
				Kind: v1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT, State: []string{"active", "failed"}[rng.IntN(2)]})
		case 4:
			st.RemoveWorkload(fmt.Sprintf("unit:nginx%d.service", rng.IntN(4)))
		case 5:
			var ls []*v1.PortBinding
			for i := range rng.IntN(3) {
				ls = append(ls, &v1.PortBinding{Id: fmt.Sprint(i), HostPort: uint32(80 + i)})
			}
			st.ReplaceListeners(ls)
		case 6:
			st.ReplaceDockerResources(DockerResources{
				Engine:    &v1.DockerEngine{Available: true, Containers: uint32(rng.IntN(3))},
				Images:    []*v1.DockerImage{{Id: fmt.Sprint(rng.IntN(2))}},
				ImagesSet: rng.IntN(2) == 0,
			})
			st.SetHostInfo(&v1.HostInfo{Hostname: "h", Cores: uint32(1 + rng.IntN(2))})
		case 7: // flush on the live connection; some frames are lost
			snap, delta := st.TakeInventory(lastSent, false)
			lastSent = st.InventoryVersion()
			deliver(snap, delta, rng.IntN(6) == 0)
		case 8: // reconnect: HelloAck carries what the hub holds
			snap, delta := st.TakeInventory(hub.Version, rng.IntN(5) == 0)
			lastSent = st.InventoryVersion()
			deliver(snap, delta, false)
		}
	}
	snap, delta := st.TakeInventory(hub.Version, false)
	deliver(snap, delta, false)
	if want, got := st.Snapshot(), hub.Snapshot(); !proto.Equal(want, got) {
		t.Fatalf("hub diverged\nwant %v\ngot  %v", want, got)
	}
}

func TestIdenticalUpsertIsNoop(t *testing.T) {
	st := NewStore(Options{})
	c := &v1.Container{Id: "ctr:a", Name: "a"}
	st.UpsertContainer(c)
	v := st.InventoryVersion()
	st.UpsertContainer(&v1.Container{Name: "a"}) // id derived from name
	st.ReplaceListeners(nil)
	if st.InventoryVersion() != v {
		t.Fatalf("version moved on identical data: %d -> %d", v, st.InventoryVersion())
	}
	c.Name = "mutated-by-source"
	if st.FindContainer("a").GetName() != "a" {
		t.Fatal("store must keep its own copy")
	}
}

func TestDeltaThenSnapshotOnUnknownVersion(t *testing.T) {
	st := NewStore(Options{})
	st.UpsertContainer(&v1.Container{Name: "a"})
	snap, delta := st.TakeInventory(0, false)
	if snap != nil || delta == nil || delta.BaseVersion != 0 || len(delta.UpsertContainers) != 1 {
		t.Fatalf("first take on a fresh hub should be a delta from 0: %v %v", snap, delta)
	}
	st.RemoveContainer("ctr:a")
	snap, delta = st.TakeInventory(7, false) // hub holds something else
	if snap == nil || delta != nil || len(snap.Containers) != 0 {
		t.Fatalf("mismatched version should yield a snapshot: %v %v", snap, delta)
	}
	if s, d := st.TakeInventory(st.InventoryVersion(), false); s != nil || d != nil {
		t.Fatal("nothing pending")
	}
}

func BenchmarkTick50(b *testing.B) {
	st := NewStore(Options{})
	h := st.Sink("cgroup")
	ids := make([]SeriesID, 0, 400)
	for c := range 50 {
		for _, m := range []string{"cpu.util", "mem.used_bytes", "net.rx_bps", "net.tx_bps", "io.read_bps", "io.write_bps", "pids", "mem.ratio"} {
			ids = append(ids, h.Series(fmt.Sprintf("ctr:c%d", c), m))
		}
	}
	now := time.UnixMilli(0)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		now = now.Add(5 * time.Second)
		for j, id := range ids {
			h.Set(id, float32((i+j)%7))
		}
		h.Commit(now, true)
		if i%8 == 0 {
			_ = st.TakeMetrics()
		}
	}
}
