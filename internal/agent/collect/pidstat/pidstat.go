// Package pidstat samples CPU and resident memory for sets of pids from
// /proc/<pid>/stat, for workloads that are plain processes (process rules,
// pm2 apps).
package pidstat

import (
	"os"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/procfs"
)

// UserHZ is the kernel's USER_HZ (clock ticks per second in /proc), which
// is 100 on every architecture the agent ships for.
const UserHZ = 100

// Sampler keeps the previous CPU counters per pid.
type Sampler struct {
	procDir  string
	pageSize uint64
	buf      []byte
	prev     map[int]cpuSample
	seen     map[int]bool
}

type cpuSample struct {
	start, ticks uint64
	at           time.Time
}

// New returns a sampler for procDir (<root>/proc).
func New(procDir string) *Sampler {
	return &Sampler{procDir: procDir, pageSize: uint64(os.Getpagesize()), buf: make([]byte, 4096), prev: map[int]cpuSample{}, seen: map[int]bool{}}
}

// Stat reads /proc/<pid>/stat.
func (s *Sampler) Stat(pid int) (procfs.PidStat, bool) {
	b, err := procfs.ReadFile(procfs.PidPath(s.procDir, pid, "stat"), s.buf)
	if err != nil {
		return procfs.PidStat{}, false
	}
	return procfs.ParsePidStat(b)
}

// Usage is the summed usage of a pid set.
type Usage struct {
	CPU      float64 // cores
	RSS      uint64  // bytes
	Alive    int
	HaveRate bool
}

// Sample measures pids at now. Call Forget after each round so pids that
// disappeared are dropped.
func (s *Sampler) Sample(now time.Time, pids []int) Usage {
	var u Usage
	for _, pid := range pids {
		st, ok := s.Stat(pid)
		if !ok || st.State == 'Z' {
			continue
		}
		u.Alive++
		u.RSS += st.RSSPages * s.pageSize
		ticks := st.UTime + st.STime
		s.seen[pid] = true
		if p, ok := s.prev[pid]; ok && p.start == st.StartTime {
			if dt := now.Sub(p.at).Seconds(); dt > 0 {
				u.CPU += float64(procfs.Delta(p.ticks, ticks)) / UserHZ / dt
				u.HaveRate = true
			}
		}
		s.prev[pid] = cpuSample{start: st.StartTime, ticks: ticks, at: now}
	}
	return u
}

// Forget drops pids not sampled since the previous Forget.
func (s *Sampler) Forget() {
	for pid := range s.prev {
		if !s.seen[pid] {
			delete(s.prev, pid)
		}
	}
	clear(s.seen)
}

// StartedAtMs converts a starttime (ticks after boot) with the boot time.
func StartedAtMs(btimeSec, startTicks uint64) int64 {
	if btimeSec == 0 {
		return 0
	}
	return int64(btimeSec)*1000 + int64(startTicks*1000/UserHZ)
}

// BootTime reads btime from <procDir>/stat.
func BootTime(procDir string) uint64 {
	b, err := os.ReadFile(procDir + "/stat")
	if err != nil {
		return 0
	}
	_, bt, _ := procfs.ParseStat(b)
	return bt
}
