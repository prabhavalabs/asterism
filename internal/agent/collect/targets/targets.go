// Package targets is the in-process registry that agent collectors share:
// the Docker source publishes running containers, the systemd source units,
// the process and pm2 sources matched pids, and the listener source the
// pids that own listening sockets. Readers poll Gen() and copy only when it
// changed, so the hot cgroup tick never allocates.
package targets

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Container is a container the cgroup and listener collectors care about.
type Container struct {
	EntityID string // "ctr:<name>"
	Name     string
	FullID   string // 64 hex
	Pid      int32
	OwnNetns bool // false for network_mode host / container:<x>
	Running  bool
	// HostPorts maps published host port -> container port (docker-proxy
	// listeners are attributed to the container).
	HostPorts map[uint32]uint32
}

// Unit is a systemd unit with a cgroup.
type Unit struct {
	Name string // "nginx.service"
	Dir  string // cgroup directory on the agent's filesystem
}

// Registry is safe for concurrent use.
type Registry struct {
	gen atomic.Uint64

	mu         sync.RWMutex
	containers map[string]Container // by FullID
	units      map[string]Unit
	procs      map[string]map[int]struct{} // "proc:<name>" -> pids (per source key)
	procOwner  map[int]string
	listenPids map[int]struct{}
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		containers: map[string]Container{},
		units:      map[string]Unit{},
		procs:      map[string]map[int]struct{}{},
		procOwner:  map[int]string{},
		listenPids: map[int]struct{}{},
	}
}

// Gen changes whenever containers or units change.
func (r *Registry) Gen() uint64 { return r.gen.Load() }

// SetContainer adds or replaces a container.
func (r *Registry) SetContainer(c Container) {
	r.mu.Lock()
	old, ok := r.containers[c.FullID]
	r.containers[c.FullID] = c
	r.mu.Unlock()
	if !ok || !sameContainer(old, c) {
		r.gen.Add(1)
	}
}

func sameContainer(a, b Container) bool {
	if a.EntityID != b.EntityID || a.Pid != b.Pid || a.OwnNetns != b.OwnNetns || a.Running != b.Running || len(a.HostPorts) != len(b.HostPorts) {
		return false
	}
	for k, v := range a.HostPorts {
		if b.HostPorts[k] != v {
			return false
		}
	}
	return true
}

// RemoveContainer removes a container by full id.
func (r *Registry) RemoveContainer(fullID string) {
	r.mu.Lock()
	_, ok := r.containers[fullID]
	delete(r.containers, fullID)
	r.mu.Unlock()
	if ok {
		r.gen.Add(1)
	}
}

// Containers appends all containers (sorted by entity id) to dst[:0].
func (r *Registry) Containers(dst []Container) []Container {
	r.mu.RLock()
	dst = dst[:0]
	for _, c := range r.containers {
		dst = append(dst, c)
	}
	r.mu.RUnlock()
	sort.Slice(dst, func(i, j int) bool { return dst[i].EntityID < dst[j].EntityID })
	return dst
}

// ContainerByID finds a container by full id or id prefix (≥ 12 chars).
func (r *Registry) ContainerByID(id string) (Container, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.containers[id]; ok {
		return c, true
	}
	if len(id) < 12 {
		return Container{}, false
	}
	for full, c := range r.containers {
		if strings.HasPrefix(full, id) {
			return c, true
		}
	}
	return Container{}, false
}

// ContainerByHostPort returns the running container publishing host port p.
func (r *Registry) ContainerByHostPort(p uint32) (Container, uint32, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.containers {
		if cp, ok := c.HostPorts[p]; ok && c.Running {
			return c, cp, true
		}
	}
	return Container{}, 0, false
}

// SetUnits replaces the unit set.
func (r *Registry) SetUnits(units []Unit) {
	m := make(map[string]Unit, len(units))
	for _, u := range units {
		m[u.Name] = u
	}
	r.mu.Lock()
	changed := len(m) != len(r.units)
	if !changed {
		for k, v := range m {
			if r.units[k] != v {
				changed = true
				break
			}
		}
	}
	r.units = m
	r.mu.Unlock()
	if changed {
		r.gen.Add(1)
	}
}

// Units appends all units (sorted by name) to dst[:0].
func (r *Registry) Units(dst []Unit) []Unit {
	r.mu.RLock()
	dst = dst[:0]
	for _, u := range r.units {
		dst = append(dst, u)
	}
	r.mu.RUnlock()
	sort.Slice(dst, func(i, j int) bool { return dst[i].Name < dst[j].Name })
	return dst
}

// SetProcs records the pids of workload id (process and pm2 workloads),
// replacing that workload's previous set.
func (r *Registry) SetProcs(workloadID string, pids []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for pid := range r.procs[workloadID] {
		if r.procOwner[pid] == workloadID {
			delete(r.procOwner, pid)
		}
	}
	if len(pids) == 0 {
		delete(r.procs, workloadID)
		return
	}
	set := make(map[int]struct{}, len(pids))
	for _, p := range pids {
		set[p] = struct{}{}
		r.procOwner[p] = workloadID
	}
	r.procs[workloadID] = set
}

// ProcOwner returns the process workload id owning pid, or "".
func (r *Registry) ProcOwner(pid int) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.procOwner[pid]
}

// SetListeningPids records which pids own listening sockets.
func (r *Registry) SetListeningPids(pids []int) {
	m := make(map[int]struct{}, len(pids))
	for _, p := range pids {
		m[p] = struct{}{}
	}
	r.mu.Lock()
	r.listenPids = m
	r.mu.Unlock()
}

// IsListening reports whether pid owns a listening socket.
func (r *Registry) IsListening(pid int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.listenPids[pid]
	return ok
}
