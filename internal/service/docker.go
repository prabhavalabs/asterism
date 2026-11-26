package service

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// DockerSnapshot is a point-in-time, caller-owned copy of Docker inventory.
type DockerSnapshot = dockercli.Snapshot

// DockerProvider supplies Docker inventory. Implementations must return deep
// copies so callers may mutate the result.
type DockerProvider interface {
	DockerSnapshot() DockerSnapshot
}

// ContainerActuator performs mutating container actions on the Docker host.
// containerID is always a validated short (12 hex) container id.
type ContainerActuator interface {
	SupportedContainerActions() []asterismv1.ContainerAction
	ContainerAction(ctx context.Context, action asterismv1.ContainerAction, containerID string, timeout time.Duration) (*asterismv1.Container, error)
}

// AttributedActuator is optionally implemented by actuators that execute
// actions elsewhere (agent mode). It receives the caller's audit actor, and
// the returned attributes (for example executed_by and agent_audit_id) are
// added to the audit event. An error may implement ActionCode() string,
// returning a Connect code name ("permission_denied", "not_found", ...), and
// AuditAttrs() map[string]string.
type AttributedActuator interface {
	ContainerActionAttributed(ctx context.Context, action asterismv1.ContainerAction, containerID string, timeout time.Duration, actor string) (*asterismv1.Container, map[string]string, error)
}

// ActuatorStatus is optionally implemented by actuators whose availability
// changes at runtime (an agent that is offline or has actions disabled). A
// non-empty reason disables actions for every caller.
type ActuatorStatus interface {
	ActionsUnavailableReason() string
}

// ActionPolicy is the configured gate for mutating actions.
type ActionPolicy struct {
	Enabled        bool
	ProtectedNames []string
	AllowProtected bool
	DefaultTimeout time.Duration
}

// ConfigureDocker attaches the Docker inventory source and optional actuator.
func (o *Observatory) ConfigureDocker(p DockerProvider, a ContainerActuator, policy ActionPolicy) {
	o.docker = p
	o.actuator = a
	if policy.DefaultTimeout <= 0 {
		policy.DefaultTimeout = 10 * time.Second
	}
	o.policy = policy
}

func (o *Observatory) snapshot() DockerSnapshot {
	if o.docker == nil {
		return DockerSnapshot{Engine: &asterismv1.DockerEngine{
			Available:         false,
			UnavailableReason: fmt.Sprintf("Docker inventory is not collected in %q mode; use collect.mode = \"agent\" or \"ssh\" (or \"showcase\" for demo data)", o.mode),
		}}
	}
	snap := o.docker.DockerSnapshot()
	if snap.Engine == nil {
		snap.Engine = &asterismv1.DockerEngine{Available: len(snap.Containers) > 0}
	}
	for _, c := range snap.Containers {
		o.decorate(c)
	}
	return snap
}

// Capabilities reports which optional features the caller may use.
func (o *Observatory) Capabilities(p auth.Principal) *asterismv1.Capabilities {
	c := &asterismv1.Capabilities{
		ActionsEnabled:  o.policy.Enabled,
		Scopes:          p.ScopeNames(),
		DockerAvailable: o.docker != nil,
		SourceMode:      o.mode,
		CanEditProjects: canEditProjects(p) && o.icons != nil,
	}
	if o.actuator != nil {
		c.ContainerActions = slices.Clone(o.actuator.SupportedContainerActions())
	}
	switch {
	case !o.policy.Enabled:
		c.ActionsDisabledReason = actionsDisabledMsg
	case o.actuator == nil:
		c.ActionsDisabledReason = fmt.Sprintf("container actions are not available in %q collector mode", o.mode)
	case !p.Has(auth.ScopeActions):
		c.ActionsDisabledReason = "your credentials lack the \"actions\" scope"
	case o.actuatorUnavailable() != "":
		c.ActionsDisabledReason = o.actuatorUnavailable()
	default:
		c.CanPerformActions = true
	}
	return c
}

// actuatorUnavailable returns the actuator's runtime reason for refusing
// actions, or "".
func (o *Observatory) actuatorUnavailable() string {
	if st, ok := o.actuator.(ActuatorStatus); ok {
		return st.ActionsUnavailableReason()
	}
	return ""
}

func containerCPU(c *asterismv1.Container) float64 {
	if c.Stats == nil {
		return 0
	}
	return c.Stats.CpuUtil
}

func containerMem(c *asterismv1.Container) uint64 {
	if c.Stats == nil {
		return 0
	}
	return c.Stats.MemUsedBytes
}

var containerSorter = newSorter("container", "project, name",
	func(c *asterismv1.Container) string { return c.Id },
	byFold("name", func(c *asterismv1.Container) string { return c.Name }),
	by("project", func(c *asterismv1.Container) string { return c.ProjectId }),
	by("state", func(c *asterismv1.Container) asterismv1.ContainerState { return c.State }),
	byFold("image", func(c *asterismv1.Container) string { return c.Image }),
	by("cpu", containerCPU),
	by("memory", containerMem),
	by("uptime", func(c *asterismv1.Container) uint64 { return c.UptimeSeconds }),
	by("restarts", func(c *asterismv1.Container) uint32 { return c.RestartCount }),
	by("created", func(c *asterismv1.Container) int64 { return c.CreatedAtMs }),
)

func containerMatchesQuery(c *asterismv1.Container, q string) bool {
	if q == "" {
		return true
	}
	if port, ok := portQuery(q); ok {
		for _, p := range c.Ports {
			if p.HostPort == port || p.ContainerPort == port {
				return true
			}
		}
	}
	return strings.HasPrefix(c.ContainerId, q) || containsAny(q, c.Name, c.Image, c.ComposeService)
}

// ListContainers returns one page of containers (running and stopped)
// matching the request, plus facet counts over every page.
func (o *Observatory) ListContainers(req *asterismv1.ListContainersRequest) (*asterismv1.ListContainersResponse, error) {
	snap := o.snapshot()
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.Container]{
		{facet: "project", match: func(c *asterismv1.Container) bool {
			return req.GetProjectId() == "" || c.ProjectId == req.GetProjectId()
		}},
		{facet: "state", match: func(c *asterismv1.Container) bool {
			if len(req.GetStates()) > 0 && !slices.Contains(req.GetStates(), c.State) {
				return false
			}
			return !req.GetAttentionOnly() || dockercli.NeedsAttention(c)
		}},
		{match: func(c *asterismv1.Container) bool { return containerMatchesQuery(c, q) }},
	}
	matched := matching(snap.Containers, filters, "")
	var mem uint64
	for _, c := range matched {
		mem += containerMem(c)
	}
	items, info, err := listPage(matched, containerSorter, req.GetOrderBy(), req.GetPage())
	if err != nil {
		return nil, err
	}
	return &asterismv1.ListContainersResponse{
		Containers:    items,
		Counts:        dockercli.Counts(matching(snap.Containers, filters, "state")),
		Engine:        snap.Engine,
		CollectedAtMs: snap.CollectedAtMs,
		PageInfo:      info,
		ProjectCounts: countBy(matching(snap.Containers, filters, "project"), func(c *asterismv1.Container) string { return c.ProjectId }),
		MemUsedBytes:  mem,
	}, nil
}

var containerRefRe = regexp.MustCompile(`^(ctr:)?[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var hexPrefixRe = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// resolveContainer finds a container by entity id, name, or id prefix.
func resolveContainer(cs []*asterismv1.Container, ref string) (*asterismv1.Container, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, newErr(ErrInvalidArgument, "container id is required (use \"ctr:<name>\", the name, or a container id)")
	}
	if !containerRefRe.MatchString(ref) {
		return nil, newErr(ErrInvalidArgument, "invalid container reference %q: use \"ctr:<name>\", the name, or a hex container id", dockercli.Truncate(ref, 64))
	}
	name := strings.TrimPrefix(ref, "ctr:")
	for _, c := range cs {
		if c.Name == name || c.Id == ref {
			return c, nil
		}
	}
	if hexPrefixRe.MatchString(name) {
		var match *asterismv1.Container
		for _, c := range cs {
			if strings.HasPrefix(c.ContainerId, name) || (len(name) > 12 && strings.HasPrefix(name, c.ContainerId)) {
				if match != nil {
					return nil, newErr(ErrInvalidArgument, "container id prefix %q is ambiguous; use more characters or the name", name)
				}
				match = c
			}
		}
		if match != nil {
			return match, nil
		}
	}
	return nil, newErr(ErrNotFound, "unknown container %q%s", ref, suggest(cs, name))
}

func suggest(cs []*asterismv1.Container, name string) string {
	low := strings.ToLower(name)
	for _, c := range cs {
		if strings.Contains(strings.ToLower(c.Name), low) || strings.Contains(low, strings.ToLower(c.Name)) {
			return fmt.Sprintf("; did you mean %q?", c.Id)
		}
	}
	return ""
}

// GetContainer returns one container plus its recent events.
func (o *Observatory) GetContainer(ref string) (*asterismv1.GetContainerResponse, error) {
	snap := o.snapshot()
	c, err := resolveContainer(snap.Containers, ref)
	if err != nil {
		return nil, err
	}
	return &asterismv1.GetContainerResponse{
		Container:    c,
		RecentEvents: o.ListEvents(c.Id, 20),
	}, nil
}

func imageName(img *asterismv1.DockerImage) string {
	return strings.ToLower(img.Repository + ":" + img.Tag)
}

var imageSorter = newSorter("image", "size desc",
	func(img *asterismv1.DockerImage) string { return img.Id },
	by("size", func(img *asterismv1.DockerImage) uint64 { return img.SizeBytes }),
	by("created", func(img *asterismv1.DockerImage) int64 { return img.CreatedAtMs }),
	by("repository", imageName),
	by("containers", func(img *asterismv1.DockerImage) uint32 { return img.Containers }),
)

// ListImages returns one page of Docker images with in-use counts.
func (o *Observatory) ListImages(req *asterismv1.ListImagesRequest) (*asterismv1.ListImagesResponse, error) {
	snap := o.snapshot()
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.DockerImage]{
		{facet: "usage", match: func(img *asterismv1.DockerImage) bool {
			return (!req.GetDanglingOnly() || img.Dangling) && (!req.GetInUseOnly() || img.Containers > 0) &&
				(!req.GetUnusedOnly() || img.Containers == 0)
		}},
		{match: func(img *asterismv1.DockerImage) bool {
			fields := append([]string{img.Id, img.Repository, img.Tag}, img.RepoTags...)
			return containsAny(q, append(fields, img.UsedBy...)...)
		}},
	}
	matched := matching(snap.Images, filters, "")
	resp := &asterismv1.ListImagesResponse{CollectedAtMs: snap.CollectedAtMs, Counts: &asterismv1.ImageCounts{}}
	for _, img := range matched {
		resp.TotalSizeBytes += img.SizeBytes
	}
	for _, img := range matching(snap.Images, filters, "usage") {
		resp.Counts.Total++
		if img.Dangling {
			resp.Counts.Dangling++
		}
		if img.Containers > 0 {
			resp.Counts.InUse++
		} else {
			resp.Counts.Unused++
		}
	}
	var err error
	if resp.Images, resp.PageInfo, err = listPage(matched, imageSorter, req.GetOrderBy(), req.GetPage()); err != nil {
		return nil, err
	}
	return resp, nil
}

var volumeSorter = newSorter("volume", "name",
	func(v *asterismv1.DockerVolume) string { return v.Name },
	by("name", func(v *asterismv1.DockerVolume) string { return v.Name }),
	by("size", func(v *asterismv1.DockerVolume) int64 { return v.SizeBytes }),
	by("created", func(v *asterismv1.DockerVolume) int64 { return v.CreatedAtMs }),
	by("containers", func(v *asterismv1.DockerVolume) uint32 { return v.Containers }),
	by("project", func(v *asterismv1.DockerVolume) string { return v.ComposeProject }),
)

// ListVolumes returns one page of Docker volumes with in-use counts.
func (o *Observatory) ListVolumes(req *asterismv1.ListVolumesRequest) (*asterismv1.ListVolumesResponse, error) {
	snap := o.snapshot()
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.DockerVolume]{
		{facet: "usage", match: func(v *asterismv1.DockerVolume) bool {
			return (!req.GetUnusedOnly() || v.Containers == 0) && (!req.GetInUseOnly() || v.Containers > 0)
		}},
		{match: func(v *asterismv1.DockerVolume) bool {
			return containsAny(q, append([]string{v.Name, v.Driver, v.ComposeProject}, v.UsedBy...)...)
		}},
	}
	matched := matching(snap.Volumes, filters, "")
	resp := &asterismv1.ListVolumesResponse{CollectedAtMs: snap.CollectedAtMs, Counts: &asterismv1.VolumeCounts{}}
	for _, v := range matched {
		if v.SizeBytes > 0 {
			resp.TotalSizeBytes += uint64(v.SizeBytes)
		}
	}
	for _, v := range matching(snap.Volumes, filters, "usage") {
		resp.Counts.Total++
		if v.Containers > 0 {
			resp.Counts.InUse++
		} else {
			resp.Counts.Unused++
		}
		if v.Anonymous {
			resp.Counts.Anonymous++
		}
	}
	var err error
	if resp.Volumes, resp.PageInfo, err = listPage(matched, volumeSorter, req.GetOrderBy(), req.GetPage()); err != nil {
		return nil, err
	}
	return resp, nil
}

var networkSorter = newSorter("network", "name",
	func(n *asterismv1.DockerNetwork) string { return n.Id },
	by("name", func(n *asterismv1.DockerNetwork) string { return n.Name }),
	by("driver", func(n *asterismv1.DockerNetwork) string { return n.Driver }),
	by("containers", func(n *asterismv1.DockerNetwork) int { return len(n.Containers) }),
	by("created", func(n *asterismv1.DockerNetwork) int64 { return n.CreatedAtMs }),
)

// ListNetworks returns one page of Docker networks with attached containers.
func (o *Observatory) ListNetworks(req *asterismv1.ListNetworksRequest) (*asterismv1.ListNetworksResponse, error) {
	snap := o.snapshot()
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.DockerNetwork]{
		{facet: "builtin", match: func(n *asterismv1.DockerNetwork) bool {
			return !req.GetUserDefinedOnly() || !n.Builtin
		}},
		{match: func(n *asterismv1.DockerNetwork) bool {
			if q == "" {
				return true
			}
			fields := append([]string{n.Name, n.Id, n.Driver, n.Scope, n.ComposeProject}, n.Subnets...)
			for _, a := range n.Containers {
				fields = append(fields, a.Name)
			}
			return containsAny(q, fields...)
		}},
	}
	items, info, err := listPage(matching(snap.Networks, filters, ""), networkSorter, req.GetOrderBy(), req.GetPage())
	if err != nil {
		return nil, err
	}
	counts := &asterismv1.NetworkCounts{}
	for _, n := range matching(snap.Networks, filters, "builtin") {
		counts.Total++
		if n.Builtin {
			counts.Builtin++
		} else {
			counts.UserDefined++
		}
	}
	return &asterismv1.ListNetworksResponse{Networks: items, PageInfo: info, CollectedAtMs: snap.CollectedAtMs, Counts: counts}, nil
}

// DiskUsage returns the `docker system df` summary and engine info.
func (o *Observatory) DiskUsage() *asterismv1.GetDiskUsageResponse {
	snap := o.snapshot()
	du := snap.DiskUsage
	if du == nil {
		du = &asterismv1.DockerDiskUsage{}
	}
	return &asterismv1.GetDiskUsageResponse{Usage: du, Engine: snap.Engine}
}

// protectedReason returns why actions are refused for c, or "" if they are allowed.
func (o *Observatory) protectedReason(c *asterismv1.Container) string {
	switch {
	case slices.Contains(o.policy.ProtectedNames, c.Name):
		return "listed in [actions] protected_containers"
	case strings.EqualFold(c.Labels["asterism.self"], "true"):
		return "this is the Asterism container itself (label asterism.self=true)"
	case strings.EqualFold(c.Labels["asterism.protected"], "true"):
		return "labelled asterism.protected=true"
	}
	repo := c.ImageRepo
	if i := strings.LastIndexByte(repo, '/'); i >= 0 {
		repo = repo[i+1:]
	}
	if repo == "asterism" {
		return "runs the asterism image (this is likely Asterism itself)"
	}
	return ""
}

// decorate adds service-derived fields: protection and insights.
func (o *Observatory) decorate(c *asterismv1.Container) {
	if reason := o.protectedReason(c); reason != "" && !o.policy.AllowProtected {
		c.Protected = true
		c.ProtectedReason = reason
	}
	c.Insights = containerInsights(c)
	c.Tech = DetectTech(c.Image)
}

func containerInsights(c *asterismv1.Container) []*asterismv1.ContainerInsight {
	var out []*asterismv1.ContainerInsight
	add := func(sev, code, msg string) {
		out = append(out, &asterismv1.ContainerInsight{Severity: sev, Code: code, Message: msg})
	}
	if c.OomKilled {
		add("critical", "oom_killed", "Last exit was an out-of-memory kill; raise the memory limit or fix the leak")
	}
	if c.State == asterismv1.ContainerState_CONTAINER_STATE_RESTARTING || c.RestartCount >= 5 {
		add("warning", "restart_loop", fmt.Sprintf("Container has restarted %d times", c.RestartCount))
	}
	if c.Health == asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY {
		streak := uint32(0)
		if c.Healthcheck != nil {
			streak = c.Healthcheck.FailingStreak
		}
		add("warning", "unhealthy", fmt.Sprintf("Healthcheck failing (%d consecutive failures)", streak))
	}
	if c.State == asterismv1.ContainerState_CONTAINER_STATE_EXITED && c.ExitCode != 0 && !c.OomKilled {
		add("warning", "nonzero_exit", fmt.Sprintf("Exited with code %d", c.ExitCode))
	}
	if c.Privileged {
		add("warning", "privileged", "Runs in privileged mode (full host device access)")
	}
	for _, m := range c.Mounts {
		if m.DockerSocket {
			add("warning", "docker_socket", "Mounts the Docker socket (root-equivalent access to the host)")
			break
		}
	}
	if c.NetworkMode == "host" {
		add("info", "host_network", "Uses the host network namespace (ports are not isolated)")
	}
	if c.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE || c.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE {
		for _, p := range c.Ports {
			if p.Published && p.Exposure == asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC {
				add("critical", "public_datastore", fmt.Sprintf("Datastore port %d is published on %s", p.HostPort, p.HostIp))
				break
			}
		}
	}
	if c.Resources != nil && c.Resources.MemLimitBytes == 0 && c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
		add("info", "no_memory_limit", "No memory limit set")
	}
	if c.RestartPolicy == "no" && c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING && c.ComposeProject != "" {
		add("info", "no_restart_policy", "No restart policy; the container will stay down after a crash or reboot")
	}
	sort.SliceStable(out, func(i, j int) bool { return sevRank(out[i].Severity) > sevRank(out[j].Severity) })
	return out
}

func sevRank(s string) int {
	switch s {
	case "critical":
		return 2
	case "warning":
		return 1
	default:
		return 0
	}
}
