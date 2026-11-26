package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/prabhavalabs/asterism/internal/agent/collect/cgroup"
	"github.com/prabhavalabs/asterism/internal/agent/collect/docker"
	"github.com/prabhavalabs/asterism/internal/agent/collect/systemd"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// discoverTimeout bounds the whole dry run.
const discoverTimeout = 60 * time.Second

// cmdDiscover is the one-shot dry run: it initialises every collector,
// runs each once (twice where a second pass adds facts), prints what was
// found, and suggests projects: YAML. It never contacts the hub.
func cmdDiscover(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadConfig("discover", args, stderr)
	if cfg == nil {
		return code
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
	defer cancel()
	env := envFor(cfg)
	env.Logger = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	srcs := buildSources(cfg, env)
	store := state.NewStore(state.Options{Projects: cfg.Projects})
	p := func(format string, a ...any) { fmt.Fprintf(stdout, format, a...) }

	p("# asterism-agent discover (%s)\n", versionString())
	p("# host.root: %s   config: %s\n\n", cfg.Host.Root, displayPath(cfg))
	p("sources:\n")
	var live []state.Source
	for _, s := range srcs {
		senv := env
		senv.Logger = env.Logger.With("source", s.Name())
		caps, err := s.Init(ctx, senv)
		if err != nil {
			p("  %-10s off   %v\n", s.Name(), err)
			continue
		}
		p("  %-10s on    every %-6s caps=%s\n", s.Name(), intervalName(s), strings.Join(caps, ","))
		live = append(live, s)
	}

	sinks := map[string]*state.Handle{}
	now := time.Now()
	tick := func(pass int) {
		for _, s := range live {
			h := sinks[s.Name()]
			if h == nil {
				h = store.Sink(s.Name())
				sinks[s.Name()] = h
			}
			switch src := s.(type) {
			case *docker.Source:
				if pass == 0 {
					if err := src.SyncOnce(ctx, h, cfg.Intervals.DockerDiskUsage.D() > 0); err != nil {
						p("  %s sync: %v\n", s.Name(), err)
					}
				}
				continue
			case *systemd.Source:
				if pass == 1 && src.HasSystemctl() {
					src.RefreshNow(ctx)
				}
			}
			if s.Interval() > 0 {
				if err := s.Tick(now, h); err != nil {
					p("  %s tick: %v\n", s.Name(), err)
				}
				h.Commit(now, true)
			}
		}
	}
	tick(0)
	tick(1) // listeners -> systemd "listening" discovery, systemctl states

	snap := store.Snapshot()
	// Decode the metrics exactly as the hub would, for a sample of values.
	vals := map[state.SeriesKey]float32{}
	_ = state.NewDecoder().Apply(store.TakeMetrics(), func(pt state.Point) { vals[pt.Key] = pt.Value })
	val := func(e, m string) string {
		v, ok := vals[state.SeriesKey{Entity: e, Metric: m}]
		if !ok {
			return "-"
		}
		if strings.HasSuffix(m, "_bytes") {
			return bytesStr(uint64(v))
		}
		return fmt.Sprintf("%.3g", v)
	}
	printHost(p, snap.GetHost(), snap.GetFilesystems())
	p("  metrics: %d series · mem.used %s (%s) · load.1 %s · disk.used_ratio %s (rates need a second sample)\n",
		len(vals), val("host", "mem.used_bytes"), val("host", "mem.used_ratio"), val("host", "load.1"), val("host", "disk.used_ratio"))
	for _, s := range live {
		if cg, ok := s.(*cgroup.Source); ok {
			ents := cg.Entities()
			p("  cgroup: sampling %d container(s)/unit(s)\n", len(ents))
			for _, id := range sortedKeys(ents) {
				p("    %-36s mem %-10s pids %-4s %s\n", id, val(id, "mem.used_bytes"), val(id, "pids.count"), ents[id])
			}
		}
	}
	printDocker(p, snap)
	printWorkloads(p, snap.GetWorkloads())
	printListeners(p, snap.GetListeners())
	printRoutes(p, snap.GetRoutes())
	if len(snap.GetProjectRules()) > 0 {
		p("\nconfigured projects:\n")
		for _, r := range snap.GetProjectRules() {
			p("  %-16s %d member(s): %s\n", r.Name, len(r.MemberIds), strings.Join(r.MemberIds, " "))
		}
	}
	p("\n# Suggested projects (paste under projects: in %s and adjust):\n", config.DefaultPath)
	enc := yaml.NewEncoder(stdout)
	enc.SetIndent(2)
	_ = enc.Encode(struct {
		Projects []suggestion `yaml:"projects"`
	}{suggestProjects(snap)})
	_ = enc.Close()
	return 0
}

func intervalName(s state.Source) string {
	if s.Interval() == 0 {
		return "events"
	}
	return config.Duration(s.Interval()).String()
}

type printer func(format string, a ...any)

func printHost(p printer, h *v1.HostInfo, fs []*v1.FilesystemUsage) {
	if h == nil {
		return
	}
	p("\nhost: %s · %s · kernel %s · %s · %d cores (%s) · %s RAM\n", h.Hostname, h.Os, h.Kernel, h.Arch, h.Cores, h.CpuModel, bytesStr(h.MemTotalBytes))
	for _, f := range fs {
		p("  fs %-12s %-8s %s of %s used\n", f.Mount, f.Fstype, bytesStr(f.UsedBytes), bytesStr(f.TotalBytes))
	}
}

func printDocker(p printer, snap *v1.InventorySnapshot) {
	if e := snap.GetEngine(); e != nil {
		if e.Available {
			p("\ndocker: %s on %s (%s, cgroup %s v%s) · %d images · %d volumes · %d networks\n", e.ServerVersion, e.OperatingSystem, e.StorageDriver, e.CgroupDriver, e.CgroupVersion, len(snap.GetImages()), len(snap.GetVolumes()), len(snap.GetNetworks()))
		} else {
			p("\ndocker: unavailable (%s)\n", e.UnavailableReason)
		}
	}
	if du := snap.GetDiskUsage(); du != nil {
		p("  disk usage: %s total, %s reclaimable\n", bytesStr(du.TotalSizeBytes), bytesStr(du.TotalReclaimableBytes))
	}
	cs := snap.GetContainers()
	if len(cs) == 0 {
		return
	}
	p("containers (%d):\n", len(cs))
	for _, c := range cs {
		var ports []string
		for _, pt := range c.Ports {
			if pt.Published {
				ports = append(ports, fmt.Sprintf("%s:%d->%d/%s", pt.HostIp, pt.HostPort, pt.ContainerPort, pt.Proto))
			}
		}
		prot := ""
		if c.Protected {
			prot = " [protected: " + c.ProtectedReason + "]"
		}
		p("  %-28s %-9s %-18s %-30s env=%d redacted_labels=%d %s%s\n", c.Name, strings.ToLower(strings.TrimPrefix(c.State.String(), "CONTAINER_STATE_")), c.ProjectId, trunc(c.Image, 30), len(c.EnvKeys), c.RedactedLabelCount, strings.Join(ports, ","), prot)
	}
}

func printWorkloads(p printer, ws []*v1.Workload) {
	if len(ws) == 0 {
		return
	}
	p("\nworkloads (%d):\n", len(ws))
	for _, w := range ws {
		kind := strings.ToLower(strings.TrimPrefix(w.Kind.String(), "WORKLOAD_KIND_"))
		extra := w.Labels["process.cmdline"]
		if extra == "" {
			extra = w.Labels["pm2.home"]
		}
		p("  %-36s %-13s %-9s pid=%-7d %s\n", w.Id, kind, w.State, w.Pid, trunc(extra, 80))
	}
}

func printListeners(p printer, ls []*v1.PortBinding) {
	if len(ls) == 0 {
		return
	}
	p("\nlisteners (%d):\n", len(ls))
	for _, l := range ls {
		exp := strings.ToLower(strings.TrimPrefix(l.Exposure.String(), "PORT_EXPOSURE_"))
		p("  %-4s %-28s %-9s %-28s %s\n", l.Proto, hostPort(l.HostIp, l.HostPort), exp, l.WorkloadId, l.ProcessName)
	}
}

func hostPort(ip string, port uint32) string {
	if strings.Contains(ip, ":") {
		return fmt.Sprintf("[%s]:%d", ip, port)
	}
	return fmt.Sprintf("%s:%d", ip, port)
}

func printRoutes(p printer, rs []*v1.IngressRoute) {
	if len(rs) == 0 {
		return
	}
	p("\nroutes (%d):\n", len(rs))
	for _, r := range rs {
		var ups []string
		for _, u := range r.Upstreams {
			ups = append(ups, u.Address)
		}
		p("  %-44s :%-5d tls=%-5v -> %s\n", r.Id, r.ListenPort, r.Tls, strings.Join(ups, ", "))
	}
}

// suggestion is one suggested projects: entry (field order as in the docs).
type suggestion struct {
	Name     string              `yaml:"name"`
	Category string              `yaml:"category"`
	Members  map[string][]string `yaml:"members"`
}

// suggestProjects proposes projects: compose projects as-is, proxies and
// tunnels as "edge", databases and caches as "data", process apps by name.
func suggestProjects(snap *v1.InventorySnapshot) []suggestion {
	compose := map[string]bool{}
	for _, c := range snap.GetContainers() {
		if c.ComposeProject != "" {
			compose[c.ComposeProject] = true
		}
	}
	out := []suggestion{}
	for _, name := range sortedSet(compose) {
		out = append(out, suggestion{name, "production", map[string][]string{"compose": {name}}})
	}
	var edge, data, other []string
	for _, w := range snap.GetWorkloads() {
		if w.Kind != v1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT {
			continue
		}
		n := w.Name
		switch {
		case hasAny(n, "nginx", "caddy", "haproxy", "traefik", "cloudflared", "tailscale"):
			edge = append(edge, n)
		case hasAny(n, "postgres", "mysql", "mariadb", "redis", "valkey", "mongo", "clickhouse", "memcached"):
			data = append(data, n)
		default:
			other = append(other, n)
		}
	}
	if len(edge) > 0 {
		out = append(out, suggestion{"edge", "infra", map[string][]string{"units": edge}})
	}
	if len(data) > 0 {
		out = append(out, suggestion{"data", "infra", map[string][]string{"units": data}})
	}
	if len(other) > 0 {
		out = append(out, suggestion{"services", "production", map[string][]string{"units": other}})
	}
	procs := map[string][]string{}
	for _, w := range snap.GetWorkloads() {
		if w.Kind != v1.WorkloadKind_WORKLOAD_KIND_PROCESS {
			continue
		}
		key := "processes"
		if w.Labels["asterism.source"] == "pm2" {
			key = "pm2"
		}
		procs[key] = append(procs[key], w.Name)
	}
	if len(procs) > 0 {
		for _, v := range procs {
			sort.Strings(v)
		}
		out = append(out, suggestion{"apps", "production", procs})
	}
	return out
}

func hasAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func bytesStr(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
