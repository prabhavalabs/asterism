// Package pm2 discovers pm2-managed Node apps from <home>/pids/*.pid and
// /proc/<pid> only. dump.pm2 and the pm2 RPC sockets are never read: the
// dump contains every app's environment values.
//
// Pid files are named "<app>-<pm_id>.pid"; instances of one app (cluster
// mode) become one Workload{kind: PROCESS, id: "proc:<app>"} with replicas.
package pm2

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/pidstat"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Source is the pm2 collector.
type Source struct {
	cfg      config.PM2
	interval time.Duration
	reg      *targets.Registry

	log     *slog.Logger
	env     state.Env
	homes   []string // host paths of .pm2 directories
	procDir string
	btime   uint64
	sampler *pidstat.Sampler
	known   map[string]bool
	series  map[string][2]state.SeriesID
}

// New returns a pm2 source.
func New(cfg config.PM2, interval time.Duration, reg *targets.Registry) *Source {
	return &Source{cfg: cfg, interval: interval, reg: reg, known: map[string]bool{}, series: map[string][2]state.SeriesID{}}
}

// Name implements state.Source.
func (s *Source) Name() string { return "pm2" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Init implements state.Source. With no homes configured it looks for
// /root/.pm2 and /home/*/.pm2.
func (s *Source) Init(_ context.Context, env state.Env) ([]string, error) {
	s.env = env
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.procDir = env.HostPath("/proc")
	homes := s.cfg.Homes
	if len(homes) == 0 {
		homes = append([]string{"/root/.pm2"}, globHost(env, "/home/*/.pm2")...)
	}
	for _, h := range homes {
		if st, err := os.Stat(env.HostPath(filepath.Join(h, "pids"))); err == nil && st.IsDir() {
			s.homes = append(s.homes, h)
		}
	}
	if len(s.homes) == 0 {
		return nil, errors.New("no pm2 home with a pids/ directory found")
	}
	s.btime = pidstat.BootTime(s.procDir)
	s.sampler = pidstat.New(s.procDir)
	return []string{"pm2"}, nil
}

// globHost globs a host path pattern and returns host paths.
func globHost(env state.Env, pattern string) []string {
	m, _ := filepath.Glob(env.HostPath(pattern))
	prefix := env.HostPath("/")
	out := make([]string, 0, len(m))
	for _, p := range m {
		if env.Root == "" || env.Root == "/" {
			out = append(out, p)
			continue
		}
		out = append(out, "/"+strings.TrimPrefix(strings.TrimPrefix(p, prefix), "/"))
	}
	return out
}

// Homes returns the discovered pm2 homes (host paths).
func (s *Source) Homes() []string { return s.homes }

// AppName strips the pm_id suffix: "shop-api-3.pid" -> "shop-api".
func AppName(file string) string {
	n := strings.TrimSuffix(file, ".pid")
	if i := strings.LastIndexByte(n, '-'); i > 0 {
		if _, err := strconv.Atoi(n[i+1:]); err == nil {
			return n[:i]
		}
	}
	return n
}

type app struct {
	home string
	pids []int
}

// Tick implements state.Source.
func (s *Source) Tick(now time.Time, sink state.Sink) error {
	apps := map[string]*app{}
	for _, h := range s.homes {
		dir := s.env.HostPath(filepath.Join(h, "pids"))
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
				continue
			}
			name := AppName(e.Name())
			if name == "" {
				continue
			}
			a := apps[name]
			if a == nil {
				a = &app{home: h}
				apps[name] = a
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || len(b) > 32 {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 0 {
				continue
			}
			if st, ok := s.sampler.Stat(pid); ok && st.State != 'Z' {
				a.pids = append(a.pids, pid)
			}
		}
	}
	names := make([]string, 0, len(apps))
	for n := range apps {
		names = append(names, n)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	for _, n := range names {
		a := apps[n]
		id := "proc:" + n
		seen[id] = true
		sort.Ints(a.pids)
		w := &v1.Workload{
			Id:     id,
			Kind:   v1.WorkloadKind_WORKLOAD_KIND_PROCESS,
			Name:   n,
			Labels: map[string]string{"asterism.source": "pm2", "pm2.home": a.home},
		}
		if len(a.pids) == 0 {
			w.State, w.Health = "stopped", v1.HealthStatus_HEALTH_STATUS_DOWN
		} else {
			w.State, w.Health = "online", v1.HealthStatus_HEALTH_STATUS_HEALTHY
			w.Pid, w.Replicas = int32(a.pids[0]), uint32(len(a.pids))
			if st, ok := s.sampler.Stat(a.pids[0]); ok {
				w.StartedAtMs = pidstat.StartedAtMs(s.btime, st.StartTime)
			}
			w.Networks = []string{"host"}
		}
		sink.UpsertWorkload(w)
		if s.reg != nil {
			s.reg.SetProcs(id, a.pids)
		}
		if len(a.pids) > 0 {
			ids, ok := s.series[id]
			if !ok {
				ids = [2]state.SeriesID{sink.Series(id, "cpu.util"), sink.Series(id, "mem.used_bytes")}
				s.series[id] = ids
			}
			u := s.sampler.Sample(now, a.pids)
			if u.HaveRate {
				sink.Set(ids[0], float32(u.CPU))
			}
			sink.Set(ids[1], float32(u.RSS))
		}
	}
	for id := range s.known {
		if !seen[id] {
			sink.RemoveWorkload(id) // pm2 delete removes the pid file
			if s.reg != nil {
				s.reg.SetProcs(id, nil)
			}
		}
	}
	s.known = seen
	s.sampler.Forget()
	return nil
}
