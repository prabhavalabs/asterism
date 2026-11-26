// Package process turns plain host processes matching sources.processes
// rules into Workload{kind: PROCESS, id: "proc:<name>"} with redacted
// command lines, and samples their CPU and memory.
//
// /proc is listed every tick, but only new pids are read (stat, cmdline,
// and exe/status when a rule needs them); matched pids are re-checked by
// start time so pid reuse is detected. Processes inside containers are
// never matched (the Docker source owns them).
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/pidstat"
	"github.com/prabhavalabs/asterism/internal/agent/collect/redact"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/procfs"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// maxCmdline caps the redacted command line kept in labels.
const maxCmdline = 256

// Source is the process collector.
type Source struct {
	rules    []config.ProcessMatch
	interval time.Duration
	reg      *targets.Registry
	red      *redact.Redactor

	log      *slog.Logger
	procDir  string
	users    map[uint32]string
	needExe  bool
	needUser bool
	btime    uint64

	cache   map[int]*proc
	sampler *pidstat.Sampler
	series  map[string][2]state.SeriesID
	buf     []byte
}

type proc struct {
	start uint64
	ppid  int64
	rule  int // -1 = no match
	comm  string
	exe   string
	args  []string
	user  string
}

// New returns a process source.
func New(rules []config.ProcessMatch, interval time.Duration, reg *targets.Registry, red *redact.Redactor) *Source {
	return &Source{rules: rules, interval: interval, reg: reg, red: red, cache: map[int]*proc{}, series: map[string][2]state.SeriesID{}}
}

// Name implements state.Source.
func (s *Source) Name() string { return "processes" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Init implements state.Source.
func (s *Source) Init(_ context.Context, env state.Env) ([]string, error) {
	if len(s.rules) == 0 {
		return nil, errors.New("no sources.processes.match rules")
	}
	for i, r := range s.rules {
		if r.Name == "" {
			return nil, fmt.Errorf("sources.processes.match[%d]: name is required", i)
		}
		s.needExe = s.needExe || r.Exe != ""
		s.needUser = s.needUser || r.User != ""
	}
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.procDir = env.HostPath("/proc")
	if _, err := os.Stat(s.procDir + "/1"); err != nil {
		return nil, fmt.Errorf("host procfs: %w", err)
	}
	s.users = ReadPasswd(env.HostPath("/etc/passwd"))
	s.btime = pidstat.BootTime(s.procDir)
	s.sampler = pidstat.New(s.procDir)
	s.buf = make([]byte, 32<<10)
	return []string{"processes"}, nil
}

// ReadPasswd maps uids to user names from an /etc/passwd file.
func ReadPasswd(p string) map[uint32]string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	m := map[uint32]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		if uid, err := strconv.ParseUint(f[2], 10, 32); err == nil {
			m[uint32(uid)] = f[0]
		}
	}
	return m
}

// Tick implements state.Source.
func (s *Source) Tick(now time.Time, sink state.Sink) error {
	pids, err := procfs.ListPids(s.procDir)
	if err != nil {
		return err
	}
	alive := make(map[int]bool, len(pids))
	groups := make([][]int, len(s.rules))
	for _, pid := range pids {
		alive[pid] = true
		p := s.cache[pid]
		if p != nil && p.rule >= 0 {
			// Re-check matched pids for reuse.
			if st, ok := s.sampler.Stat(pid); !ok || st.StartTime != p.start {
				p = nil
			}
		}
		if p == nil {
			p = s.inspect(pid)
			s.cache[pid] = p
		}
		if p.rule >= 0 {
			groups[p.rule] = append(groups[p.rule], pid)
		}
	}
	for pid := range s.cache {
		if !alive[pid] {
			delete(s.cache, pid)
		}
	}
	for i, r := range s.rules {
		s.publish(now, sink, r, groups[i])
	}
	s.sampler.Forget()
	return nil
}

// inspect reads a new pid and evaluates the rules.
func (s *Source) inspect(pid int) *proc {
	p := &proc{rule: -1}
	st, ok := s.sampler.Stat(pid)
	if !ok {
		return p
	}
	p.start, p.ppid, p.comm = st.StartTime, st.PPid, string(st.Comm)
	if st.PPid == 2 || pid == 2 {
		return p // kernel thread
	}
	b, err := procfs.ReadFile(procfs.PidPath(s.procDir, pid, "cmdline"), s.buf)
	if err != nil || len(b) == 0 {
		return p
	}
	p.args = procfs.SplitCmdline(b)
	if s.needExe {
		if exe, err := os.Readlink(procfs.PidPath(s.procDir, pid, "exe")); err == nil {
			p.exe = strings.TrimSuffix(exe, " (deleted)")
		}
	}
	if s.needUser {
		if b, err := procfs.ReadFile(procfs.PidPath(s.procDir, pid, "status"), s.buf); err == nil {
			if uid, ok := procfs.ParseStatusUID(b); ok {
				if n, ok := s.users[uid]; ok {
					p.user = n
				} else {
					p.user = strconv.FormatUint(uint64(uid), 10)
				}
			}
		}
	}
	for i, r := range s.rules {
		if s.matches(r, p) {
			if s.inContainer(pid) {
				break
			}
			p.rule = i
			break
		}
	}
	if p.rule < 0 {
		p.args, p.exe = nil, "" // keep the cache small
	}
	return p
}

func (s *Source) inContainer(pid int) bool {
	b, err := procfs.ReadFile(procfs.PidPath(s.procDir, pid, "cgroup"), s.buf)
	if err != nil {
		return false
	}
	return procfs.ContainerIDFromCgroup(string(bytes.TrimSpace(b))) != ""
}

func (s *Source) matches(r config.ProcessMatch, p *proc) bool {
	if r.Exe == "" && r.Cmdline == "" && r.User == "" {
		return p.comm == r.Name
	}
	if r.Exe != "" {
		exe := p.exe
		if exe == "" && len(p.args) > 0 {
			exe = p.args[0]
		}
		if !wildcard(r.Exe, exe) {
			return false
		}
	}
	if r.Cmdline != "" && !wildcard(r.Cmdline, strings.Join(p.args, " ")) {
		return false
	}
	if r.User != "" && r.User != p.user {
		return false
	}
	return true
}

// RoleFromName maps a rule role ("app", "worker", ...) to the enum.
func RoleFromName(r string) v1.WorkloadRole {
	switch strings.ToLower(r) {
	case "proxy":
		return v1.WorkloadRole_WORKLOAD_ROLE_PROXY
	case "app":
		return v1.WorkloadRole_WORKLOAD_ROLE_APP
	case "worker":
		return v1.WorkloadRole_WORKLOAD_ROLE_WORKER
	case "database", "db":
		return v1.WorkloadRole_WORKLOAD_ROLE_DATABASE
	case "cache":
		return v1.WorkloadRole_WORKLOAD_ROLE_CACHE
	case "queue":
		return v1.WorkloadRole_WORKLOAD_ROLE_QUEUE
	case "storage":
		return v1.WorkloadRole_WORKLOAD_ROLE_STORAGE
	}
	return v1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED
}

func (s *Source) publish(now time.Time, sink state.Sink, r config.ProcessMatch, pids []int) {
	id := "proc:" + r.Name
	sort.Ints(pids)
	inGroup := make(map[int]bool, len(pids))
	for _, pid := range pids {
		inGroup[pid] = true
	}
	var top []int
	for _, pid := range pids {
		if !inGroup[int(s.cache[pid].ppid)] {
			top = append(top, pid)
		}
	}
	w := &v1.Workload{
		Id:     id,
		Kind:   v1.WorkloadKind_WORKLOAD_KIND_PROCESS,
		Role:   RoleFromName(r.Role),
		Name:   r.Name,
		Labels: map[string]string{"asterism.source": "process"},
	}
	if len(top) == 0 {
		w.State, w.Health = "stopped", v1.HealthStatus_HEALTH_STATUS_DOWN
	} else {
		p := s.cache[top[0]]
		w.Pid = int32(top[0])
		w.State, w.Health = "running", v1.HealthStatus_HEALTH_STATUS_HEALTHY
		w.Replicas = uint32(len(top))
		w.StartedAtMs = pidstat.StartedAtMs(s.btime, p.start)
		cmd := strings.Join(s.red.Args(p.args), " ")
		if len(cmd) > maxCmdline {
			cmd = cmd[:maxCmdline] + "…"
		}
		w.Labels["process.cmdline"] = cmd
		if p.exe != "" {
			w.Labels["process.exe"] = p.exe
		}
		if p.user != "" {
			w.Labels["process.user"] = p.user
		}
		w.Networks = []string{"host"}
	}
	sink.UpsertWorkload(w)
	if s.reg != nil {
		s.reg.SetProcs(id, pids)
	}
	if len(pids) == 0 {
		return
	}
	ids, ok := s.series[id]
	if !ok {
		ids = [2]state.SeriesID{sink.Series(id, "cpu.util"), sink.Series(id, "mem.used_bytes")}
		s.series[id] = ids
	}
	u := s.sampler.Sample(now, pids)
	if u.HaveRate {
		sink.Set(ids[0], float32(u.CPU))
	}
	if u.Alive > 0 {
		sink.Set(ids[1], float32(u.RSS))
	}
}

// Matched returns the matched pids per workload id (for discover).
func (s *Source) Matched() map[string][]int {
	out := map[string][]int{}
	for pid, p := range s.cache {
		if p.rule >= 0 {
			id := "proc:" + s.rules[p.rule].Name
			out[id] = append(out[id], pid)
		}
	}
	return out
}
