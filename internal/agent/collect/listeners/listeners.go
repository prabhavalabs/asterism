// Package listeners scans the host's listening sockets from
// <root>/proc/1/net/{tcp,tcp6,udp,udp6} (the host network namespace) and
// maps them to PortBindings with an exposure classification.
//
// Owners are resolved through /proc/<pid>/fd socket links, but only when a
// listening inode is new; resolving needs CAP_SYS_PTRACE (and usually
// CAP_DAC_READ_SEARCH) for other users' processes. Without it, ports still
// show, attributed to "host" or to a container by its published port.
package listeners

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/procfs"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// retryUnresolved is how long an inode no /proc/<pid>/fd walk could
// attribute is left alone before another walk is attempted for it.
const retryUnresolved = 10 * time.Minute

// Source is the listener collector.
type Source struct {
	cfg      config.Ports
	interval time.Duration
	reg      *targets.Registry

	log     *slog.Logger
	procDir string
	tables  []table
	resolve bool

	owners     map[uint64]owner     // inode -> owner
	unresolved map[uint64]time.Time // inode -> last failed walk
	buf        []byte
}

type table struct {
	path  string
	proto string
	udp   bool
}

type owner struct {
	pid  int
	comm string
	cg   string // cgroup path
}

// New returns a listener source.
func New(cfg config.Ports, interval time.Duration, reg *targets.Registry) *Source {
	return &Source{cfg: cfg, interval: interval, reg: reg, owners: map[uint64]owner{}, unresolved: map[uint64]time.Time{}}
}

// Name implements state.Source.
func (s *Source) Name() string { return "listeners" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Init implements state.Source.
func (s *Source) Init(_ context.Context, env state.Env) ([]string, error) {
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.procDir = env.HostPath("/proc")
	protos := s.cfg.Protocols
	if len(protos) == 0 {
		protos = []string{"tcp", "udp"}
	}
	for _, p := range protos {
		for _, v := range []string{"", "6"} {
			t := table{path: env.HostPath("/proc/1/net/" + p + v), proto: p, udp: p == "udp"}
			if _, err := os.Stat(t.path); err == nil {
				s.tables = append(s.tables, t)
			}
		}
	}
	if len(s.tables) == 0 {
		return nil, fmt.Errorf("no readable socket tables under %s/1/net", s.procDir)
	}
	s.buf = make([]byte, 4096)
	caps := []string{"ports"}
	if s.cfg.ResolveOwners {
		// Probe: can we read another process's fd table (pid 1)?
		if _, err := os.ReadDir(s.procDir + "/1/fd"); err == nil {
			s.resolve = true
			caps = append(caps, "port_owners")
		} else {
			s.log.Info("port owners unavailable (grant CAP_SYS_PTRACE and CAP_DAC_READ_SEARCH to resolve them)", "err", err)
		}
	}
	return caps, nil
}

type listener struct {
	proto string
	addr  netip.Addr
	port  uint16
	inode uint64
}

// Scan reads the socket tables and returns the deduplicated listeners.
func (s *Source) Scan() ([]listener, error) {
	seen := map[string]bool{}
	var out []listener
	var firstErr error
	for _, t := range s.tables {
		f, err := os.Open(t.path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		err = procfs.ParseSockets(f, func(so procfs.Socket) {
			if t.udp {
				if so.State != procfs.UDPClose || so.RPort != 0 {
					return
				}
			} else if so.State != procfs.TCPListen {
				return
			}
			k := t.proto + "|" + so.Local.String() + "|" + strconv.Itoa(int(so.Port))
			if seen[k] {
				return
			}
			seen[k] = true
			out = append(out, listener{proto: t.proto, addr: so.Local, port: so.Port, inode: so.Inode})
		})
		f.Close()
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// Tick implements state.Source.
func (s *Source) Tick(now time.Time, sink state.Sink) error {
	ls, err := s.Scan()
	if err != nil {
		return err
	}
	if s.resolve {
		s.resolveOwners(now, ls)
	}
	sink.ReplaceListeners(s.bindings(ls))
	return nil
}

// resolveOwners walks /proc/*/fd once when some listening inode is unknown.
func (s *Source) resolveOwners(now time.Time, ls []listener) {
	live := make(map[uint64]bool, len(ls))
	need := false
	for _, l := range ls {
		live[l.inode] = true
		if _, ok := s.owners[l.inode]; ok || l.inode == 0 {
			continue
		}
		if t, ok := s.unresolved[l.inode]; ok && now.Sub(t) < retryUnresolved {
			continue
		}
		need = true
	}
	for ino := range s.owners {
		if !live[ino] {
			delete(s.owners, ino)
		}
	}
	for ino := range s.unresolved {
		if !live[ino] {
			delete(s.unresolved, ino)
		}
	}
	if !need {
		return
	}
	pids, err := procfs.ListPids(s.procDir)
	if err != nil {
		return
	}
	for _, pid := range pids {
		_ = procfs.SocketInodes(s.procDir, pid, func(ino uint64) {
			if !live[ino] {
				return
			}
			if _, ok := s.owners[ino]; ok {
				return
			}
			s.owners[ino] = s.describe(pid)
			delete(s.unresolved, ino)
		})
	}
	for _, l := range ls {
		if _, ok := s.owners[l.inode]; !ok {
			s.unresolved[l.inode] = now
		}
	}
}

func (s *Source) describe(pid int) owner {
	o := owner{pid: pid}
	if b, err := procfs.ReadFile(procfs.PidPath(s.procDir, pid, "stat"), s.buf); err == nil {
		if st, ok := procfs.ParsePidStat(b); ok {
			o.comm = string(st.Comm)
		}
	}
	if b, err := procfs.ReadFile(procfs.PidPath(s.procDir, pid, "cgroup"), s.buf); err == nil {
		if p, ok := procfs.CgroupV2Path(b); ok {
			o.cg = string(p)
		} else {
			// cgroup v1: any line carries the container id / unit.
			o.cg = strings.TrimSpace(string(b))
		}
	}
	return o
}

func (s *Source) bindings(ls []listener) []*v1.PortBinding {
	out := make([]*v1.PortBinding, 0, len(ls))
	var listening []int
	for _, l := range ls {
		ip := l.addr.String()
		key := ip
		if l.addr.Is6() {
			key = "[" + ip + "]"
		}
		pb := &v1.PortBinding{
			Id:            "port:" + l.proto + ":" + key + ":" + strconv.Itoa(int(l.port)),
			WorkloadId:    "host",
			HostIp:        ip,
			HostPort:      uint32(l.port),
			ContainerPort: uint32(l.port),
			Proto:         l.proto,
			Exposure:      dockercli.ClassifyExposure(ip),
		}
		if l.addr.IsUnspecified() {
			pb.Exposure = v1.PortExposure_PORT_EXPOSURE_PUBLIC
		}
		o, known := s.owners[l.inode]
		if known {
			pb.ProcessName = o.comm
			listening = append(listening, o.pid)
		}
		proxy := known && (o.comm == "docker-proxy" || o.comm == "rootlessport")
		if (!known || proxy) && s.reg != nil {
			// docker-proxy, or an owner we could not resolve: attribute a
			// published host port to its container.
			if c, cp, ok := s.reg.ContainerByHostPort(pb.HostPort); ok {
				pb.WorkloadId, pb.WorkloadName, pb.ContainerPort = c.EntityID, c.Name, cp
			}
		}
		if known && pb.WorkloadId == "host" {
			s.attribute(pb, o)
		}
		if pb.WorkloadName == "" && pb.WorkloadId == "host" {
			pb.WorkloadName = pb.ProcessName
		}
		out = append(out, pb)
	}
	if s.reg != nil {
		s.reg.SetListeningPids(listening)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostPort != out[j].HostPort {
			return out[i].HostPort < out[j].HostPort
		}
		return out[i].Id < out[j].Id
	})
	return out
}

// attribute sets the workload hint from the owner's cgroup and pid.
func (s *Source) attribute(pb *v1.PortBinding, o owner) {
	if id := procfs.ContainerIDFromCgroup(o.cg); id != "" && s.reg != nil {
		if c, ok := s.reg.ContainerByID(id); ok {
			pb.WorkloadId, pb.WorkloadName = c.EntityID, c.Name
			return
		}
	}
	if s.reg != nil {
		if w := s.reg.ProcOwner(o.pid); w != "" {
			pb.WorkloadId, pb.WorkloadName = w, strings.TrimPrefix(w, "proc:")
			return
		}
	}
	if u := procfs.UnitFromCgroup(o.cg); u != "" {
		pb.WorkloadId, pb.WorkloadName = "unit:"+u, u
	}
}
