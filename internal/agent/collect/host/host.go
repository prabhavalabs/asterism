// Package host samples host-wide metrics from procfs (via host.root) and
// publishes static HostInfo and statfs filesystem usage.
//
// Series (entity "host"): cpu.util, cpu.steal (0-1), mem.used_bytes,
// mem.used_ratio, swap.used_bytes, load.1, load.5, load.15, net.rx_bps,
// net.tx_bps (excluding host.interfaces.exclude), io.read_bps, io.write_bps
// (whole disks), disk.used_ratio for the first filesystem and
// disk.used_ratio{<mount>} for the others.
package host

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/procfs"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// fsRefresh bounds how often unchanged filesystem usage is re-sent as
// inventory (usage also streams as the disk.used_ratio series).
const fsRefresh = 5 * time.Minute

// Source is the host collector.
type Source struct {
	interval time.Duration
	cfg      *config.Config
	log      *slog.Logger
	env      state.Env

	buf                             []byte
	fStat, fMem, fLoad, fNet, fDisk *procfs.File
	exclude                         []string
	excluded                        map[string]bool
	wholeDisks                      map[string]bool
	prevCPU                         procfs.CPUTimes
	haveCPU                         bool
	rx, tx, rd, wr                  procfs.Counter
	ids                             hostSeries
	fsIDs                           map[string]state.SeriesID
	mounts                          []mount
	lastFS                          []*v1.FilesystemUsage
	lastFSAt                        time.Time
	infoSent                        bool
	info                            *v1.HostInfo
	fstypes                         map[string]string
}

type hostSeries struct {
	cpu, steal, memUsed, memRatio, swapUsed, l1, l5, l15, rx, tx, rd, wr state.SeriesID
}

type mount struct {
	name string   // host mount point, e.g. "/"
	try  []string // statfs candidates on the agent's filesystem
	path string   // the candidate that worked
}

// New returns a host source.
func New(cfg *config.Config, interval time.Duration) *Source {
	return &Source{cfg: cfg, interval: interval, excluded: map[string]bool{}, fsIDs: map[string]state.SeriesID{}}
}

// Name implements state.Source.
func (s *Source) Name() string { return "host" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Class implements state.Classed.
func (s *Source) Class() state.Class { return state.ClassHost }

// Init implements state.Source.
func (s *Source) Init(_ context.Context, env state.Env) ([]string, error) {
	s.env = env
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.buf = make([]byte, procfs.BufSize)
	var err error
	if s.fStat, err = procfs.Open(env.HostPath("/proc/stat")); err != nil {
		return nil, fmt.Errorf("host procfs: %w", err)
	}
	s.fMem, _ = procfs.Open(env.HostPath("/proc/meminfo"))
	s.fLoad, _ = procfs.Open(env.HostPath("/proc/loadavg"))
	// /proc/1/net/dev is the host network namespace even when the agent
	// runs in its own (container) namespace with the host /proc mounted.
	if s.fNet, err = procfs.Open(env.HostPath("/proc/1/net/dev")); err != nil {
		s.fNet, _ = procfs.Open(env.HostPath("/proc/net/dev"))
	}
	s.fDisk, _ = procfs.Open(env.HostPath("/proc/diskstats"))
	if s.cfg != nil {
		s.exclude = s.cfg.Host.Interfaces.Exclude
	}
	s.wholeDisks = readWholeDisks(env.HostPath("/sys/block"))
	s.fstypes = readMounts(env.HostPath("/proc/1/mounts"))
	fss := []string{"/"}
	if s.cfg != nil && len(s.cfg.Host.Filesystems) > 0 {
		fss = s.cfg.Host.Filesystems
	}
	for _, m := range fss {
		s.mounts = append(s.mounts, mount{name: m, try: statfsCandidates(env, m)})
	}
	s.ids = hostSeries{}
	s.info = s.hostInfo()
	return []string{"procfs"}, nil
}

// statfsCandidates lists where a host mount can be measured from: the host
// init's root (/proc/1/root, needs CAP_SYS_PTRACE in a container), then the
// mount under host.root (an explicit bind mount), then the plain path when
// the agent runs on the host.
func statfsCandidates(env state.Env, m string) []string {
	if env.Root == "" || env.Root == "/" {
		return []string{m}
	}
	return []string{
		filepath.Join(env.HostPath("/proc/1/root"), m),
		filepath.Join(env.Root, "rootfs", m),
		env.HostPath(m),
	}
}

func readWholeDisks(sysBlock string) map[string]bool {
	ents, err := os.ReadDir(sysBlock)
	if err != nil {
		return nil
	}
	m := map[string]bool{}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") || strings.HasPrefix(n, "zram") ||
			strings.HasPrefix(n, "dm-") || strings.HasPrefix(n, "sr") || strings.HasPrefix(n, "fd") || strings.HasPrefix(n, "md") || strings.HasPrefix(n, "nbd") {
			continue
		}
		m[n] = true
	}
	return m
}

func readMounts(p string) map[string]string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for len(b) > 0 {
		var line []byte
		line, b = procfs.NextLine(b)
		f := bytes.Fields(line)
		if len(f) >= 3 {
			m[string(f[1])] = string(f[2])
		}
	}
	return m
}

func (s *Source) netExcluded(name []byte) bool {
	if v, ok := s.excluded[string(name)]; ok {
		return v
	}
	n := string(name)
	ex := false
	for _, g := range s.exclude {
		if ok, _ := path.Match(g, n); ok {
			ex = true
			break
		}
	}
	s.excluded[n] = ex
	return ex
}

// Tick implements state.Source.
func (s *Source) Tick(now time.Time, sink state.Sink) error {
	if s.ids.cpu == 0 {
		s.ids = hostSeries{
			cpu: sink.Series("host", "cpu.util"), steal: sink.Series("host", "cpu.steal"),
			memUsed: sink.Series("host", "mem.used_bytes"), memRatio: sink.Series("host", "mem.used_ratio"),
			swapUsed: sink.Series("host", "swap.used_bytes"),
			l1:       sink.Series("host", "load.1"), l5: sink.Series("host", "load.5"), l15: sink.Series("host", "load.15"),
			rx: sink.Series("host", "net.rx_bps"), tx: sink.Series("host", "net.tx_bps"),
			rd: sink.Series("host", "io.read_bps"), wr: sink.Series("host", "io.write_bps"),
		}
	}
	if !s.infoSent && s.info != nil {
		sink.SetHostInfo(s.info)
		s.infoSent = true
	}
	b, err := s.fStat.Read(s.buf)
	if err != nil {
		return fmt.Errorf("read /proc/stat: %w", err)
	}
	if cpu, _, ok := procfs.ParseStat(b); ok {
		if s.haveCPU {
			dt := float64(procfs.Delta(s.prevCPU.Total(), cpu.Total()))
			if dt > 0 {
				idle := float64(procfs.Delta(s.prevCPU.IdleAll(), cpu.IdleAll()))
				steal := float64(procfs.Delta(s.prevCPU.Steal, cpu.Steal))
				sink.Set(s.ids.cpu, float32(clamp01((dt-idle)/dt))) // steal counts as busy, as in the local and SSH collectors
				sink.Set(s.ids.steal, float32(clamp01(steal/dt)))
			}
		}
		s.prevCPU, s.haveCPU = cpu, true
	}
	if s.fMem != nil {
		if b, err := s.fMem.Read(s.buf); err == nil {
			if m, ok := procfs.ParseMeminfo(b); ok {
				sink.Set(s.ids.memUsed, float32(m.Used()))
				if m.MemTotal > 0 {
					sink.Set(s.ids.memRatio, float32(float64(m.Used())/float64(m.MemTotal)))
				}
				sink.Set(s.ids.swapUsed, float32(m.SwapUsed()))
			}
		}
	}
	if s.fLoad != nil {
		if b, err := s.fLoad.Read(s.buf); err == nil {
			if l1, l5, l15, ok := procfs.ParseLoadavg(b); ok {
				sink.Set(s.ids.l1, float32(l1))
				sink.Set(s.ids.l5, float32(l5))
				sink.Set(s.ids.l15, float32(l15))
			}
		}
	}
	if s.fNet != nil {
		if b, err := s.fNet.Read(s.buf); err == nil {
			var rx, tx uint64
			procfs.ParseNetDev(b, func(name []byte, r, t uint64) {
				if !s.netExcluded(name) {
					rx += r
					tx += t
				}
			})
			if r, ok := s.rx.Observe(now, rx); ok {
				sink.Set(s.ids.rx, float32(r))
			}
			if r, ok := s.tx.Observe(now, tx); ok {
				sink.Set(s.ids.tx, float32(r))
			}
		}
	}
	if s.fDisk != nil {
		if b, err := s.fDisk.Read(s.buf); err == nil {
			var rd, wr uint64
			procfs.ParseDiskstats(b, func(name []byte, r, w uint64) {
				whole := s.wholeDisks[string(name)]
				if s.wholeDisks == nil {
					whole = procfs.IsWholeDiskName(name)
				}
				if whole {
					rd += r
					wr += w
				}
			})
			if r, ok := s.rd.Observe(now, rd*procfs.SectorSize); ok {
				sink.Set(s.ids.rd, float32(r))
			}
			if r, ok := s.wr.Observe(now, wr*procfs.SectorSize); ok {
				sink.Set(s.ids.wr, float32(r))
			}
		}
	}
	s.filesystems(now, sink)
	return nil
}

func (s *Source) filesystems(now time.Time, sink state.Sink) {
	if len(s.mounts) == 0 {
		return
	}
	fs := make([]*v1.FilesystemUsage, 0, len(s.mounts))
	for i := range s.mounts {
		m := &s.mounts[i]
		var total, used uint64
		var err error
		if m.path != "" {
			total, used, _, err = statfs(m.path)
		}
		if m.path == "" || err != nil {
			m.path = ""
			for _, c := range m.try {
				if total, used, _, err = statfs(c); err == nil && total > 0 {
					m.path = c
					break
				}
			}
		}
		if m.path == "" || total == 0 {
			continue
		}
		id, ok := s.fsIDs[m.name]
		if !ok {
			metric := "disk.used_ratio"
			if i > 0 {
				metric = "disk.used_ratio{" + m.name + "}"
			}
			id = sink.Series("host", metric)
			s.fsIDs[m.name] = id
		}
		sink.Set(id, float32(float64(used)/float64(total)))
		fs = append(fs, &v1.FilesystemUsage{Mount: m.name, Fstype: s.fstypes[m.name], TotalBytes: total, UsedBytes: used})
	}
	if s.fsChanged(fs) || now.Sub(s.lastFSAt) >= fsRefresh {
		sink.ReplaceFilesystems(fs)
		s.lastFS, s.lastFSAt = fs, now
	}
}

// fsChanged reports a different mount set or a usage change of at least
// 0.5% of the filesystem size (the series carries finer movement).
func (s *Source) fsChanged(fs []*v1.FilesystemUsage) bool {
	if len(fs) != len(s.lastFS) {
		return true
	}
	for i, f := range fs {
		o := s.lastFS[i]
		if f.Mount != o.Mount || f.TotalBytes != o.TotalBytes {
			return true
		}
		d := int64(f.UsedBytes) - int64(o.UsedBytes)
		if d < 0 {
			d = -d
		}
		if f.TotalBytes > 0 && float64(d)/float64(f.TotalBytes) >= 0.005 {
			return true
		}
	}
	return false
}

// HostInfo returns the static host facts gathered at Init.
func (s *Source) HostInfo() *v1.HostInfo { return s.info }

func (s *Source) hostInfo() *v1.HostInfo {
	env := s.env
	h := &v1.HostInfo{Id: "host", SourceMode: "agent", Arch: unameArch(runtime.GOARCH)}
	if s.cfg != nil && s.cfg.Host.Name != "" {
		h.Hostname = s.cfg.Host.Name
	} else if b, err := os.ReadFile(env.HostPath("/etc/hostname")); err == nil && len(bytes.TrimSpace(b)) > 0 {
		h.Hostname = strings.TrimSpace(string(b))
	} else if n, err := os.Hostname(); err == nil {
		h.Hostname = n
	}
	h.Os = osRelease(env.HostPath("/etc/os-release"))
	if b, err := os.ReadFile(env.HostPath("/proc/sys/kernel/osrelease")); err == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(env.HostPath("/proc/cpuinfo")); err == nil {
		h.CpuModel, h.Cores = cpuInfo(b)
	}
	if h.Cores == 0 {
		h.Cores = uint32(runtime.NumCPU())
	}
	if s.fMem != nil {
		if b, err := s.fMem.Read(s.buf); err == nil {
			if m, ok := procfs.ParseMeminfo(b); ok {
				h.MemTotalBytes, h.SwapTotalBytes = m.MemTotal, m.SwapTotal
			}
		}
	}
	if b, err := s.fStat.Read(s.buf); err == nil {
		if _, btime, _ := procfs.ParseStat(b); btime > 0 {
			h.BootTimeMs = int64(btime) * 1000
		}
	}
	if len(s.mounts) > 0 {
		for _, c := range s.mounts[0].try {
			if total, _, _, err := statfs(c); err == nil && total > 0 {
				h.DiskTotalBytes = total
				break
			}
		}
	}
	return h
}

func osRelease(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var name, version string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "PRETTY_NAME":
			return v
		case "NAME":
			name = v
		case "VERSION":
			version = v
		}
	}
	return strings.TrimSpace(name + " " + version)
}

// cpuInfo returns the CPU model and the number of logical CPUs.
func cpuInfo(b []byte) (string, uint32) {
	var model string
	var n uint32
	for len(b) > 0 {
		var line []byte
		line, b = procfs.NextLine(b)
		k, v, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		key := string(bytes.TrimSpace(k))
		switch key {
		case "processor":
			n++
		case "model name", "Model", "cpu model", "Hardware":
			if model == "" {
				model = string(bytes.TrimSpace(v))
			}
		}
	}
	return model, n
}

func unameArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i686"
	case "arm":
		return "armv7l"
	default:
		return goarch
	}
}

func clamp01(v float64) float64 { return min(max(v, 0), 1) }
