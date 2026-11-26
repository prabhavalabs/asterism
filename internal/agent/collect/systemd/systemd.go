// Package systemd discovers systemd services as workloads
// (Workload{kind: SYSTEMD_UNIT, id: "unit:<name>"}) from the cgroup v2 tree
// under <root>/sys/fs/cgroup/system.slice, and publishes their cgroups so
// the cgroup source samples them.
//
// The cgroup tree shows running units without any privilege or socket. For
// the state of inactive and failed units the agent runs
// `systemctl show --property=... -- <units>` with a fixed argv and
// validated unit names, on its own goroutine, when the agent runs on the
// host (host.root "/") and systemctl exists. D-Bus via godbus was not linked
// in: it costs ~0.4 MB of the 11 MiB binary budget for the same facts.
package systemd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/procfs"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// unitNameRe validates unit names passed to systemctl.
var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9@._:\\-]{1,200}\.service$`)

// systemctlPaths are the only binaries the agent will execute.
var systemctlPaths = []string{"/usr/bin/systemctl", "/bin/systemctl"}

// maxUnits caps the number of unit workloads.
const maxUnits = 500

// Source is the systemd collector.
type Source struct {
	cfg      config.Systemd
	interval time.Duration
	reg      *targets.Registry

	log       *slog.Logger
	sliceDir  string
	systemctl string
	// Exec runs systemctl (tests replace it).
	Exec func(ctx context.Context, bin string, args ...string) ([]byte, error)

	mu        sync.Mutex
	ext       map[string]UnitState // from systemctl, by unit name
	wanted    []string             // names to query
	lastState map[string]string
	known     map[string]bool
	buf       []byte
}

// UnitState is what systemctl show reports.
type UnitState struct {
	Load, Active, Sub, Result, Description string
	MainPID                                int
}

// New returns a systemd source.
func New(cfg config.Systemd, interval time.Duration, reg *targets.Registry) *Source {
	return &Source{cfg: cfg, interval: interval, reg: reg, ext: map[string]UnitState{}, lastState: map[string]string{}, known: map[string]bool{}, Exec: runExec}
}

func runExec(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"LANG=C", "SYSTEMD_PAGER=", "SYSTEMD_COLORS=0"}
	var out bytes.Buffer
	cmd.Stdout = &limitWriter{w: &out, n: 1 << 20}
	err := cmd.Run()
	return out.Bytes(), err
}

type limitWriter struct {
	w *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.n {
		p = p[:max(0, l.n-l.w.Len())]
	}
	l.w.Write(p)
	return len(p), nil
}

// Name implements state.Source.
func (s *Source) Name() string { return "systemd" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Init implements state.Source.
func (s *Source) Init(_ context.Context, env state.Env) ([]string, error) {
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.sliceDir = filepath.Join(env.HostPath(procfs.CgroupRoot), "system.slice")
	_, errSlice := os.Stat(s.sliceDir)
	_, errRun := os.Stat(env.HostPath("/run/systemd/system"))
	if errSlice != nil && errRun != nil {
		return nil, errors.New("systemd not detected (no /run/systemd/system and no system.slice cgroup)")
	}
	if errSlice != nil {
		return nil, fmt.Errorf("systemd cgroup tree unreadable at %s: %w", s.sliceDir, errSlice)
	}
	s.buf = make([]byte, 64<<10)
	caps := []string{"systemd"}
	if (env.Root == "" || env.Root == "/") && !s.cfg.DBus.Off() {
		for _, p := range systemctlPaths {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				s.systemctl = p
				caps = append(caps, "systemctl")
				break
			}
		}
	}
	return caps, nil
}

type unit struct {
	name string
	dir  string
	pids []int
}

// scan lists services under system.slice (and one level of sub-slices,
// where templated units such as postgresql@16-main.service live).
func (s *Source) scan() []unit {
	var out []unit
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			p := filepath.Join(dir, n)
			switch {
			case strings.HasSuffix(n, ".service"):
				u := unit{name: n, dir: p}
				if b, err := procfs.ReadFile(filepath.Join(p, "cgroup.procs"), s.buf); err == nil {
					for len(b) > 0 {
						var line []byte
						line, b = procfs.NextLine(b)
						if pid, ok := procfs.ParseUint(bytes.TrimSpace(line)); ok && pid > 0 {
							u.pids = append(u.pids, int(pid))
						}
					}
				}
				out = append(out, u)
			case strings.HasSuffix(n, ".slice") && depth == 0:
				walk(p, depth+1)
			}
		}
	}
	walk(s.sliceDir, 0)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func globAny(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

func (s *Source) selected(u unit) bool {
	if globAny(s.cfg.Exclude, u.name) {
		return false
	}
	switch s.cfg.Discover {
	case "all":
		return true
	case "include-only":
		return globAny(s.cfg.Include, u.name)
	default: // listening
		if globAny(s.cfg.Include, u.name) {
			return true
		}
		if s.reg != nil {
			for _, pid := range u.pids {
				if s.reg.IsListening(pid) {
					return true
				}
			}
		}
		return false
	}
}

// Tick implements state.Source.
func (s *Source) Tick(now time.Time, sink state.Sink) error {
	all := s.scan()
	s.mu.Lock()
	ext := s.ext
	s.mu.Unlock()

	var picked []unit
	present := map[string]bool{}
	for _, u := range all {
		present[u.name] = true
		if s.selected(u) && len(picked) < maxUnits {
			picked = append(picked, u)
		}
	}
	// Included units without a cgroup are inactive or failed: show them when
	// systemctl knows them.
	for _, g := range s.cfg.Include {
		if strings.ContainsAny(g, "*?[") || present[g] || globAny(s.cfg.Exclude, g) {
			continue
		}
		if st, ok := ext[g]; ok && st.Active != "" && st.Load != "" && st.Load != "not-found" {
			picked = append(picked, unit{name: g})
		}
	}

	seen := map[string]bool{}
	var regUnits []targets.Unit
	var wanted []string
	for _, u := range picked {
		id := "unit:" + u.name
		seen[id] = true
		wanted = append(wanted, u.name)
		w := s.workload(u, ext[u.name])
		sink.UpsertWorkload(w)
		if u.dir != "" && len(u.pids) > 0 {
			regUnits = append(regUnits, targets.Unit{Name: u.name, Dir: u.dir})
		}
		if prev := s.lastState[u.name]; prev != "" && prev != "failed" && w.State == "failed" {
			sink.Emit(&v1.Event{
				Id:         fmt.Sprintf("evt:unit.failed:%s:%d", u.name, now.UnixMilli()),
				TsMs:       now.UnixMilli(),
				EntityId:   id,
				EntityName: u.name,
				Kind:       "unit.failed",
				Severity:   "critical",
				Message:    fmt.Sprintf("%s entered the failed state (%s)", u.name, ext[u.name].Result),
				Attrs:      map[string]string{"result": ext[u.name].Result},
			})
		}
		s.lastState[u.name] = w.State
	}
	for _, g := range s.cfg.Include {
		if !strings.ContainsAny(g, "*?[") && !seen["unit:"+g] {
			wanted = append(wanted, g)
		}
	}
	for id := range s.known {
		if !seen[id] {
			sink.RemoveWorkload(id)
			delete(s.lastState, strings.TrimPrefix(id, "unit:"))
		}
	}
	s.known = seen
	if s.reg != nil {
		s.reg.SetUnits(regUnits)
	}
	s.mu.Lock()
	s.wanted = wanted
	s.mu.Unlock()
	return nil
}

func (s *Source) workload(u unit, ext UnitState) *v1.Workload {
	w := &v1.Workload{
		Id:          "unit:" + u.name,
		Kind:        v1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT,
		Name:        u.name,
		ServiceName: strings.TrimSuffix(u.name, ".service"),
		Replicas:    1,
		Networks:    []string{"host"},
		Labels:      map[string]string{"asterism.source": "systemd"},
	}
	switch {
	case ext.Active != "":
		w.State = ext.Active
		if ext.Sub != "" {
			w.Labels["systemd.sub_state"] = ext.Sub
		}
		w.Pid = int32(ext.MainPID)
	case len(u.pids) > 0:
		w.State = "active"
	default:
		w.State = "inactive"
	}
	if w.Pid == 0 && len(u.pids) > 0 {
		w.Pid = int32(u.pids[0])
	}
	switch w.State {
	case "active":
		w.Health = v1.HealthStatus_HEALTH_STATUS_HEALTHY
	case "activating", "reloading", "deactivating":
		w.Health = v1.HealthStatus_HEALTH_STATUS_DEGRADED
	default:
		w.Health = v1.HealthStatus_HEALTH_STATUS_DOWN
		w.Replicas = 0
	}
	return w
}

// Run implements state.Runner: it refreshes unit states with systemctl when
// available. Without systemctl it returns immediately (cgroup-only mode).
func (s *Source) Run(ctx context.Context, _ state.Sink) error {
	if s.systemctl == "" {
		return nil
	}
	for {
		s.refreshStates(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.interval):
		}
	}
}

func (s *Source) refreshStates(ctx context.Context) {
	s.mu.Lock()
	names := append([]string(nil), s.wanted...)
	s.mu.Unlock()
	var valid []string
	for _, n := range names {
		if unitNameRe.MatchString(n) {
			valid = append(valid, n)
		}
	}
	if len(valid) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append([]string{"show", "--no-pager", "--property=Id,LoadState,ActiveState,SubState,MainPID,Result,Description", "--"}, valid...)
	out, err := s.Exec(cctx, s.systemctl, args...)
	if err != nil && len(out) == 0 {
		s.log.Debug("systemctl show failed", "err", err)
		return
	}
	st := ParseShow(out)
	s.mu.Lock()
	s.ext = st
	s.mu.Unlock()
}

// ParseShow parses `systemctl show` output (blank-line separated blocks).
func ParseShow(b []byte) map[string]UnitState {
	out := map[string]UnitState{}
	var id string
	var cur UnitState
	flush := func() {
		if id != "" {
			out[id] = cur
		}
		id, cur = "", UnitState{}
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "Id":
			id = v
		case "LoadState":
			cur.Load = v
		case "ActiveState":
			cur.Active = v
		case "SubState":
			cur.Sub = v
		case "Result":
			cur.Result = v
		case "Description":
			cur.Description = v
		case "MainPID":
			cur.MainPID, _ = strconv.Atoi(v)
		}
	}
	flush()
	return out
}

// SetStates injects unit states (tests and discover).
func (s *Source) SetStates(m map[string]UnitState) {
	s.mu.Lock()
	s.ext = m
	s.mu.Unlock()
}

// Wanted returns the unit names systemctl would be asked about.
func (s *Source) Wanted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.wanted...)
}

// RefreshNow runs one systemctl refresh synchronously (discover).
func (s *Source) RefreshNow(ctx context.Context) { s.refreshStates(ctx) }

// HasSystemctl reports whether systemctl state is available.
func (s *Source) HasSystemctl() bool { return s.systemctl != "" }
