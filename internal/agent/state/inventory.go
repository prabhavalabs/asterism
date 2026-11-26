package state

import (
	"path"
	"slices"
	"sort"
	"strings"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"google.golang.org/protobuf/proto"
)

// inventory is guarded by Store.mu.
type inventory struct {
	ver     uint64 // current version
	baseVer uint64 // version of the last snapshot or delta handed out

	host        *v1.HostInfo
	engine      *v1.DockerEngine
	containers  map[string]*v1.Container
	workloads   map[string]*v1.Workload
	images      []*v1.DockerImage
	volumes     []*v1.DockerVolume
	networks    []*v1.DockerNetwork
	diskUsage   *v1.DockerDiskUsage
	listeners   []*v1.PortBinding
	routes      []*v1.IngressRoute
	rules       []*v1.ProjectRule
	filesystems []*v1.FilesystemUsage
	icons       []*v1.ProjectIconRef

	pending pendingDelta
	changed chan struct{}
}

type pendingDelta struct {
	upsertC, removedC                                                           map[string]struct{}
	upsertW, removedW                                                           map[string]struct{}
	host, engine, images, volumes, networks, disk, listeners, routes, rules, fs bool
	icons                                                                       bool
}

func (p *pendingDelta) reset() {
	*p = pendingDelta{
		upsertC: map[string]struct{}{}, removedC: map[string]struct{}{},
		upsertW: map[string]struct{}{}, removedW: map[string]struct{}{},
	}
}

func (p *pendingDelta) empty() bool {
	return len(p.upsertC)+len(p.removedC)+len(p.upsertW)+len(p.removedW) == 0 &&
		!p.host && !p.engine && !p.images && !p.volumes && !p.networks && !p.disk &&
		!p.listeners && !p.routes && !p.rules && !p.fs && !p.icons
}

func (inv *inventory) init() {
	inv.containers = map[string]*v1.Container{}
	inv.workloads = map[string]*v1.Workload{}
	inv.pending.reset()
	inv.changed = make(chan struct{}, 1)
}

func (inv *inventory) bump() {
	inv.ver++
	select {
	case inv.changed <- struct{}{}:
	default:
	}
}

// Changed is signalled (coalesced) whenever the inventory version moves.
func (s *Store) Changed() <-chan struct{} { return s.inv.changed }

// InventoryVersion returns the current inventory version.
func (s *Store) InventoryVersion() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inv.ver
}

// UpsertContainer stores a copy of c keyed by c.Id ("ctr:<name>"; derived
// from the name when empty). An identical upsert is a no-op.
func (s *Store) UpsertContainer(c *v1.Container) {
	if c == nil {
		return
	}
	cp := proto.Clone(c).(*v1.Container)
	if cp.Id == "" {
		if cp.Name == "" {
			return
		}
		cp.Id = "ctr:" + cp.Name
	}
	if s.opts.AnnotateContainer != nil {
		s.opts.AnnotateContainer(cp)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inv := &s.inv
	if old, ok := inv.containers[cp.Id]; ok && proto.Equal(old, cp) {
		return
	}
	inv.containers[cp.Id] = cp
	inv.pending.upsertC[cp.Id] = struct{}{}
	delete(inv.pending.removedC, cp.Id)
	inv.bump()
}

// RemoveContainer removes a container by entity id.
func (s *Store) RemoveContainer(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv := &s.inv
	if _, ok := inv.containers[id]; !ok {
		return
	}
	delete(inv.containers, id)
	delete(inv.pending.upsertC, id)
	inv.pending.removedC[id] = struct{}{}
	inv.bump()
}

// UpsertWorkload stores a copy of w keyed by w.Id. Containers belong in
// UpsertContainer, not here.
func (s *Store) UpsertWorkload(w *v1.Workload) {
	if w == nil || w.Id == "" {
		return
	}
	cp := proto.Clone(w).(*v1.Workload)
	s.mu.Lock()
	defer s.mu.Unlock()
	inv := &s.inv
	if old, ok := inv.workloads[cp.Id]; ok && proto.Equal(old, cp) {
		return
	}
	inv.workloads[cp.Id] = cp
	inv.pending.upsertW[cp.Id] = struct{}{}
	delete(inv.pending.removedW, cp.Id)
	inv.bump()
}

// RemoveWorkload removes a workload by entity id.
func (s *Store) RemoveWorkload(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv := &s.inv
	if _, ok := inv.workloads[id]; !ok {
		return
	}
	delete(inv.workloads, id)
	delete(inv.pending.upsertW, id)
	inv.pending.removedW[id] = struct{}{}
	inv.bump()
}

// replaceList swaps *dst for a copy of src when they differ.
func replaceList[T proto.Message](dst *[]T, src []T) bool {
	if len(*dst) == len(src) {
		same := true
		for i := range src {
			if !proto.Equal((*dst)[i], src[i]) {
				same = false
				break
			}
		}
		if same {
			return false
		}
	}
	cp := make([]T, len(src))
	for i, m := range src {
		cp[i] = proto.Clone(m).(T)
	}
	*dst = cp
	return true
}

func replaceOne[T proto.Message](dst *T, src T, isNil func(T) bool) bool {
	if !isNil(*dst) && proto.Equal(*dst, src) {
		return false
	}
	*dst = proto.Clone(src).(T)
	return true
}

// ReplaceListeners replaces the listening ports.
func (s *Store) ReplaceListeners(l []*v1.PortBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replaceList(&s.inv.listeners, l) {
		s.inv.pending.listeners = true
		s.inv.bump()
	}
}

// ReplaceRoutes replaces the ingress routes.
func (s *Store) ReplaceRoutes(r []*v1.IngressRoute) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replaceList(&s.inv.routes, r) {
		s.inv.pending.routes = true
		s.inv.bump()
	}
}

// ReplaceFilesystems replaces the filesystem usage list.
func (s *Store) ReplaceFilesystems(f []*v1.FilesystemUsage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replaceList(&s.inv.filesystems, f) {
		s.inv.pending.fs = true
		s.inv.bump()
	}
}

// ReplaceProjectIcons replaces the favicon files found in project
// directories (sorted by working directory).
func (s *Store) ReplaceProjectIcons(icons []*v1.ProjectIconRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replaceList(&s.inv.icons, icons) {
		s.inv.pending.icons = true
		s.inv.bump()
	}
}

// ProjectIcon returns the reported icon file with the given digest.
func (s *Store) ProjectIcon(sha256 string) (*v1.ProjectIconRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ic := range s.inv.icons {
		if ic.GetSha256() == sha256 {
			return proto.Clone(ic).(*v1.ProjectIconRef), true
		}
	}
	return nil, false
}

// SetHostInfo replaces the static host facts.
func (s *Store) SetHostInfo(h *v1.HostInfo) {
	if h == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if replaceOne(&s.inv.host, h, func(x *v1.HostInfo) bool { return x == nil }) {
		s.inv.pending.host = true
		s.inv.bump()
	}
}

// ReplaceDockerResources replaces the parts of r that are set.
func (s *Store) ReplaceDockerResources(r DockerResources) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv := &s.inv
	changed := false
	if r.Engine != nil && replaceOne(&inv.engine, r.Engine, func(x *v1.DockerEngine) bool { return x == nil }) {
		inv.pending.engine, changed = true, true
	}
	if r.ImagesSet && replaceList(&inv.images, r.Images) {
		inv.pending.images, changed = true, true
	}
	if r.VolumesSet && replaceList(&inv.volumes, r.Volumes) {
		inv.pending.volumes, changed = true, true
	}
	if r.NetworksSet && replaceList(&inv.networks, r.Networks) {
		inv.pending.networks, changed = true, true
	}
	if r.DiskUsage != nil && replaceOne(&inv.diskUsage, r.DiskUsage, func(x *v1.DockerDiskUsage) bool { return x == nil }) {
		inv.pending.disk, changed = true, true
	}
	if changed {
		inv.bump()
	}
}

// FindContainer resolves an entity id ("ctr:<name>"), a container name, or a
// Docker id (short or full, prefix match) to a copy of the container.
func (s *Store) FindContainer(ref string) *v1.Container {
	if ref == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.inv.containers[ref]; ok {
		return proto.Clone(c).(*v1.Container)
	}
	if c, ok := s.inv.containers["ctr:"+ref]; ok {
		return proto.Clone(c).(*v1.Container)
	}
	for _, c := range s.inv.containers {
		cid := c.GetContainerId()
		if len(ref) >= 12 && cid != "" && (strings.HasPrefix(ref, cid) || strings.HasPrefix(cid, ref)) {
			return proto.Clone(c).(*v1.Container)
		}
	}
	return nil
}

// Containers returns copies of every container, sorted by id.
func (s *Store) Containers() []*v1.Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSorted(s.inv.containers)
}

// TakeInventory returns what the hub needs to reach the current version.
//
// known is the version the hub holds (HelloAck.known_inventory_version, or
// the agent's own last handed-out version during a connection). When force
// is set, or known differs from the last version handed out, it returns a
// full snapshot; otherwise a delta of the changes since then, or (nil, nil)
// when nothing changed.
func (s *Store) TakeInventory(known uint64, force bool) (*v1.InventorySnapshot, *v1.InventoryDelta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshRulesLocked()
	inv := &s.inv
	if force || known != inv.baseVer {
		snap := s.snapshotLocked()
		inv.baseVer = inv.ver
		inv.pending.reset()
		return snap, nil
	}
	if inv.pending.empty() {
		return nil, nil
	}
	d := &v1.InventoryDelta{BaseVersion: inv.baseVer, Version: inv.ver}
	p := &inv.pending
	for _, id := range sortedKeys(p.upsertC) {
		d.UpsertContainers = append(d.UpsertContainers, proto.Clone(inv.containers[id]).(*v1.Container))
	}
	d.RemovedContainerIds = sortedKeys(p.removedC)
	for _, id := range sortedKeys(p.upsertW) {
		d.UpsertWorkloads = append(d.UpsertWorkloads, proto.Clone(inv.workloads[id]).(*v1.Workload))
	}
	d.RemovedWorkloadIds = sortedKeys(p.removedW)
	r := &v1.ReplaceSet{}
	has := false
	if p.host && inv.host != nil {
		r.Host, has = proto.Clone(inv.host).(*v1.HostInfo), true
	}
	if p.engine && inv.engine != nil {
		r.Engine, has = proto.Clone(inv.engine).(*v1.DockerEngine), true
	}
	if p.disk && inv.diskUsage != nil {
		r.DiskUsage, has = proto.Clone(inv.diskUsage).(*v1.DockerDiskUsage), true
	}
	if p.images {
		r.Images, r.ImagesSet, has = cloneList(inv.images), true, true
	}
	if p.volumes {
		r.Volumes, r.VolumesSet, has = cloneList(inv.volumes), true, true
	}
	if p.networks {
		r.Networks, r.NetworksSet, has = cloneList(inv.networks), true, true
	}
	if p.listeners {
		r.Listeners, r.ListenersSet, has = cloneList(inv.listeners), true, true
	}
	if p.routes {
		r.Routes, r.RoutesSet, has = cloneList(inv.routes), true, true
	}
	if p.rules {
		r.ProjectRules, r.ProjectRulesSet, has = cloneList(inv.rules), true, true
	}
	if p.fs {
		r.Filesystems, r.FilesystemsSet, has = cloneList(inv.filesystems), true, true
	}
	if p.icons {
		r.ProjectIcons, r.ProjectIconsSet, has = cloneList(inv.icons), true, true
	}
	if has {
		d.Replace = r
	}
	inv.baseVer = inv.ver
	p.reset()
	return nil, d
}

// Snapshot returns a full snapshot without changing delta tracking.
func (s *Store) Snapshot() *v1.InventorySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshRulesLocked()
	return s.snapshotLocked()
}

func (s *Store) snapshotLocked() *v1.InventorySnapshot {
	inv := &s.inv
	snap := &v1.InventorySnapshot{
		Version:      inv.ver,
		Containers:   cloneSorted(inv.containers),
		Workloads:    cloneSorted(inv.workloads),
		Images:       cloneList(inv.images),
		Volumes:      cloneList(inv.volumes),
		Networks:     cloneList(inv.networks),
		Listeners:    cloneList(inv.listeners),
		Routes:       cloneList(inv.routes),
		ProjectRules: cloneList(inv.rules),
		Filesystems:  cloneList(inv.filesystems),
		ProjectIcons: cloneList(inv.icons),
	}
	if inv.host != nil {
		snap.Host = proto.Clone(inv.host).(*v1.HostInfo)
	}
	if inv.engine != nil {
		snap.Engine = proto.Clone(inv.engine).(*v1.DockerEngine)
	}
	if inv.diskUsage != nil {
		snap.DiskUsage = proto.Clone(inv.diskUsage).(*v1.DockerDiskUsage)
	}
	return snap
}

// refreshRulesLocked resolves the configured projects against the current
// containers and workloads.
func (s *Store) refreshRulesLocked() {
	if len(s.opts.Projects) == 0 {
		return
	}
	rules := make([]*v1.ProjectRule, 0, len(s.opts.Projects))
	for _, p := range s.opts.Projects {
		r := &v1.ProjectRule{Name: p.Name, Category: p.Category, Icon: p.Icon, Color: p.Color}
		for id, c := range s.inv.containers {
			if globAny(p.Members.Compose, c.GetComposeProject()) || globAny(p.Members.Containers, c.GetName()) {
				r.MemberIds = append(r.MemberIds, id)
			}
		}
		for id, w := range s.inv.workloads {
			switch w.GetKind() {
			case v1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT:
				if globAny(p.Members.Units, w.GetName()) {
					r.MemberIds = append(r.MemberIds, id)
				}
			case v1.WorkloadKind_WORKLOAD_KIND_PROCESS:
				if globAny(p.Members.Processes, w.GetName()) || globAny(p.Members.PM2, w.GetName()) {
					r.MemberIds = append(r.MemberIds, id)
				}
			}
		}
		sort.Strings(r.MemberIds)
		rules = append(rules, r)
	}
	if replaceList(&s.inv.rules, rules) {
		s.inv.pending.rules = true
		s.inv.bump()
	}
}

func globAny(globs []string, name string) bool {
	if name == "" {
		return false
	}
	return slices.ContainsFunc(globs, func(g string) bool {
		ok, _ := path.Match(g, name)
		return ok
	})
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cloneSorted[T proto.Message](m map[string]T) []T {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		out = append(out, proto.Clone(m[k]).(T))
	}
	return out
}

func cloneList[T proto.Message](in []T) []T {
	if len(in) == 0 {
		return nil
	}
	out := make([]T, len(in))
	for i, m := range in {
		out[i] = proto.Clone(m).(T)
	}
	return out
}

func cloneEvent(e *v1.Event) *v1.Event { return proto.Clone(e).(*v1.Event) }

// HostInfo returns a copy of the static host facts, or nil before the host
// collector has run.
func (s *Store) HostInfo() *v1.HostInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inv.host == nil {
		return nil
	}
	return proto.Clone(s.inv.host).(*v1.HostInfo)
}
