package mcp

import (
	"slices"
	"strconv"
	"strings"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Compact, token-efficient views of the protobuf messages. Field names carry
// units (cpu_pct, mem_bytes), timestamps are RFC 3339 strings, and enums are
// short lowercase names. Secret-bearing data never appears: workloads and
// containers expose environment variable names only, never values.

// Page describes one page of a list and whether more items were left out.
type Page struct {
	Page       int32  `json:"page"`
	PageSize   int32  `json:"page_size"`
	TotalSize  int32  `json:"total_size"`
	TotalPages int32  `json:"total_pages"`
	Truncated  bool   `json:"truncated"`
	Hint       string `json:"hint,omitempty"`
}

func pageOf(p *asterismv1.PageInfo, narrow string) Page {
	out := Page{
		Page:       p.GetPage(),
		PageSize:   p.GetPageSize(),
		TotalSize:  p.GetTotalSize(),
		TotalPages: p.GetTotalPages(),
	}
	if out.Page < out.TotalPages {
		out.Truncated = true
		out.Hint = "more results: pass page=" + itoa(out.Page+1) + " or narrow with " + narrow
	}
	return out
}

type HostView struct {
	Hostname       string  `json:"hostname"`
	OS             string  `json:"os,omitempty"`
	Kernel         string  `json:"kernel,omitempty"`
	Arch           string  `json:"arch,omitempty"`
	CPUModel       string  `json:"cpu_model,omitempty"`
	Cores          uint32  `json:"cores"`
	UptimeSeconds  uint64  `json:"uptime_seconds"`
	BootTime       string  `json:"boot_time,omitempty"`
	CPUPct         float64 `json:"cpu_pct"`
	CPUStealPct    float64 `json:"cpu_steal_pct,omitempty"`
	Load1          float64 `json:"load_1"`
	Load5          float64 `json:"load_5"`
	Load15         float64 `json:"load_15"`
	MemUsedBytes   uint64  `json:"mem_used_bytes"`
	MemTotalBytes  uint64  `json:"mem_total_bytes"`
	MemPct         float64 `json:"mem_pct"`
	SwapUsedBytes  uint64  `json:"swap_used_bytes,omitempty"`
	SwapTotalBytes uint64  `json:"swap_total_bytes,omitempty"`
	DiskUsedBytes  uint64  `json:"disk_used_bytes"`
	DiskTotalBytes uint64  `json:"disk_total_bytes"`
	DiskPct        float64 `json:"disk_pct"`
	NetRxBps       float64 `json:"net_rx_bps"`
	NetTxBps       float64 `json:"net_tx_bps"`
	SourceMode     string  `json:"source_mode,omitempty"`
}

func hostView(h *asterismv1.HostInfo) HostView {
	return HostView{
		Hostname:       h.GetHostname(),
		OS:             h.GetOs(),
		Kernel:         h.GetKernel(),
		Arch:           h.GetArch(),
		CPUModel:       h.GetCpuModel(),
		Cores:          h.GetCores(),
		UptimeSeconds:  h.GetUptimeSeconds(),
		BootTime:       iso(h.GetBootTimeMs()),
		CPUPct:         pct(h.GetCpuUtil()),
		CPUStealPct:    pct(h.GetCpuStealUtil()),
		Load1:          round2(h.GetLoad_1()),
		Load5:          round2(h.GetLoad_5()),
		Load15:         round2(h.GetLoad_15()),
		MemUsedBytes:   h.GetMemUsedBytes(),
		MemTotalBytes:  h.GetMemTotalBytes(),
		MemPct:         share(h.GetMemUsedBytes(), h.GetMemTotalBytes()),
		SwapUsedBytes:  h.GetSwapUsedBytes(),
		SwapTotalBytes: h.GetSwapTotalBytes(),
		DiskUsedBytes:  h.GetDiskUsedBytes(),
		DiskTotalBytes: h.GetDiskTotalBytes(),
		DiskPct:        share(h.GetDiskUsedBytes(), h.GetDiskTotalBytes()),
		NetRxBps:       round1(h.GetNetRxBps()),
		NetTxBps:       round1(h.GetNetTxBps()),
		SourceMode:     h.GetSourceMode(),
	}
}

type ProjectView struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Category      string  `json:"category,omitempty"`
	Kind          string  `json:"kind,omitempty"`
	Status        string  `json:"status"`
	HealthScore   uint32  `json:"health_score"`
	CPUPct        float64 `json:"cpu_pct"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	MemLimitBytes uint64  `json:"mem_limit_bytes,omitempty"`
	MemSharePct   float64 `json:"mem_share_pct"`
	Workloads     uint32  `json:"workloads"`
	WorkingDir    string  `json:"working_dir,omitempty"`
	Icon          string  `json:"icon,omitempty"`
	// IconKind is how the icon was chosen: uploaded, configured, label,
	// favicon, detected or monogram. Image bytes are never returned.
	IconKind string `json:"icon_kind,omitempty"`
	// IconSource says where an uploaded or favicon image came from.
	IconSource string `json:"icon_source,omitempty"`
}

// iconSource describes the origin of an image icon ("" for glyphs).
func iconSource(ic *asterismv1.ProjectIcon) string {
	switch ic.GetKind() {
	case asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UPLOADED:
		return "uploaded by an admin"
	case asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON:
		switch ic.GetFaviconSource() {
		case asterismv1.FaviconSource_FAVICON_SOURCE_DIRECTORY:
			return "project directory: " + ic.GetFaviconOrigin()
		case asterismv1.FaviconSource_FAVICON_SOURCE_DOMAIN:
			return "public domain: " + ic.GetFaviconOrigin()
		case asterismv1.FaviconSource_FAVICON_SOURCE_GENERATED:
			return "generated (showcase)"
		}
	}
	return ""
}

func projectView(p *asterismv1.Project) ProjectView {
	return ProjectView{
		ID:            p.GetId(),
		Name:          p.GetName(),
		Category:      p.GetCategory(),
		Kind:          p.GetKind(),
		Status:        healthName(p.GetStatus()),
		HealthScore:   p.GetHealthScore(),
		CPUPct:        pct(p.GetCpuUtil()),
		MemUsedBytes:  p.GetMemUsedBytes(),
		MemLimitBytes: p.GetMemLimitBytes(),
		MemSharePct:   round1(p.GetMemSharePct()),
		Workloads:     p.GetWorkloadCount(),
		WorkingDir:    p.GetWorkingDir(),
		Icon:          p.GetIcon().GetSlug(),
		IconKind:      iconKind(p.GetIcon().GetKind()),
		IconSource:    iconSource(p.GetIcon()),
	}
}

type PortView struct {
	ID            string `json:"id,omitempty"`
	Workload      string `json:"workload,omitempty"`
	WorkloadID    string `json:"workload_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	HostIP        string `json:"host_ip,omitempty"`
	HostPort      uint32 `json:"host_port,omitempty"`
	ContainerPort uint32 `json:"container_port,omitempty"`
	Proto         string `json:"proto,omitempty"`
	Exposure      string `json:"exposure"`
	Process       string `json:"process,omitempty"`
}

func portView(p *asterismv1.PortBinding) PortView {
	return PortView{
		ID:            p.GetId(),
		Workload:      p.GetWorkloadName(),
		WorkloadID:    p.GetWorkloadId(),
		ProjectID:     p.GetProjectId(),
		HostIP:        p.GetHostIp(),
		HostPort:      p.GetHostPort(),
		ContainerPort: p.GetContainerPort(),
		Proto:         p.GetProto(),
		Exposure:      exposureName(p.GetExposure()),
		Process:       p.GetProcessName(),
	}
}

type WorkloadView struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Role          string     `json:"role,omitempty"`
	ProjectID     string     `json:"project_id,omitempty"`
	Service       string     `json:"service,omitempty"`
	Image         string     `json:"image,omitempty"`
	Tech          string     `json:"tech,omitempty"`
	State         string     `json:"state,omitempty"`
	Health        string     `json:"health,omitempty"`
	Restarts      uint32     `json:"restarts"`
	StartedAt     string     `json:"started_at,omitempty"`
	CPUPct        float64    `json:"cpu_pct"`
	MemUsedBytes  uint64     `json:"mem_used_bytes"`
	MemLimitBytes uint64     `json:"mem_limit_bytes,omitempty"`
	NetRxBps      float64    `json:"net_rx_bps,omitempty"`
	NetTxBps      float64    `json:"net_tx_bps,omitempty"`
	Replicas      uint32     `json:"replicas,omitempty"`
	Ports         []PortView `json:"ports,omitempty"`
	Networks      []string   `json:"networks,omitempty"`
	// EnvKeys lists environment variable names only; values are never exposed.
	EnvKeys []string `json:"env_keys,omitempty"`
}

func workloadView(w *asterismv1.Workload, detail bool) WorkloadView {
	v := WorkloadView{
		ID:            w.GetId(),
		Name:          w.GetName(),
		Kind:          enumName(w.GetKind().String(), "WORKLOAD_KIND_"),
		Role:          roleName(w.GetRole()),
		ProjectID:     w.GetProjectId(),
		Service:       w.GetServiceName(),
		Image:         w.GetImage(),
		Tech:          w.GetTech(),
		State:         w.GetState(),
		Health:        healthName(w.GetHealth()),
		Restarts:      w.GetRestartCount(),
		StartedAt:     iso(w.GetStartedAtMs()),
		CPUPct:        pct(w.GetCpuUtil()),
		MemUsedBytes:  w.GetMemUsedBytes(),
		MemLimitBytes: w.GetMemLimitBytes(),
		Replicas:      w.GetReplicas(),
	}
	if detail {
		v.NetRxBps = round1(w.GetNetRxBps())
		v.NetTxBps = round1(w.GetNetTxBps())
		for _, p := range w.GetPorts() {
			v.Ports = append(v.Ports, portView(p))
		}
		v.Networks = w.GetNetworks()
		v.EnvKeys = w.GetEnvKeys()
	}
	return v
}

type UpstreamView struct {
	Address    string `json:"address"`
	WorkloadID string `json:"workload_id,omitempty"`
	Weight     uint32 `json:"weight,omitempty"`
	Backup     bool   `json:"backup,omitempty"`
	Healthy    bool   `json:"healthy"`
}

type IngressView struct {
	ID              string         `json:"id"`
	Proxy           string         `json:"proxy"`
	ProxyWorkloadID string         `json:"proxy_workload_id,omitempty"`
	Hostnames       []string       `json:"hostnames,omitempty"`
	ListenPort      uint32         `json:"listen_port,omitempty"`
	TLS             bool           `json:"tls"`
	PathPrefix      string         `json:"path_prefix,omitempty"`
	Strategy        string         `json:"strategy,omitempty"`
	UpstreamsUp     int            `json:"upstreams_up"`
	Upstreams       []UpstreamView `json:"upstreams"`
}

func ingressView(r *asterismv1.IngressRoute) IngressView {
	v := IngressView{
		ID:              r.GetId(),
		Proxy:           enumName(r.GetProxyKind().String(), "PROXY_KIND_"),
		ProxyWorkloadID: r.GetProxyWorkloadId(),
		Hostnames:       r.GetHostnames(),
		ListenPort:      r.GetListenPort(),
		TLS:             r.GetTls(),
		PathPrefix:      r.GetPathPrefix(),
		Strategy:        r.GetStrategy(),
		Upstreams:       []UpstreamView{},
	}
	for _, u := range r.GetUpstreams() {
		if u.GetHealthy() {
			v.UpstreamsUp++
		}
		v.Upstreams = append(v.Upstreams, UpstreamView{
			Address:    u.GetAddress(),
			WorkloadID: u.GetWorkloadId(),
			Weight:     u.GetWeight(),
			Backup:     u.GetBackup(),
			Healthy:    u.GetHealthy(),
		})
	}
	return v
}

type EventView struct {
	Time     string            `json:"time"`
	Severity string            `json:"severity"`
	Kind     string            `json:"kind"`
	EntityID string            `json:"entity_id,omitempty"`
	Entity   string            `json:"entity,omitempty"`
	Message  string            `json:"message"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Audit    *AuditView        `json:"audit,omitempty"`
}

type AuditView struct {
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

func eventView(e *asterismv1.Event) EventView {
	v := EventView{
		Time:     iso(e.GetTsMs()),
		Severity: e.GetSeverity(),
		Kind:     e.GetKind(),
		EntityID: e.GetEntityId(),
		Entity:   e.GetEntityName(),
		Message:  e.GetMessage(),
		Attrs:    e.GetAttrs(),
	}
	if a := e.GetAudit(); a != nil {
		v.Audit = &AuditView{
			Actor:  a.GetActor(),
			Action: a.GetAction(),
			Target: firstNonEmpty(a.GetTargetName(), a.GetTargetId()),
			Result: a.GetResult(),
			Error:  a.GetError(),
		}
	}
	return v
}

type ContainerView struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ProjectID     string   `json:"project_id,omitempty"`
	Service       string   `json:"service,omitempty"`
	Image         string   `json:"image"`
	Role          string   `json:"role,omitempty"`
	State         string   `json:"state"`
	Status        string   `json:"status,omitempty"`
	Health        string   `json:"health,omitempty"`
	Restarts      uint32   `json:"restarts"`
	OOMKilled     bool     `json:"oom_killed,omitempty"`
	ExitCode      int32    `json:"exit_code,omitempty"`
	UptimeSeconds uint64   `json:"uptime_seconds,omitempty"`
	CPUPct        float64  `json:"cpu_pct"`
	MemUsedBytes  uint64   `json:"mem_used_bytes"`
	MemLimitBytes uint64   `json:"mem_limit_bytes,omitempty"`
	Ports         []string `json:"ports,omitempty"`
	Insights      []string `json:"insights,omitempty"`
}

type ContainerDetail struct {
	ContainerView
	ContainerID    string            `json:"container_id"`
	CreatedAt      string            `json:"created_at,omitempty"`
	StartedAt      string            `json:"started_at,omitempty"`
	FinishedAt     string            `json:"finished_at,omitempty"`
	Error          string            `json:"error,omitempty"`
	RestartPolicy  string            `json:"restart_policy,omitempty"`
	Command        []string          `json:"command,omitempty"`
	User           string            `json:"user,omitempty"`
	Privileged     bool              `json:"privileged,omitempty"`
	ReadOnlyRoot   bool              `json:"read_only_rootfs,omitempty"`
	CPULimitCores  float64           `json:"cpu_limit_cores,omitempty"`
	PidsLimit      int64             `json:"pids_limit,omitempty"`
	NetRxBps       float64           `json:"net_rx_bps,omitempty"`
	NetTxBps       float64           `json:"net_tx_bps,omitempty"`
	BlkReadBps     float64           `json:"blk_read_bps,omitempty"`
	BlkWriteBps    float64           `json:"blk_write_bps,omitempty"`
	Pids           uint32            `json:"pids,omitempty"`
	Healthcheck    *HealthcheckView  `json:"healthcheck,omitempty"`
	Networks       []string          `json:"networks,omitempty"`
	Mounts         []string          `json:"mounts,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	RedactedLabels uint32            `json:"redacted_labels,omitempty"`
	// EnvKeys lists environment variable names only; values are never exposed.
	EnvKeys      []string    `json:"env_keys,omitempty"`
	Protected    bool        `json:"protected,omitempty"`
	RecentEvents []EventView `json:"recent_events,omitempty"`
}

type HealthcheckView struct {
	Status         string `json:"status"`
	FailingStreak  uint32 `json:"failing_streak,omitempty"`
	LastExitCode   int32  `json:"last_exit_code,omitempty"`
	LastOutput     string `json:"last_output,omitempty"`
	LastCheckAt    string `json:"last_check_at,omitempty"`
	IntervalSecond uint32 `json:"interval_seconds,omitempty"`
}

func containerView(c *asterismv1.Container) ContainerView {
	v := ContainerView{
		ID:            c.GetId(),
		Name:          c.GetName(),
		ProjectID:     c.GetProjectId(),
		Service:       c.GetComposeService(),
		Image:         c.GetImage(),
		Role:          roleName(c.GetRole()),
		State:         enumName(c.GetState().String(), "CONTAINER_STATE_"),
		Status:        c.GetStatus(),
		Health:        enumName(c.GetHealth().String(), "CONTAINER_HEALTH_"),
		Restarts:      c.GetRestartCount(),
		OOMKilled:     c.GetOomKilled(),
		ExitCode:      c.GetExitCode(),
		UptimeSeconds: c.GetUptimeSeconds(),
		CPUPct:        pct(c.GetStats().GetCpuUtil()),
		MemUsedBytes:  c.GetStats().GetMemUsedBytes(),
		MemLimitBytes: c.GetStats().GetMemLimitBytes(),
	}
	if v.Health == "none" {
		v.Health = ""
	}
	for _, p := range c.GetPorts() {
		v.Ports = append(v.Ports, containerPortString(p))
	}
	for _, in := range c.GetInsights() {
		v.Insights = append(v.Insights, in.GetSeverity()+": "+in.GetMessage())
	}
	return v
}

func containerDetail(c *asterismv1.Container, events []*asterismv1.Event) ContainerDetail {
	d := ContainerDetail{
		ContainerView:  containerView(c),
		ContainerID:    c.GetContainerId(),
		CreatedAt:      iso(c.GetCreatedAtMs()),
		StartedAt:      iso(c.GetStartedAtMs()),
		FinishedAt:     iso(c.GetFinishedAtMs()),
		Error:          c.GetError(),
		RestartPolicy:  c.GetRestartPolicy(),
		Command:        slices.Concat(c.GetEntrypoint(), c.GetCommand()),
		User:           c.GetUser(),
		Privileged:     c.GetPrivileged(),
		ReadOnlyRoot:   c.GetReadOnlyRootfs(),
		CPULimitCores:  round2(c.GetResources().GetCpuLimitCores()),
		PidsLimit:      c.GetResources().GetPidsLimit(),
		NetRxBps:       round1(c.GetStats().GetNetRxBps()),
		NetTxBps:       round1(c.GetStats().GetNetTxBps()),
		BlkReadBps:     round1(c.GetStats().GetBlkReadBps()),
		BlkWriteBps:    round1(c.GetStats().GetBlkWriteBps()),
		Pids:           c.GetStats().GetPids(),
		Labels:         c.GetLabels(),
		RedactedLabels: c.GetRedactedLabelCount(),
		EnvKeys:        c.GetEnvKeys(),
		Protected:      c.GetProtected(),
	}
	if hc := c.GetHealthcheck(); hc != nil && hc.GetStatus() != asterismv1.ContainerHealth_CONTAINER_HEALTH_UNSPECIFIED {
		d.Healthcheck = &HealthcheckView{
			Status:         enumName(hc.GetStatus().String(), "CONTAINER_HEALTH_"),
			FailingStreak:  hc.GetFailingStreak(),
			LastExitCode:   hc.GetLastExitCode(),
			LastOutput:     truncate(hc.GetLastOutput(), 300),
			LastCheckAt:    iso(hc.GetLastCheckAtMs()),
			IntervalSecond: hc.GetIntervalSeconds(),
		}
	}
	for _, n := range c.GetNetworks() {
		d.Networks = append(d.Networks, n.GetName())
	}
	for _, m := range c.GetMounts() {
		src := firstNonEmpty(m.GetName(), m.GetSource())
		mode := "ro"
		if m.GetReadWrite() {
			mode = "rw"
		}
		d.Mounts = append(d.Mounts, m.GetType()+":"+src+" -> "+m.GetDestination()+" ("+mode+")")
	}
	for _, e := range events {
		d.RecentEvents = append(d.RecentEvents, eventView(e))
	}
	return d
}

func containerPortString(p *asterismv1.ContainerPort) string {
	proto := p.GetProto()
	if proto == "" {
		proto = "tcp"
	}
	if !p.GetPublished() || p.GetHostPort() == 0 {
		return itoa(int32(p.GetContainerPort())) + "/" + proto + " (internal)"
	}
	host := p.GetHostIp()
	if host == "" {
		host = "0.0.0.0"
	}
	return host + ":" + itoa(int32(p.GetHostPort())) + "->" + itoa(int32(p.GetContainerPort())) + "/" + proto + " (" + exposureName(p.GetExposure()) + ")"
}

func healthName(h asterismv1.HealthStatus) string {
	return enumName(h.String(), "HEALTH_STATUS_")
}

func roleName(r asterismv1.WorkloadRole) string {
	return enumName(r.String(), "WORKLOAD_ROLE_")
}

func exposureName(e asterismv1.PortExposure) string {
	return enumName(e.String(), "PORT_EXPOSURE_")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func itoa(n int32) string { return strconv.Itoa(int(n)) }

func iconKind(k asterismv1.ProjectIconKind) string {
	if k == asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UNSPECIFIED {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(k.String(), "PROJECT_ICON_KIND_"))
}
