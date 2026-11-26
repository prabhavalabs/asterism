package dockercli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// inspectTemplate selects only the container fields Asterism needs. Environment
// variables are reduced to their names on the Docker host so values never cross
// the wire, and HostConfig is narrowed so logging-driver options stay behind too.
const inspectTemplate = `{"Id":{{json .Id}},"Name":{{json .Name}},"Created":{{json .Created}},"Image":{{json .Image}},"RestartCount":{{json .RestartCount}},"State":{{json .State}},` +
	`"HostConfig":{"NetworkMode":{{json .HostConfig.NetworkMode}},"Privileged":{{json .HostConfig.Privileged}},"ReadonlyRootfs":{{json .HostConfig.ReadonlyRootfs}},"RestartPolicy":{{json .HostConfig.RestartPolicy}},` +
	`"Memory":{{json .HostConfig.Memory}},"MemoryReservation":{{json .HostConfig.MemoryReservation}},"NanoCpus":{{json .HostConfig.NanoCpus}},"CpuQuota":{{json .HostConfig.CpuQuota}},"CpuPeriod":{{json .HostConfig.CpuPeriod}},` +
	`"CpuShares":{{json .HostConfig.CpuShares}},"CpusetCpus":{{json .HostConfig.CpusetCpus}},"PidsLimit":{{json .HostConfig.PidsLimit}},"PortBindings":{{json .HostConfig.PortBindings}}},` +
	`"Mounts":{{json .Mounts}},"NetworkSettings":{"Networks":{{json .NetworkSettings.Networks}},"Ports":{{json .NetworkSettings.Ports}}},` +
	`"Config":{"Image":{{json .Config.Image}},"Labels":{{json .Config.Labels}},"Cmd":{{json .Config.Cmd}},"Entrypoint":{{json .Config.Entrypoint}},"ExposedPorts":{{json .Config.ExposedPorts}},` +
	`"User":{{json .Config.User}},"Healthcheck":{{json .Config.Healthcheck}},"EnvKeys":[{{range $i, $e := .Config.Env}}{{if $i}},{{end}}{{json (index (split $e "=") 0)}}{{end}}]}}`

// InspectAllScript inspects every container (running and stopped) in one docker call.
const InspectAllScript = `ids=$(docker ps -aq --no-trunc 2>/dev/null); if [ -n "$ids" ]; then docker inspect --format '` + inspectTemplate + `' $ids 2>/dev/null; fi`

// InspectCommand returns a docker inspect command for the given validated container ids.
func InspectCommand(ids ...string) (string, error) {
	if len(ids) == 0 {
		return "", fmt.Errorf("no container ids")
	}
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		if !ValidContainerID(id) {
			return "", fmt.Errorf("invalid container id %q", Truncate(id, 80))
		}
		quoted = append(quoted, ShellQuote(id))
	}
	return "docker inspect --format '" + inspectTemplate + "' " + strings.Join(quoted, " "), nil
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type inspectDoc struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Created      string `json:"Created"`
	Image        string `json:"Image"`
	RestartCount int    `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		OOMKilled  bool   `json:"OOMKilled"`
		Dead       bool   `json:"Dead"`
		Pid        int    `json:"Pid"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
			Log           []struct {
				Start    string `json:"Start"`
				End      string `json:"End"`
				ExitCode int    `json:"ExitCode"`
				Output   string `json:"Output"`
			} `json:"Log"`
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		NetworkMode    string `json:"NetworkMode"`
		Privileged     bool   `json:"Privileged"`
		ReadonlyRootfs bool   `json:"ReadonlyRootfs"`
		RestartPolicy  struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Memory            int64                    `json:"Memory"`
		MemoryReservation int64                    `json:"MemoryReservation"`
		NanoCpus          int64                    `json:"NanoCpus"`
		CPUQuota          int64                    `json:"CpuQuota"`
		CPUPeriod         int64                    `json:"CpuPeriod"`
		CPUShares         int64                    `json:"CpuShares"`
		CpusetCpus        string                   `json:"CpusetCpus"`
		PidsLimit         *int64                   `json:"PidsLimit"`
		PortBindings      map[string][]portBinding `json:"PortBindings"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Driver      string `json:"Driver"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string   `json:"IPAddress"`
			Gateway   string   `json:"Gateway"`
			Aliases   []string `json:"Aliases"`
		} `json:"Networks"`
		Ports map[string][]portBinding `json:"Ports"`
	} `json:"NetworkSettings"`
	Config struct {
		Image        string              `json:"Image"`
		Labels       map[string]string   `json:"Labels"`
		Cmd          []string            `json:"Cmd"`
		Entrypoint   []string            `json:"Entrypoint"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		User         string              `json:"User"`
		Healthcheck  *struct {
			Test     []string `json:"Test"`
			Interval int64    `json:"Interval"`
			Timeout  int64    `json:"Timeout"`
			Retries  int      `json:"Retries"`
		} `json:"Healthcheck"`
		EnvKeys []string `json:"EnvKeys"`
	} `json:"Config"`
}

// Compose label keys.
const (
	LabelComposeProject    = "com.docker.compose.project"
	LabelComposeService    = "com.docker.compose.service"
	LabelComposeWorkingDir = "com.docker.compose.project.working_dir"
)

// ParseInspect parses one JSON document per line (InspectAllScript output) or a
// JSON array (plain `docker inspect`). Unparseable lines are skipped.
func ParseInspect(out string, now time.Time) []*asterismv1.Container {
	var docs []inspectDoc
	trim := strings.TrimSpace(out)
	if strings.HasPrefix(trim, "[") {
		if err := json.Unmarshal([]byte(trim), &docs); err != nil {
			docs = nil
		}
	} else {
		sc := bufio.NewScanner(strings.NewReader(out))
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] != '{' {
				continue
			}
			var d inspectDoc
			if err := json.Unmarshal([]byte(line), &d); err != nil {
				continue
			}
			docs = append(docs, d)
		}
	}
	res := make([]*asterismv1.Container, 0, len(docs))
	for i := range docs {
		if c := buildContainer(&docs[i], now); c != nil {
			res = append(res, c)
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res
}

func buildContainer(d *inspectDoc, now time.Time) *asterismv1.Container {
	if !ValidContainerID(d.ID) {
		return nil
	}
	name := strings.TrimPrefix(d.Name, "/")
	labels := d.Config.Labels
	project := labels[LabelComposeProject]

	c := &asterismv1.Container{
		Id:                "ctr:" + name,
		ContainerId:       ShortID(d.ID),
		Name:              name,
		Image:             d.Config.Image,
		ImageId:           ShortImageID(d.Image),
		State:             ParseState(d.State.Status),
		CreatedAtMs:       parseDockerTime(d.Created),
		StartedAtMs:       parseDockerTime(d.State.StartedAt),
		FinishedAtMs:      parseDockerTime(d.State.FinishedAt),
		ExitCode:          int32(d.State.ExitCode),
		Error:             RedactText(d.State.Error),
		RestartCount:      uint32(max(d.RestartCount, 0)),
		RestartPolicy:     normalizeRestartPolicy(d.HostConfig.RestartPolicy.Name),
		RestartMaxRetries: uint32(max(d.HostConfig.RestartPolicy.MaximumRetryCount, 0)),
		OomKilled:         d.State.OOMKilled,
		Pid:               int32(d.State.Pid),
		ComposeProject:    project,
		ComposeService:    labels[LabelComposeService],
		ComposeWorkingDir: labels[LabelComposeWorkingDir],
		NetworkMode:       d.HostConfig.NetworkMode,
		Command:           RedactShellArgs(d.Config.Cmd),
		Entrypoint:        RedactShellArgs(d.Config.Entrypoint),
		EnvKeys:           cleanEnvKeys(d.Config.EnvKeys),
		User:              d.Config.User,
		Privileged:        d.HostConfig.Privileged,
		ReadOnlyRootfs:    d.HostConfig.ReadonlyRootfs,
	}
	if project == "" {
		c.ProjectId = "proj:standalone"
	} else {
		c.ProjectId = "proj:" + project
	}
	c.ImageRepo, c.ImageTag = SplitImageRef(c.Image)
	c.Labels, c.RedactedLabelCount = FilterLabels(labels)

	// Health
	c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_NONE
	if hc := d.Config.Healthcheck; hc != nil && len(hc.Test) > 0 && !(len(hc.Test) == 1 && strings.EqualFold(hc.Test[0], "NONE")) {
		c.Healthcheck = &asterismv1.ContainerHealthcheck{
			Test:            RedactShellArgs(hc.Test),
			IntervalSeconds: uint32(time.Duration(hc.Interval).Seconds()),
			TimeoutSeconds:  uint32(time.Duration(hc.Timeout).Seconds()),
			Retries:         uint32(max(hc.Retries, 0)),
		}
	}
	if h := d.State.Health; h != nil && h.Status != "" {
		c.Health = ParseHealth(h.Status)
		if c.Healthcheck == nil {
			c.Healthcheck = &asterismv1.ContainerHealthcheck{}
		}
		c.Healthcheck.Status = c.Health
		c.Healthcheck.FailingStreak = uint32(max(h.FailingStreak, 0))
		if n := len(h.Log); n > 0 {
			last := h.Log[n-1]
			c.Healthcheck.LastExitCode = int32(last.ExitCode)
			c.Healthcheck.LastCheckAtMs = parseDockerTime(last.End)
			c.Healthcheck.LastOutput = Truncate(RedactText(strings.TrimSpace(last.Output)), 240)
		}
	} else if c.Healthcheck != nil {
		c.Healthcheck.Status = c.Health
	}

	if c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING || c.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
		if c.StartedAtMs > 0 && now.UnixMilli() > c.StartedAtMs {
			c.UptimeSeconds = uint64((now.UnixMilli() - c.StartedAtMs) / 1000)
		}
	}
	c.Status = HumanStatus(c, now)
	c.StatusLevel = StatusLevel(c.State, c.Health)

	// Networks
	for _, netName := range SortedKeys(d.NetworkSettings.Networks) {
		n := d.NetworkSettings.Networks[netName]
		aliases := make([]string, 0, len(n.Aliases))
		for _, a := range n.Aliases {
			if a != "" && !strings.HasPrefix(d.ID, a) {
				aliases = append(aliases, a)
			}
		}
		c.Networks = append(c.Networks, &asterismv1.ContainerNetwork{
			Name:      netName,
			IpAddress: n.IPAddress,
			Gateway:   n.Gateway,
			Aliases:   aliases,
		})
	}

	c.Ports = buildPorts(d)

	for _, m := range d.Mounts {
		c.Mounts = append(c.Mounts, &asterismv1.ContainerMount{
			Type:         m.Type,
			Name:         m.Name,
			Source:       m.Source,
			Destination:  m.Destination,
			ReadWrite:    m.RW,
			Mode:         m.Mode,
			Driver:       m.Driver,
			DockerSocket: isDockerSocket(m.Source) || isDockerSocket(m.Destination),
		})
	}
	sort.Slice(c.Mounts, func(i, j int) bool { return c.Mounts[i].Destination < c.Mounts[j].Destination })

	res := &asterismv1.ContainerResources{
		MemReservationBytes: uint64(max(d.HostConfig.MemoryReservation, 0)),
		CpuShares:           uint64(max(d.HostConfig.CPUShares, 0)),
		CpusetCpus:          d.HostConfig.CpusetCpus,
	}
	if d.HostConfig.Memory > 0 {
		res.MemLimitBytes = uint64(d.HostConfig.Memory)
	}
	switch {
	case d.HostConfig.NanoCpus > 0:
		res.CpuLimitCores = float64(d.HostConfig.NanoCpus) / 1e9
	case d.HostConfig.CPUQuota > 0:
		period := d.HostConfig.CPUPeriod
		if period <= 0 {
			period = 100_000
		}
		res.CpuLimitCores = float64(d.HostConfig.CPUQuota) / float64(period)
	}
	if d.HostConfig.PidsLimit != nil && *d.HostConfig.PidsLimit > 0 {
		res.PidsLimit = *d.HostConfig.PidsLimit
	}
	c.Resources = res
	return c
}

func cleanEnvKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		// Defensive: never keep anything after '=' even if the template changed.
		if eq := strings.IndexByte(k, '='); eq >= 0 {
			k = k[:eq]
		}
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isDockerSocket(p string) bool {
	return strings.HasSuffix(p, "/docker.sock") || strings.HasSuffix(p, "/podman.sock")
}

func buildPorts(d *inspectDoc) []*asterismv1.ContainerPort {
	type key struct {
		ip    string
		hp    uint32
		cp    uint32
		proto string
	}
	seen := make(map[key]bool)
	published := make(map[string]bool)
	var out []*asterismv1.ContainerPort
	add := func(spec string, bindings []portBinding) {
		cp, proto := parsePortSpec(spec)
		if cp == 0 {
			return
		}
		if len(bindings) == 0 {
			return
		}
		published[spec] = true
		hasV4 := make(map[string]bool)
		for _, b := range bindings {
			if b.HostIP == "" || b.HostIP == "0.0.0.0" {
				hasV4[b.HostPort] = true
			}
		}
		for _, b := range bindings {
			ip := b.HostIP
			if ip == "" {
				ip = "0.0.0.0"
			}
			// Collapse the IPv6 twin docker publishes next to 0.0.0.0.
			if ip == "::" && hasV4[b.HostPort] {
				continue
			}
			hp64, _ := strconv.ParseUint(b.HostPort, 10, 32)
			k := key{ip, uint32(hp64), cp, proto}
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, &asterismv1.ContainerPort{
				HostIp:        ip,
				HostPort:      uint32(hp64),
				ContainerPort: cp,
				Proto:         proto,
				Published:     true,
				Exposure:      ClassifyExposure(ip),
			})
		}
	}

	live := d.State.Running || d.State.Paused || d.State.Restarting
	if live && len(d.NetworkSettings.Ports) > 0 {
		for _, spec := range SortedKeys(d.NetworkSettings.Ports) {
			add(spec, d.NetworkSettings.Ports[spec])
		}
	} else {
		for _, spec := range SortedKeys(d.HostConfig.PortBindings) {
			add(spec, d.HostConfig.PortBindings[spec])
		}
	}
	// Exposed but unpublished ports.
	exposed := make(map[string]bool)
	for spec := range d.Config.ExposedPorts {
		exposed[spec] = true
	}
	if live {
		for spec, b := range d.NetworkSettings.Ports {
			if len(b) == 0 {
				exposed[spec] = true
			}
		}
	}
	for _, spec := range SortedKeys(exposed) {
		if published[spec] {
			continue
		}
		cp, proto := parsePortSpec(spec)
		if cp == 0 {
			continue
		}
		k := key{"", 0, cp, proto}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, &asterismv1.ContainerPort{
			ContainerPort: cp,
			Proto:         proto,
			Exposure:      asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ContainerPort != out[j].ContainerPort {
			return out[i].ContainerPort < out[j].ContainerPort
		}
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		return out[i].HostIp < out[j].HostIp
	})
	return out
}

// parsePortSpec parses "8080/tcp" (or "8080") into port and protocol.
func parsePortSpec(spec string) (uint32, string) {
	portStr, proto, ok := strings.Cut(spec, "/")
	if !ok || proto == "" {
		proto = "tcp"
	}
	if dash := strings.IndexByte(portStr, '-'); dash > 0 {
		portStr = portStr[:dash]
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return 0, ""
	}
	return uint32(p), proto
}

// ClassifyExposure maps a bind address to public, loopback (incl. Tailscale), or internal.
func ClassifyExposure(ip string) asterismv1.PortExposure {
	norm := strings.Trim(ip, "[]")
	switch {
	case norm == "":
		return asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL
	case strings.HasPrefix(norm, "127.") || norm == "::1" || norm == "localhost":
		return asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK
	case strings.HasPrefix(norm, "100.") || strings.HasPrefix(norm, "fd7a:"):
		// Tailscale private overlay
		return asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK
	default:
		return asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC
	}
}

// ShortID returns the 12-character form of a container id.
func ShortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// ShortImageID returns "sha256:<12 hex>".
func ShortImageID(id string) string {
	if id == "" {
		return ""
	}
	return "sha256:" + ShortID(id)
}

// SplitImageRef splits "registry:5000/org/app:1.2@sha256:..." into repository and tag.
func SplitImageRef(ref string) (repo, tag string) {
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		ref = ref[:at]
	}
	slash := strings.LastIndexByte(ref, '/')
	colon := strings.LastIndexByte(ref, ':')
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	if ref == "" {
		return "", ""
	}
	return ref, "latest"
}

// ParseState maps Docker's State.Status string.
func ParseState(s string) asterismv1.ContainerState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "created":
		return asterismv1.ContainerState_CONTAINER_STATE_CREATED
	case "running":
		return asterismv1.ContainerState_CONTAINER_STATE_RUNNING
	case "paused":
		return asterismv1.ContainerState_CONTAINER_STATE_PAUSED
	case "restarting":
		return asterismv1.ContainerState_CONTAINER_STATE_RESTARTING
	case "removing":
		return asterismv1.ContainerState_CONTAINER_STATE_REMOVING
	case "exited":
		return asterismv1.ContainerState_CONTAINER_STATE_EXITED
	case "dead":
		return asterismv1.ContainerState_CONTAINER_STATE_DEAD
	default:
		return asterismv1.ContainerState_CONTAINER_STATE_UNSPECIFIED
	}
}

// StateName returns the Docker name for a ContainerState ("running", "exited", ...).
func StateName(s asterismv1.ContainerState) string {
	n := strings.TrimPrefix(s.String(), "CONTAINER_STATE_")
	return strings.ToLower(n)
}

// ParseHealth maps Docker's State.Health.Status string.
func ParseHealth(s string) asterismv1.ContainerHealth {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "starting":
		return asterismv1.ContainerHealth_CONTAINER_HEALTH_STARTING
	case "healthy":
		return asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY
	case "unhealthy":
		return asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY
	case "none", "":
		return asterismv1.ContainerHealth_CONTAINER_HEALTH_NONE
	default:
		return asterismv1.ContainerHealth_CONTAINER_HEALTH_UNSPECIFIED
	}
}

// StatusLevel folds Docker state and health into the portal-wide health status.
func StatusLevel(state asterismv1.ContainerState, health asterismv1.ContainerHealth) asterismv1.HealthStatus {
	switch state {
	case asterismv1.ContainerState_CONTAINER_STATE_RUNNING:
		if health == asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY {
			return asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED
		}
		return asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY
	case asterismv1.ContainerState_CONTAINER_STATE_PAUSED, asterismv1.ContainerState_CONTAINER_STATE_RESTARTING, asterismv1.ContainerState_CONTAINER_STATE_REMOVING:
		return asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED
	default:
		return asterismv1.HealthStatus_HEALTH_STATUS_DOWN
	}
}

func normalizeRestartPolicy(s string) string {
	if s == "" {
		return "no"
	}
	return s
}

func parseDockerTime(s string) int64 {
	if s == "" || strings.HasPrefix(s, "0001-01-01") {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// HumanStatus renders the `docker ps` style status string.
func HumanStatus(c *asterismv1.Container, now time.Time) string {
	since := func(ms int64) string {
		if ms <= 0 {
			return ""
		}
		return HumanDuration(now.Sub(time.UnixMilli(ms)))
	}
	switch c.State {
	case asterismv1.ContainerState_CONTAINER_STATE_RUNNING, asterismv1.ContainerState_CONTAINER_STATE_PAUSED:
		s := "Up"
		if d := since(c.StartedAtMs); d != "" {
			s += " " + d
		}
		if c.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
			return s + " (Paused)"
		}
		switch c.Health {
		case asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY:
			s += " (healthy)"
		case asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY:
			s += " (unhealthy)"
		case asterismv1.ContainerHealth_CONTAINER_HEALTH_STARTING:
			s += " (health: starting)"
		}
		return s
	case asterismv1.ContainerState_CONTAINER_STATE_RESTARTING:
		s := fmt.Sprintf("Restarting (%d)", c.ExitCode)
		if d := since(c.FinishedAtMs); d != "" {
			s += " " + d + " ago"
		}
		return s
	case asterismv1.ContainerState_CONTAINER_STATE_EXITED:
		s := fmt.Sprintf("Exited (%d)", c.ExitCode)
		if d := since(c.FinishedAtMs); d != "" {
			s += " " + d + " ago"
		}
		return s
	case asterismv1.ContainerState_CONTAINER_STATE_CREATED:
		return "Created"
	case asterismv1.ContainerState_CONTAINER_STATE_DEAD:
		return "Dead"
	case asterismv1.ContainerState_CONTAINER_STATE_REMOVING:
		return "Removal In Progress"
	default:
		return "Unknown"
	}
}

// HumanDuration mirrors Docker's go-units HumanDuration.
func HumanDuration(d time.Duration) string {
	seconds := int(d.Seconds())
	switch {
	case seconds < 1:
		return "Less than a second"
	case seconds == 1:
		return "1 second"
	case seconds < 60:
		return fmt.Sprintf("%d seconds", seconds)
	}
	minutes := int(d.Minutes())
	switch {
	case minutes == 1:
		return "About a minute"
	case minutes < 60:
		return fmt.Sprintf("%d minutes", minutes)
	}
	hours := int(d.Hours() + 0.5)
	switch {
	case hours == 1:
		return "About an hour"
	case hours < 48:
		return fmt.Sprintf("%d hours", hours)
	case hours < 24*7*2:
		return fmt.Sprintf("%d days", hours/24)
	case hours < 24*30*2:
		return fmt.Sprintf("%d weeks", hours/24/7)
	case hours < 24*365*2:
		return fmt.Sprintf("%d months", hours/24/30)
	}
	return fmt.Sprintf("%d years", int(d.Hours())/24/365)
}

// Counts tallies containers by state.
func Counts(cs []*asterismv1.Container) *asterismv1.ContainerCounts {
	out := &asterismv1.ContainerCounts{Total: uint32(len(cs))}
	for _, c := range cs {
		switch c.State {
		case asterismv1.ContainerState_CONTAINER_STATE_RUNNING:
			out.Running++
		case asterismv1.ContainerState_CONTAINER_STATE_PAUSED:
			out.Paused++
		case asterismv1.ContainerState_CONTAINER_STATE_RESTARTING:
			out.Restarting++
		case asterismv1.ContainerState_CONTAINER_STATE_EXITED:
			out.Exited++
		case asterismv1.ContainerState_CONTAINER_STATE_CREATED:
			out.Created++
		case asterismv1.ContainerState_CONTAINER_STATE_DEAD:
			out.Dead++
		}
		if c.Health == asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY {
			out.Unhealthy++
		}
		if NeedsAttention(c) {
			out.Attention++
		}
	}
	return out
}

// NeedsAttention reports whether a container is unhealthy, restarting, dead,
// or exited after an OOM kill.
func NeedsAttention(c *asterismv1.Container) bool {
	return c.Health == asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY ||
		c.State == asterismv1.ContainerState_CONTAINER_STATE_RESTARTING ||
		c.State == asterismv1.ContainerState_CONTAINER_STATE_DEAD ||
		(c.State == asterismv1.ContainerState_CONTAINER_STATE_EXITED && c.OomKilled)
}
