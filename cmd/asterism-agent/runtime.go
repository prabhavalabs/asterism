package main

import (
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

type runtimeTuning struct {
	gomaxprocs  int
	memoryLimit int64
	gcPercent   int
}

// applyRuntime applies agent.gomaxprocs, agent.memory_limit and
// agent.gc_percent. The GOMAXPROCS, GOMEMLIMIT and GOGC environment
// variables win: the Go runtime has already applied them at start-up.
func applyRuntime(a config.Agent, env config.LookupEnv) runtimeTuning {
	set := func(k string) bool {
		v, ok := env(k)
		return ok && strings.TrimSpace(v) != ""
	}
	if !set("GOMAXPROCS") && a.GOMAXPROCS > 0 {
		runtime.GOMAXPROCS(a.GOMAXPROCS)
	}
	if !set("GOMEMLIMIT") && a.MemoryLimit > 0 {
		debug.SetMemoryLimit(int64(a.MemoryLimit))
	}
	if !set("GOGC") && a.GCPercent != 0 {
		debug.SetGCPercent(a.GCPercent)
	}
	t := runtimeTuning{gomaxprocs: runtime.GOMAXPROCS(0), memoryLimit: debug.SetMemoryLimit(-1)}
	// SetGCPercent returns the previous value; set it back immediately.
	t.gcPercent = debug.SetGCPercent(100)
	debug.SetGCPercent(t.gcPercent)
	return t
}

func envFor(cfg *config.Config) state.Env {
	return state.Env{Config: cfg, Root: cfg.Host.Root, Now: time.Now}
}

// residentBytes returns the process RSS from /proc/self/statm, falling back
// to the Go runtime's view of mapped memory on other systems.
func residentBytes() uint64 {
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 1 {
			if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				return pages * uint64(os.Getpagesize())
			}
		}
	}
	s := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 || s[1].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64() - s[1].Value.Uint64()
}

// cpuMeter reports process CPU use as a fraction of one core between calls.
type cpuMeter struct {
	mu       sync.Mutex
	lastCPU  time.Duration
	lastWall time.Time
}

func newCPUMeter() *cpuMeter { return &cpuMeter{lastCPU: processCPU(), lastWall: time.Now()} }

func (m *cpuMeter) util() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	cpu, now := processCPU(), time.Now()
	wall := now.Sub(m.lastWall)
	d := cpu - m.lastCPU
	m.lastCPU, m.lastWall = cpu, now
	if wall <= 0 || d < 0 {
		return 0
	}
	return float64(d) / float64(wall)
}
