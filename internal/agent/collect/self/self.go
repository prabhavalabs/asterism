// Package self samples the agent's own footprint as series on entity
// "agent": mem.rss_bytes (from /proc/self/statm, or the Go runtime's mapped
// memory elsewhere), cpu.util (cores, from /proc/self/stat), go.goroutines
// and go.heap_bytes. Uplink counters (frames sent/dropped, buffer, events
// dropped) travel in AgentStats on every heartbeat.
package self

import (
	"context"
	"os"
	"runtime"
	"runtime/metrics"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/pidstat"
	"github.com/prabhavalabs/asterism/internal/agent/procfs"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

// Entity is the series entity for the agent itself.
const Entity = "agent"

// Source is the self-stats collector.
type Source struct {
	interval time.Duration
	buf      []byte
	stat     *procfs.File
	statm    *procfs.File
	cpu      procfs.Counter
	page     uint64
	samples  []metrics.Sample
	ids      [4]state.SeriesID
}

// New returns a self source.
func New(interval time.Duration) *Source { return &Source{interval: interval} }

// Name implements state.Source.
func (s *Source) Name() string { return "self" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Init implements state.Source. It reads the agent's own /proc/self, never
// the host root.
func (s *Source) Init(context.Context, state.Env) ([]string, error) {
	s.buf = make([]byte, 4096)
	s.stat, _ = procfs.Open("/proc/self/stat")
	s.statm, _ = procfs.Open("/proc/self/statm")
	s.page = uint64(os.Getpagesize())
	s.samples = []metrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
	}
	return nil, nil
}

// Stats is one sample.
type Stats struct {
	RSS        uint64
	CPU        float64
	HaveCPU    bool
	Goroutines int
	Heap       uint64
}

// Sample reads the current footprint.
func (s *Source) Sample(now time.Time) Stats {
	st := Stats{Goroutines: runtime.NumGoroutine()}
	metrics.Read(s.samples)
	if s.samples[0].Value.Kind() == metrics.KindUint64 {
		st.Heap = s.samples[0].Value.Uint64()
	}
	if s.statm != nil {
		if b, err := s.statm.Read(s.buf); err == nil {
			if pages, ok := procfs.ParseUint(procfs.Field(b, 1)); ok {
				st.RSS = pages * s.page
			}
		}
	}
	if st.RSS == 0 && s.samples[1].Value.Kind() == metrics.KindUint64 && s.samples[2].Value.Kind() == metrics.KindUint64 {
		st.RSS = s.samples[1].Value.Uint64() - s.samples[2].Value.Uint64()
	}
	if s.stat != nil {
		if b, err := s.stat.Read(s.buf); err == nil {
			if ps, ok := procfs.ParsePidStat(b); ok {
				ticks := ps.UTime + ps.STime
				if r, ok := s.cpu.Observe(now, ticks); ok {
					st.CPU, st.HaveCPU = r/pidstat.UserHZ, true
				}
			}
		}
	}
	return st
}

// Tick implements state.Source.
func (s *Source) Tick(now time.Time, sink state.Sink) error {
	if s.ids[0] == 0 {
		s.ids = [4]state.SeriesID{
			sink.Series(Entity, "mem.rss_bytes"), sink.Series(Entity, "cpu.util"),
			sink.Series(Entity, "go.goroutines"), sink.Series(Entity, "go.heap_bytes"),
		}
	}
	st := s.Sample(now)
	if st.RSS > 0 {
		sink.Set(s.ids[0], float32(st.RSS))
	}
	if st.HaveCPU {
		sink.Set(s.ids[1], float32(st.CPU))
	}
	sink.Set(s.ids[2], float32(st.Goroutines))
	sink.Set(s.ids[3], float32(st.Heap))
	return nil
}
