// Package cgroup samples per-container and per-unit resource usage from
// cgroup v2 files and /proc/<pid>/net/dev, without calling Docker's stats
// API. File handles stay open and are re-read with pread, and series IDs
// are interned once per target, so a steady-state tick does not allocate.
//
// Series (entity "ctr:<name>" or "unit:<name>"):
//
//	cpu.util        cores (1.0 = one full core), from cpu.stat usage_usec
//	mem.used_bytes  memory.current minus memory.stat inactive_file
//	blk.read_bps    io.stat rbytes rate    blk.write_bps  io.stat wbytes rate
//	net.rx_bps      /proc/<pid>/net/dev    net.tx_bps     (own netns only)
//	pids.count      pids.current
//	oom.count       memory.events oom_kill (cumulative)
package cgroup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/procfs"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Source is the cgroup collector.
type Source struct {
	reg      *targets.Registry
	interval time.Duration

	log     *slog.Logger
	cgRoot  string
	procDir string

	buf      []byte
	gen      uint64
	built    bool
	targets  map[string]*target // key: full id or unit name
	order    []*target
	scratchC []targets.Container
	scratchU []targets.Unit
}

// New returns a cgroup source reading targets from reg.
func New(reg *targets.Registry, interval time.Duration) *Source {
	return &Source{reg: reg, interval: interval, targets: map[string]*target{}}
}

// Name implements state.Source.
func (s *Source) Name() string { return "cgroup" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Class implements state.Classed.
func (s *Source) Class() state.Class { return state.ClassContainers }

// Init implements state.Source.
func (s *Source) Init(_ context.Context, env state.Env) ([]string, error) {
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.cgRoot = env.HostPath(procfs.CgroupRoot)
	s.procDir = env.HostPath("/proc")
	if !procfs.IsCgroupV2(s.cgRoot) {
		return nil, fmt.Errorf("no cgroup v2 hierarchy at %s (cgroup v1 hosts are not supported; container metrics are unavailable)", s.cgRoot)
	}
	s.buf = make([]byte, procfs.BufSize)
	return []string{"cgroup_v2"}, nil
}

// Tick implements state.Source.
func (s *Source) Tick(now time.Time, sink state.Sink) error {
	if g := s.reg.Gen(); !s.built || g != s.gen {
		s.rebuild(sink)
		s.gen, s.built = g, true
	}
	for _, t := range s.order {
		t.sample(now, sink, s.buf)
	}
	return nil
}

// Targets returns the number of sampled targets (for discover and tests).
func (s *Source) Targets() int { return len(s.order) }

// Entities returns the sampled entity ids with their cgroup directories.
func (s *Source) Entities() map[string]string {
	out := make(map[string]string, len(s.order))
	for _, t := range s.order {
		out[t.entity] = t.dir
	}
	return out
}

// Close releases every open file.
func (s *Source) Close() {
	for _, t := range s.targets {
		t.close()
	}
	s.targets = map[string]*target{}
	s.order = nil
}

type target struct {
	key    string
	entity string
	unit   bool
	pid    int32
	dir    string

	fCPU, fMem, fMemStat, fIO, fPids, fEvents, fNet *procfs.File

	idCPU, idMem, idRd, idWr, idRx, idTx, idPids, idOOM state.SeriesID
	cpu, rd, wr, rx, tx                                 procfs.Counter

	oomLast uint64
	oomSeen bool
	broken  bool
}

func (s *Source) rebuild(sink state.Sink) {
	want := make(map[string]struct{}, len(s.targets)+4)
	s.scratchC = s.reg.Containers(s.scratchC)
	for _, c := range s.scratchC {
		if !c.Running || c.Pid <= 0 {
			continue
		}
		want[c.FullID] = struct{}{}
		if t, ok := s.targets[c.FullID]; ok && t.pid == c.Pid && t.entity == c.EntityID && !t.broken {
			continue
		}
		if t, ok := s.targets[c.FullID]; ok {
			t.close()
			delete(s.targets, c.FullID)
		}
		if t := s.openContainer(c, sink); t != nil {
			s.targets[c.FullID] = t
		}
	}
	s.scratchU = s.reg.Units(s.scratchU)
	for _, u := range s.scratchU {
		key := "unit/" + u.Name
		want[key] = struct{}{}
		if t, ok := s.targets[key]; ok && t.dir == u.Dir && !t.broken {
			continue
		}
		if t, ok := s.targets[key]; ok {
			t.close()
			delete(s.targets, key)
		}
		if t := s.open(key, "unit:"+u.Name, true, 0, u.Dir, false, sink); t != nil {
			s.targets[key] = t
		}
	}
	for k, t := range s.targets {
		if _, ok := want[k]; !ok {
			t.close()
			delete(s.targets, k)
		}
	}
	s.order = s.order[:0]
	for _, t := range s.targets {
		s.order = append(s.order, t)
	}
}

func (s *Source) openContainer(c targets.Container, sink state.Sink) *target {
	var procPath string
	if b, err := procfs.ReadFile(procfs.PidPath(s.procDir, int(c.Pid), "cgroup"), s.buf); err == nil {
		if p, ok := procfs.CgroupV2Path(b); ok {
			procPath = string(p)
		}
	}
	dir := procfs.ContainerCgroupDir(s.cgRoot, procPath, c.FullID)
	if dir == "" {
		s.log.Debug("container cgroup not found", "container", c.Name, "pid", c.Pid, "proc_cgroup", procPath)
		return nil
	}
	return s.open(c.FullID, c.EntityID, false, c.Pid, dir, c.OwnNetns, sink)
}

func (s *Source) open(key, entity string, unit bool, pid int32, dir string, ownNetns bool, sink state.Sink) *target {
	t := &target{key: key, entity: entity, unit: unit, pid: pid, dir: dir}
	openf := func(name string) *procfs.File {
		f, err := procfs.Open(filepath.Join(dir, name))
		if err != nil {
			return nil
		}
		return f
	}
	t.fCPU = openf("cpu.stat")
	if t.fCPU == nil {
		return nil // not a cgroup v2 leaf we can read
	}
	t.fMem = openf("memory.current")
	t.fMemStat = openf("memory.stat")
	t.fIO = openf("io.stat")
	t.fPids = openf("pids.current")
	t.fEvents = openf("memory.events")
	if ownNetns && pid > 0 {
		if f, err := procfs.Open(s.procDir + "/" + strconv.Itoa(int(pid)) + "/net/dev"); err == nil {
			t.fNet = f
		}
	}
	t.idCPU = sink.Series(entity, "cpu.util")
	t.idMem = sink.Series(entity, "mem.used_bytes")
	t.idRd = sink.Series(entity, "blk.read_bps")
	t.idWr = sink.Series(entity, "blk.write_bps")
	t.idPids = sink.Series(entity, "pids.count")
	t.idOOM = sink.Series(entity, "oom.count")
	if t.fNet != nil {
		t.idRx = sink.Series(entity, "net.rx_bps")
		t.idTx = sink.Series(entity, "net.tx_bps")
	}
	return t
}

func (t *target) close() {
	for _, f := range []*procfs.File{t.fCPU, t.fMem, t.fMemStat, t.fIO, t.fPids, t.fEvents, t.fNet} {
		_ = f.Close()
	}
}

// gone reports errors that mean the cgroup or process disappeared.
func gone(err error) bool {
	return errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ESRCH) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrClosed)
}

func (t *target) sample(now time.Time, sink state.Sink, buf []byte) {
	if t.broken {
		return
	}
	b, err := t.fCPU.Read(buf)
	if err != nil {
		if gone(err) {
			t.broken = true
		}
		return
	}
	if u, ok := procfs.CPUUsageUsec(b); ok {
		if r, ok := t.cpu.Observe(now, u); ok {
			sink.Set(t.idCPU, float32(r/1e6))
		}
	}
	if t.fMem != nil {
		if b, err := t.fMem.Read(buf); err == nil {
			if cur, ok := procfs.Single(b); ok {
				var inactive uint64
				if t.fMemStat != nil {
					if b, err := t.fMemStat.Read(buf); err == nil {
						inactive, _ = procfs.InactiveFile(b)
					}
				}
				used := cur
				if inactive < cur {
					used = cur - inactive
				}
				sink.Set(t.idMem, float32(used))
			}
		}
	}
	if t.fIO != nil {
		if b, err := t.fIO.Read(buf); err == nil {
			rd, wr := procfs.ParseIOStat(b)
			if r, ok := t.rd.Observe(now, rd); ok {
				sink.Set(t.idRd, float32(r))
			}
			if r, ok := t.wr.Observe(now, wr); ok {
				sink.Set(t.idWr, float32(r))
			}
		}
	}
	if t.fPids != nil {
		if b, err := t.fPids.Read(buf); err == nil {
			if n, ok := procfs.Single(b); ok {
				sink.Set(t.idPids, float32(n))
			}
		}
	}
	if t.fEvents != nil {
		if b, err := t.fEvents.Read(buf); err == nil {
			if n, ok := procfs.OOMKills(b); ok {
				sink.Set(t.idOOM, float32(n))
				if t.unit && t.oomSeen && n > t.oomLast {
					sink.Emit(&v1.Event{
						Id:         fmt.Sprintf("evt:oom:%s:%d", t.entity, now.UnixMilli()),
						TsMs:       now.UnixMilli(),
						EntityId:   t.entity,
						EntityName: t.entity[len("unit:"):],
						Kind:       "unit.oom",
						Severity:   "critical",
						Message:    fmt.Sprintf("%d process(es) in %s were killed by the OOM killer", n-t.oomLast, t.entity[len("unit:"):]),
						Attrs:      map[string]string{"oom_kill": strconv.FormatUint(n, 10)},
					})
				}
				t.oomLast, t.oomSeen = n, true
			}
		}
	}
	if t.fNet != nil {
		if b, err := t.fNet.Read(buf); err == nil {
			if rx, tx, ok := procfs.SumNetDev(b); ok {
				if r, ok := t.rx.Observe(now, rx); ok {
					sink.Set(t.idRx, float32(r))
				}
				if r, ok := t.tx.Observe(now, tx); ok {
					sink.Set(t.idTx, float32(r))
				}
			}
		}
	}
}
