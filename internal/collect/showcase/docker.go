package showcase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// dockerSpec carries the inspect-level details a workload does not model.
type dockerSpec struct {
	created       time.Duration // age
	restartPolicy string
	cmd           []string
	entrypoint    []string
	health        []string // healthcheck test; nil = none
	mounts        []*asterismv1.ContainerMount
	cpuLimit      float64
	user          string
	labels        map[string]string
	privileged    bool
	exposedOnly   []uint32
	blkBase       float64
}

func vol(name, dest string, rw bool) *asterismv1.ContainerMount {
	return &asterismv1.ContainerMount{Type: "volume", Name: name, Source: "/var/lib/docker/volumes/" + name + "/_data", Destination: dest, ReadWrite: rw, Driver: "local"}
}

func bind(src, dest string, rw bool) *asterismv1.ContainerMount {
	mode := "ro"
	if rw {
		mode = "rw"
	}
	return &asterismv1.ContainerMount{Type: "bind", Source: src, Destination: dest, ReadWrite: rw, Mode: mode, DockerSocket: src == "/var/run/docker.sock"}
}

var showcaseSpecs = map[string]dockerSpec{
	"cloudflared":      {created: 340 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"tunnel", "--no-autoupdate", "run", "--token", "eyJhIjoiZGVtbyJ9"}, health: []string{"CMD", "cloudflared", "tunnel", "ready"}, cpuLimit: 0.5, user: "nonroot"},
	"caddy-edge":       {created: 220 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile"}, mounts: []*asterismv1.ContainerMount{bind("/srv/infra/edge-gateway/Caddyfile", "/etc/caddy/Caddyfile", false), vol("edge-gateway_caddy_data", "/data", true)}, health: []string{"CMD", "wget", "-qO-", "http://127.0.0.1:2019/config/"}, blkBase: 12_000},
	"nova-api":         {created: 94 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"/app/core-api", "serve", "--listen=:8080"}, health: []string{"CMD", "curl", "-fsS", "http://localhost:8080/healthz"}, cpuLimit: 2, user: "app", mounts: []*asterismv1.ContainerMount{vol("nova-core_uploads", "/app/uploads", true)}, blkBase: 40_000},
	"nova-worker":      {created: 94 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"/app/core-api", "worker", "--concurrency=8"}, cpuLimit: 1, user: "app", blkBase: 25_000},
	"postgres-primary": {created: 612 * time.Hour, restartPolicy: "always", cmd: []string{"postgres", "-c", "shared_buffers=512MB"}, entrypoint: []string{"docker-entrypoint.sh"}, health: []string{"CMD-SHELL", "pg_isready -U core"}, mounts: []*asterismv1.ContainerMount{vol("nova-core_pgdata", "/var/lib/postgresql/data", true), bind("/srv/apps/nova-core/initdb", "/docker-entrypoint-initdb.d", false)}, cpuLimit: 2, blkBase: 380_000},
	"redis-cache":      {created: 612 * time.Hour, restartPolicy: "always", cmd: []string{"valkey-server", "--appendonly", "yes", "--requirepass", "demo-pass"}, health: []string{"CMD", "valkey-cli", "ping"}, mounts: []*asterismv1.ContainerMount{vol("nova-core_valkey", "/data", true)}, blkBase: 18_000},
	"analytics-web":    {created: 48 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"node", "server.js"}, health: []string{"CMD", "wget", "-qO-", "http://localhost:3000/api/health"}, user: "node", blkBase: 6_000},
	"ingest-collector": {created: 300 * time.Hour, restartPolicy: "on-failure", cmd: []string{"/ingest", "--batch-size=5000"}, health: []string{"CMD", "/ingest", "healthcheck"}, cpuLimit: 1, blkBase: 90_000},
	"timescale-db":     {created: 340 * time.Hour, restartPolicy: "always", cmd: []string{"postgres"}, entrypoint: []string{"docker-entrypoint.sh"}, health: []string{"CMD-SHELL", "pg_isready"}, mounts: []*asterismv1.ContainerMount{vol("analytics-hub_tsdata", "/home/postgres/pgdata", true)}, cpuLimit: 2, blkBase: 260_000},
	"nats-broker":      {created: 340 * time.Hour, restartPolicy: "always", cmd: []string{"-js", "-sd", "/data"}, entrypoint: []string{"nats-server"}, mounts: []*asterismv1.ContainerMount{vol("analytics-hub_nats", "/data", true)}, health: []string{"CMD", "wget", "-qO-", "http://localhost:8222/healthz"}, blkBase: 30_000},
	"staging-preview":  {created: 14 * time.Hour, restartPolicy: "no", cmd: []string{"/app/core-api", "serve", "--listen=:4173"}, blkBase: 2_000},
	"asterism":         {created: 400 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"serve", "--config", "/etc/asterism/asterism.toml"}, entrypoint: []string{"/usr/local/bin/asterism"}, mounts: []*asterismv1.ContainerMount{bind("/var/run/docker.sock", "/var/run/docker.sock", false), bind("/etc/asterism", "/etc/asterism", false)}, labels: map[string]string{"asterism.self": "true"}, health: []string{"CMD", "/usr/local/bin/asterism", "version"}, cpuLimit: 0.5, blkBase: 1_000},
	"mailer":           {created: 30 * time.Hour, restartPolicy: "always", cmd: []string{"/mailer", "--smtp-url=smtp://mailer:SMTPpass@mail.internal:587"}, health: []string{"CMD", "/mailer", "ping"}},
	"backup-nightly":   {created: 200 * time.Hour, restartPolicy: "no", cmd: []string{"restic", "backup", "/data", "--password-file", "/run/secrets/restic"}, mounts: []*asterismv1.ContainerMount{vol("nova-core_pgdata", "/data/pg", false), bind("/var/run/docker.sock", "/var/run/docker.sock", true)}, privileged: true},
	"db-migrate":       {created: 94 * time.Hour, restartPolicy: "no", cmd: []string{"/app/core-api", "migrate", "up"}},
}

// imageIDs gives each image reference a stable fake id.
func fakeID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func (p *Provider) addExtraWorkloadsLocked(nowMs int64) {
	extra := []struct {
		wl   *asterismv1.Workload
		proj string
	}{
		{&asterismv1.Workload{
			Id: "ctr:asterism", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER, Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_APP,
			Name: "asterism", ProjectId: "proj:edge-gateway", ServiceName: "asterism", Image: "ghcr.io/prabhavalabs/asterism:0.1.0",
			Pid: 1880, State: "running", Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: nowMs - 400*3600*1000,
			CpuUtil: 0.006, MemUsedBytes: 31 * mb, MemLimitBytes: 256 * mb, Replicas: 1, Networks: []string{"edge_net"},
			Labels: map[string]string{"asterism.category": "infra", "asterism.self": "true"},
		}, "proj:edge-gateway"},
		{&asterismv1.Workload{
			Id: "ctr:mailer", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER, Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER,
			Name: "mailer", ProjectId: "proj:analytics-hub", ServiceName: "mailer", Image: "ghcr.io/example/mailer:v0.4.2",
			State: "unhealthy", Health: asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, RestartCount: 14, StartedAtMs: nowMs - 25_000,
			MemLimitBytes: 128 * mb, Replicas: 1, Networks: []string{"analytics_net"},
			EnvKeys: []string{"SMTP_URL", "FROM_ADDRESS"}, Labels: map[string]string{"asterism.category": "production"},
		}, "proj:analytics-hub"},
		{&asterismv1.Workload{
			Id: "ctr:backup-nightly", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER, Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_STORAGE,
			Name: "backup-nightly", ProjectId: "proj:nova-core", ServiceName: "backup", Image: "restic/restic:0.17.3",
			State: "stopped", Health: asterismv1.HealthStatus_HEALTH_STATUS_DOWN, StartedAtMs: nowMs - 9*3600*1000,
			MemLimitBytes: 256 * mb, Replicas: 1, Networks: []string{"core_net"},
			EnvKeys: []string{"RESTIC_REPOSITORY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}, Labels: map[string]string{"asterism.category": "production"},
		}, "proj:nova-core"},
	}
	for _, e := range extra {
		p.workloads[e.wl.Id] = e.wl
		for _, proj := range p.projects {
			if proj.Id == e.proj {
				proj.Workloads = append(proj.Workloads, e.wl)
			}
		}
	}
	for _, pb := range p.ports {
		if pb.Id == "port:7700" {
			pb.WorkloadId = "ctr:asterism"
			pb.WorkloadName = "asterism"
			p.workloads["ctr:asterism"].Ports = []*asterismv1.PortBinding{pb}
		}
	}
}

func (p *Provider) seedDocker() {
	now := time.Now()
	nowMs := now.UnixMilli()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.addExtraWorkloadsLocked(nowMs)

	p.containers = make(map[string]*asterismv1.Container)
	ipByNet := map[string]int{}
	netIdx := map[string]int{"bridge": 17, "edge_net": 18, "core_net": 19, "analytics_net": 20, "staging_net": 21}
	subnet := func(n string) int {
		if _, ok := netIdx[n]; !ok {
			netIdx[n] = 22 + len(netIdx) - 5
		}
		return netIdx[n]
	}

	build := func(w *asterismv1.Workload, project, workDir string) *asterismv1.Container {
		spec := p.specs[w.Name]
		id := fakeID("ctr:" + w.Name)
		repo, tag := dockercli.SplitImageRef(w.Image)
		c := &asterismv1.Container{
			Id: w.Id, ContainerId: dockercli.ShortID(id), Name: w.Name,
			Image: w.Image, ImageRepo: repo, ImageTag: tag, ImageId: dockercli.ShortImageID(fakeID("img:" + w.Image)),
			CreatedAtMs: nowMs - spec.created.Milliseconds(), StartedAtMs: w.StartedAtMs,
			RestartCount: w.RestartCount, RestartPolicy: spec.restartPolicy, Pid: w.Pid,
			ProjectId: w.ProjectId, ComposeProject: project, ComposeService: w.ServiceName, ComposeWorkingDir: workDir,
			Role: w.Role, NetworkMode: project + "_default",
			Command: dockercli.RedactArgs(spec.cmd), Entrypoint: dockercli.RedactArgs(spec.entrypoint),
			EnvKeys: append([]string(nil), w.EnvKeys...), User: spec.user, Privileged: spec.privileged,
			Mounts: spec.mounts,
			Resources: &asterismv1.ContainerResources{
				MemLimitBytes: w.MemLimitBytes, CpuLimitCores: spec.cpuLimit,
			},
			State:  asterismv1.ContainerState_CONTAINER_STATE_RUNNING,
			Health: asterismv1.ContainerHealth_CONTAINER_HEALTH_NONE,
		}
		if c.RestartPolicy == "" {
			c.RestartPolicy = "no"
		}
		labels := map[string]string{
			dockercli.LabelComposeProject:    project,
			dockercli.LabelComposeService:    w.ServiceName,
			dockercli.LabelComposeWorkingDir: workDir,
			"com.docker.compose.version":     "2.40.3",
		}
		for k, v := range w.Labels {
			labels[k] = v
		}
		for k, v := range spec.labels {
			labels[k] = v
		}
		labels["traefik.http.middlewares.admin.basicauth.users"] = "admin:$apr1$demo$hash" // filtered below
		c.Labels, c.RedactedLabelCount = dockercli.FilterLabels(labels)
		if len(spec.health) > 0 {
			c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY
			c.Healthcheck = &asterismv1.ContainerHealthcheck{
				Status: c.Health, Test: dockercli.RedactArgs(spec.health), IntervalSeconds: 30, TimeoutSeconds: 5, Retries: 3,
				LastOutput: "ok", LastCheckAtMs: nowMs - 12_000,
			}
		}
		for _, n := range w.Networks {
			if n == "host" {
				continue
			}
			ipByNet[n]++
			idx := subnet(n)
			c.Networks = append(c.Networks, &asterismv1.ContainerNetwork{
				Name: n, IpAddress: fmt.Sprintf("172.%d.0.%d", idx, ipByNet[n]+1), Gateway: fmt.Sprintf("172.%d.0.1", idx),
				Aliases: []string{w.Name, w.ServiceName},
			})
		}
		for _, pb := range w.Ports {
			c.Ports = append(c.Ports, &asterismv1.ContainerPort{
				HostIp: pb.HostIp, HostPort: pb.HostPort, ContainerPort: pb.ContainerPort, Proto: pb.Proto,
				Published: pb.HostPort > 0, Exposure: pb.Exposure,
			})
		}
		return c
	}

	for _, proj := range p.projects {
		for _, w := range proj.Workloads {
			if w.Kind != asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER {
				continue
			}
			p.containers[w.Name] = build(w, proj.Name, proj.WorkingDir)
		}
	}

	// ingest-collector: running but failing its healthcheck after an earlier OOM.
	if c := p.containers["ingest-collector"]; c != nil {
		c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY
		c.Healthcheck.Status = c.Health
		c.Healthcheck.FailingStreak = 3
		c.Healthcheck.LastExitCode = 1
		c.Healthcheck.LastOutput = "health: nats publish latency 2.4s > 1s budget"
		c.RestartCount = 2
	}
	// mailer: crash loop (bad SMTP credentials).
	if c := p.containers["mailer"]; c != nil {
		c.State = asterismv1.ContainerState_CONTAINER_STATE_RESTARTING
		c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY
		c.Healthcheck.Status = c.Health
		c.Healthcheck.FailingStreak = 5
		c.Healthcheck.LastExitCode = 1
		c.Healthcheck.LastOutput = "dial tcp 10.0.4.2:587: connect: connection refused"
		c.ExitCode = 1
		c.FinishedAtMs = nowMs - 4_000
		c.Error = ""
		c.Pid = 0
	}
	// backup-nightly: OOM-killed last night, left stopped.
	if c := p.containers["backup-nightly"]; c != nil {
		c.State = asterismv1.ContainerState_CONTAINER_STATE_EXITED
		c.ExitCode = 137
		c.OomKilled = true
		c.StartedAtMs = nowMs - 9*3600*1000
		c.FinishedAtMs = nowMs - 8*3600*1000 - 22*60*1000
		c.Pid = 0
	}
	// asterism: no published ports beyond loopback.
	// db-migrate: completed one-shot job (not part of the topology, like the SSH bridge).
	mig := build(&asterismv1.Workload{
		Id: "ctr:db-migrate", Name: "db-migrate", ProjectId: "proj:nova-core", ServiceName: "migrate",
		Image: "ghcr.io/example/core-api:v2.4.1", Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER,
		StartedAtMs: nowMs - 94*3600*1000, Networks: []string{"core_net"}, EnvKeys: []string{"DATABASE_URL"},
	}, "nova-core", "/srv/apps/nova-core")
	mig.State = asterismv1.ContainerState_CONTAINER_STATE_EXITED
	mig.ExitCode = 0
	mig.FinishedAtMs = mig.StartedAtMs + 41_000
	p.containers[mig.Name] = mig

	for _, c := range p.containers {
		if c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
			c.Stats = &asterismv1.ContainerStats{}
		}
	}
	p.addStandaloneLocked(nowMs, build)
	p.stepDockerLocked(now)
	p.seedLogsLocked(nowMs)

	// Images, volumes, networks, disk usage.
	seenImg := map[string]bool{}
	for _, c := range p.containers {
		if seenImg[c.ImageId] {
			continue
		}
		seenImg[c.ImageId] = true
		h := fakeID(c.Image)
		size := uint64(40+int(h[0])%200) * 1_000_000
		p.images = append(p.images, &asterismv1.DockerImage{
			Id: c.ImageId, RepoTags: []string{c.Image}, Repository: c.ImageRepo, Tag: c.ImageTag,
			SizeBytes: size, CreatedAtMs: c.CreatedAtMs - 36*3600*1000,
		})
	}
	p.images = append(p.images,
		&asterismv1.DockerImage{Id: dockercli.ShortImageID(fakeID("img:old-core")), RepoTags: []string{"ghcr.io/example/core-api:v2.3.9"}, Repository: "ghcr.io/example/core-api", Tag: "v2.3.9", SizeBytes: 182_400_000, CreatedAtMs: nowMs - 30*24*3600*1000},
		&asterismv1.DockerImage{Id: dockercli.ShortImageID(fakeID("img:dangling-1")), Repository: "<none>", Tag: "<none>", Dangling: true, SizeBytes: 176_900_000, CreatedAtMs: nowMs - 12*24*3600*1000},
		&asterismv1.DockerImage{Id: dockercli.ShortImageID(fakeID("img:dangling-2")), Repository: "<none>", Tag: "<none>", Dangling: true, SizeBytes: 94_300_000, CreatedAtMs: nowMs - 21*24*3600*1000},
	)
	for _, img := range extraImages(nowMs) {
		if !seenImg[img.Id] {
			seenImg[img.Id] = true
			p.images = append(p.images, img)
		}
	}
	sort.Slice(p.images, func(i, j int) bool {
		if p.images[i].Dangling != p.images[j].Dangling {
			return !p.images[i].Dangling
		}
		return p.images[i].Repository+":"+p.images[i].Tag < p.images[j].Repository+":"+p.images[j].Tag
	})

	volSizes := map[string]int64{
		"nova-core_pgdata": 4_820_000_000, "nova-core_valkey": 212_000_000, "nova-core_uploads": 1_340_000_000,
		"analytics-hub_tsdata": 7_910_000_000, "analytics-hub_nats": 640_000_000, "edge-gateway_caddy_data": 3_100_000,
		"staging-lab_old_pgdata": 1_120_000_000, fakeID("anon-vol"): 88_000_000,
	}
	for name, size := range extraVolumes() {
		volSizes[name] = size
	}
	// Every named volume a container mounts exists, sized deterministically.
	for _, c := range p.containers {
		for _, m := range c.Mounts {
			if _, ok := volSizes[m.Name]; m.Type == "volume" && !ok {
				volSizes[m.Name] = int64(20+fhash(m.Name)%900) * 1_000_000
			}
		}
	}
	for name, size := range volSizes {
		proj := ""
		if i := indexByte(name, '_'); i > 0 && len(name) != 64 {
			proj = name[:i]
		}
		p.volumes = append(p.volumes, &asterismv1.DockerVolume{
			Name: name, Driver: "local", Scope: "local", Mountpoint: "/var/lib/docker/volumes/" + name + "/_data",
			CreatedAtMs: nowMs - 200*24*3600*1000 + int64(size%1000)*3600*1000, SizeBytes: size, ComposeProject: proj, Anonymous: len(name) == 64,
		})
	}
	sort.Slice(p.volumes, func(i, j int) bool {
		if p.volumes[i].Anonymous != p.volumes[j].Anonymous {
			return !p.volumes[i].Anonymous
		}
		return p.volumes[i].Name < p.volumes[j].Name
	})

	netProj := map[string]string{"edge_net": "edge-gateway", "core_net": "nova-core", "analytics_net": "analytics-hub", "staging_net": "staging-lab"}
	userNets := map[string]bool{}
	for _, c := range p.containers {
		for _, n := range c.Networks {
			if n.Name != "bridge" {
				userNets[n.Name] = true
			}
		}
	}
	for _, n := range extraNetworks {
		userNets[n] = true
	}
	for _, n := range dockercli.SortedKeys(userNets) {
		proj := netProj[n]
		if proj == "" {
			if i := strings.LastIndexByte(n, '_'); i > 0 {
				proj = n[:i]
			}
		}
		idx := subnet(n)
		dn := &asterismv1.DockerNetwork{
			Id: dockercli.ShortID(fakeID("net:" + n)), Name: n, Driver: "bridge", Scope: "local",
			Subnets: []string{fmt.Sprintf("172.%d.0.0/16", idx)}, Gateways: []string{fmt.Sprintf("172.%d.0.1", idx)},
			CreatedAtMs: nowMs - int64(100+fhash(n)%800)*3600*1000, ComposeProject: proj, Internal: n == "core_net",
		}
		if dn.CreatedAtMs > nowMs-612*3600*1000 && netProj[n] != "" {
			dn.CreatedAtMs = nowMs - 612*3600*1000
		}
		p.networks = append(p.networks, dn)
	}
	for _, n := range []string{"bridge", "host", "none"} {
		dn := &asterismv1.DockerNetwork{Id: dockercli.ShortID(fakeID("net:" + n)), Name: n, Driver: n, Scope: "local", Builtin: true, CreatedAtMs: nowMs - 900*3600*1000}
		if n == "bridge" {
			dn.Driver = "bridge"
			dn.Subnets, dn.Gateways = []string{"172.17.0.0/16"}, []string{"172.17.0.1"}
		} else if n == "none" {
			dn.Driver = "null"
		}
		p.networks = append(p.networks, dn)
	}

	p.engine = &asterismv1.DockerEngine{
		Available: true, ServerVersion: "28.5.1", OperatingSystem: "Ubuntu 24.04.3 LTS", KernelVersion: "6.8.0-88-generic",
		Architecture: "x86_64", StorageDriver: "overlay2", CgroupDriver: "systemd", CgroupVersion: "2", LoggingDriver: "json-file",
		DockerRootDir: "/var/lib/docker", Ncpu: 4, MemTotalBytes: 8 * gb,
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// stepDockerLocked advances simulated container stats and returns live samples.
func (p *Provider) stepDockerLocked(now time.Time) []bus.MetricSample {
	tsMs := now.UnixMilli()
	phase := float64(p.tick) * 0.18
	var samples []bus.MetricSample
	i := 0
	for _, name := range dockercli.SortedKeys(p.containers) {
		c := p.containers[name]
		i++
		// Crash loop: mailer comes back for a few seconds every 45 s, then dies again.
		if c.Name == "mailer" && c.State == asterismv1.ContainerState_CONTAINER_STATE_RESTARTING && p.tick > 0 && p.tick%45 == 0 {
			c.RestartCount++
			c.StartedAtMs = tsMs
			c.FinishedAtMs = tsMs
		}
		// Health starting -> healthy 8 s after (re)start.
		if c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING && c.Health == asterismv1.ContainerHealth_CONTAINER_HEALTH_STARTING && tsMs-c.StartedAtMs > 8_000 {
			c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY
			if c.Healthcheck != nil {
				c.Healthcheck.Status = c.Health
				c.Healthcheck.FailingStreak = 0
				c.Healthcheck.LastOutput = "ok"
			}
		}
		if c.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING && c.State != asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
			c.Stats = nil
			continue
		}
		w := p.workloads[c.Id]
		if c.Stats == nil {
			c.Stats = &asterismv1.ContainerStats{}
		}
		st := c.Stats
		st.TsMs = tsMs
		if c.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
			st.CpuUtil, st.NetRxBps, st.NetTxBps, st.BlkReadBps, st.BlkWriteBps = 0, 0, 0, 0, 0
		} else {
			wave := 1 + 0.25*math.Sin(phase+float64(i)*0.9)
			var rx, tx float64
			if w != nil {
				st.CpuUtil = w.CpuUtil
				st.MemUsedBytes = w.MemUsedBytes
				rx, tx = w.NetRxBps, w.NetTxBps
			}
			if rx == 0 {
				rx, tx = 12_000, 9_000
			}
			spec := p.specs[c.Name]
			st.NetRxBps = rx * wave
			st.NetTxBps = tx * (2 - wave)
			st.BlkReadBps = spec.blkBase * 0.3 * (1 + 0.5*math.Sin(phase*0.6+float64(i)))
			st.BlkWriteBps = spec.blkBase * (1 + 0.4*math.Cos(phase*0.4+float64(i)))
			st.NetRxBytes += uint64(st.NetRxBps)
			st.NetTxBytes += uint64(st.NetTxBps)
			st.BlkReadBytes += uint64(st.BlkReadBps)
			st.BlkWriteBytes += uint64(st.BlkWriteBps)
			st.Pids = uint32(6 + (i*7)%40)
		}
		st.MemLimitBytes = 8 * gb
		if c.Resources != nil && c.Resources.MemLimitBytes > 0 {
			st.MemLimitBytes = c.Resources.MemLimitBytes
		}
		if st.MemLimitBytes > 0 {
			st.MemUtil = float64(st.MemUsedBytes) / float64(st.MemLimitBytes)
		}
		samples = append(samples,
			bus.MetricSample{EntityID: c.Id, Metric: "net.rx_bps", TSMs: tsMs, Value: st.NetRxBps},
			bus.MetricSample{EntityID: c.Id, Metric: "net.tx_bps", TSMs: tsMs, Value: st.NetTxBps},
			bus.MetricSample{EntityID: c.Id, Metric: "blk.read_bps", TSMs: tsMs, Value: st.BlkReadBps},
			bus.MetricSample{EntityID: c.Id, Metric: "blk.write_bps", TSMs: tsMs, Value: st.BlkWriteBps},
			bus.MetricSample{EntityID: c.Id, Metric: "pids.count", TSMs: tsMs, Value: float64(st.Pids)},
		)
	}
	return samples
}

// containerRunning reports whether the workload's container should emit cpu/mem samples.
func (p *Provider) containerRunningLocked(name string) bool {
	c, ok := p.containers[name]
	return !ok || c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING
}

// DockerSnapshot implements the service DockerProvider interface.
func (p *Provider) DockerSnapshot() dockercli.Snapshot {
	now := time.Now()
	p.mu.RLock()
	defer p.mu.RUnlock()
	snap := dockercli.Snapshot{
		Engine:        proto.Clone(p.engine).(*asterismv1.DockerEngine),
		CollectedAtMs: now.UnixMilli(),
	}
	for _, name := range dockercli.SortedKeys(p.containers) {
		c := proto.Clone(p.containers[name]).(*asterismv1.Container)
		c.UptimeSeconds = 0
		if (c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING || c.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED) && c.StartedAtMs > 0 {
			c.UptimeSeconds = uint64((now.UnixMilli() - c.StartedAtMs) / 1000)
		}
		c.Status = dockercli.HumanStatus(c, now)
		c.StatusLevel = dockercli.StatusLevel(c.State, c.Health)
		snap.Containers = append(snap.Containers, c)
	}
	for _, img := range p.images {
		snap.Images = append(snap.Images, proto.Clone(img).(*asterismv1.DockerImage))
	}
	for _, v := range p.volumes {
		snap.Volumes = append(snap.Volumes, proto.Clone(v).(*asterismv1.DockerVolume))
	}
	byNet := map[string][]*asterismv1.NetworkAttachment{}
	for _, c := range snap.Containers {
		if c.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING && c.State != asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
			continue
		}
		for _, n := range c.Networks {
			byNet[n.Name] = append(byNet[n.Name], &asterismv1.NetworkAttachment{ContainerId: c.ContainerId, Name: c.Name, Ipv4Address: n.IpAddress + "/16"})
		}
	}
	for _, n := range p.networks {
		dn := proto.Clone(n).(*asterismv1.DockerNetwork)
		dn.Containers = byNet[n.Name]
		snap.Networks = append(snap.Networks, dn)
	}
	dockercli.LinkUsage(snap.Containers, snap.Images, snap.Volumes)

	var imgSize, imgRecl, volSize, volRecl uint64
	var imgActive, volActive, running uint32
	for _, img := range snap.Images {
		imgSize += img.SizeBytes
		if img.Containers == 0 {
			imgRecl += img.SizeBytes
		} else {
			imgActive++
		}
	}
	for _, v := range snap.Volumes {
		volSize += uint64(v.SizeBytes)
		if v.Containers == 0 {
			volRecl += uint64(v.SizeBytes)
		} else {
			volActive++
		}
	}
	counts := dockercli.Counts(snap.Containers)
	running = counts.Running
	du := &asterismv1.DockerDiskUsage{CollectedAtMs: now.UnixMilli() - 90_000}
	du.Categories = []*asterismv1.DiskUsageCategory{
		{Kind: "images", TotalCount: uint32(len(snap.Images)), ActiveCount: imgActive, SizeBytes: imgSize, ReclaimableBytes: imgRecl},
		{Kind: "containers", TotalCount: counts.Total, ActiveCount: running, SizeBytes: 48_200_000, ReclaimableBytes: 3_400_000},
		{Kind: "volumes", TotalCount: uint32(len(snap.Volumes)), ActiveCount: volActive, SizeBytes: volSize, ReclaimableBytes: volRecl},
		{Kind: "build_cache", TotalCount: 63, ActiveCount: 0, SizeBytes: 2_310_000_000, ReclaimableBytes: 2_310_000_000},
	}
	for _, cat := range du.Categories {
		du.TotalSizeBytes += cat.SizeBytes
		du.TotalReclaimableBytes += cat.ReclaimableBytes
	}
	snap.DiskUsage = du
	snap.Engine.Containers = counts.Total
	snap.Engine.ContainersRunning = counts.Running + counts.Restarting
	snap.Engine.ContainersPaused = counts.Paused
	snap.Engine.ContainersStopped = counts.Exited + counts.Created + counts.Dead
	snap.Engine.Images = uint32(len(snap.Images))
	snap.Engine.CollectedAtMs = now.UnixMilli()
	return snap
}

// SupportedContainerActions implements the service ContainerActuator interface.
func (p *Provider) SupportedContainerActions() []asterismv1.ContainerAction {
	return []asterismv1.ContainerAction{
		asterismv1.ContainerAction_CONTAINER_ACTION_START,
		asterismv1.ContainerAction_CONTAINER_ACTION_STOP,
		asterismv1.ContainerAction_CONTAINER_ACTION_RESTART,
		asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE,
		asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE,
	}
}

// ContainerAction simulates Docker state transitions in memory.
func (p *Provider) ContainerAction(ctx context.Context, action asterismv1.ContainerAction, containerID string, _ time.Duration) (*asterismv1.Container, error) {
	// A short, cancellable pause makes the UI's pending state visible.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(p.actionDelay):
	}
	now := time.Now()
	nowMs := now.UnixMilli()

	p.mu.Lock()
	var c *asterismv1.Container
	for _, cand := range p.containers {
		if cand.ContainerId == containerID {
			c = cand
		}
	}
	if c == nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("Error response from daemon: No such container: %s", containerID)
	}
	startFresh := func() {
		c.State = asterismv1.ContainerState_CONTAINER_STATE_RUNNING
		c.StartedAtMs = nowMs
		c.ExitCode = 0
		c.OomKilled = false
		c.Error = ""
		c.Pid = int32(2000 + nowMs%30000)
		c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_NONE
		if c.Healthcheck != nil {
			c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_STARTING
			c.Healthcheck.Status = c.Health
			c.Healthcheck.FailingStreak = 0
		}
		c.Stats = &asterismv1.ContainerStats{}
	}
	stop := func() {
		c.State = asterismv1.ContainerState_CONTAINER_STATE_EXITED
		c.ExitCode = 0
		c.FinishedAtMs = nowMs
		c.Pid = 0
		c.Stats = nil
	}
	switch action {
	case asterismv1.ContainerAction_CONTAINER_ACTION_START:
		if c.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
			if c.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
				p.mu.Unlock()
				return nil, fmt.Errorf("Error response from daemon: cannot start a paused container, try unpause instead")
			}
			startFresh()
		}
	case asterismv1.ContainerAction_CONTAINER_ACTION_STOP:
		if c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING || c.State == asterismv1.ContainerState_CONTAINER_STATE_RESTARTING || c.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
			stop()
		}
	case asterismv1.ContainerAction_CONTAINER_ACTION_RESTART:
		if c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
			c.FinishedAtMs = nowMs
		}
		startFresh()
	case asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE:
		if c.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
			p.mu.Unlock()
			return nil, fmt.Errorf("Error response from daemon: container %s is not running", containerID)
		}
		c.State = asterismv1.ContainerState_CONTAINER_STATE_PAUSED
	case asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE:
		if c.State != asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
			p.mu.Unlock()
			return nil, fmt.Errorf("Error response from daemon: container %s is not paused", containerID)
		}
		c.State = asterismv1.ContainerState_CONTAINER_STATE_RUNNING
	default:
		p.mu.Unlock()
		return nil, fmt.Errorf("unsupported action %v", action)
	}
	p.syncWorkloadLocked(c)
	p.mu.Unlock()

	snap := p.DockerSnapshot()
	for _, sc := range snap.Containers {
		if sc.ContainerId == containerID {
			return sc, nil
		}
	}
	return nil, fmt.Errorf("container %s vanished", containerID)
}

// syncWorkloadLocked mirrors container state onto the topology workload.
func (p *Provider) syncWorkloadLocked(c *asterismv1.Container) {
	w, ok := p.workloads[c.Id]
	if !ok {
		return
	}
	w.StartedAtMs = c.StartedAtMs
	w.RestartCount = c.RestartCount
	switch c.State {
	case asterismv1.ContainerState_CONTAINER_STATE_RUNNING:
		w.State = "running"
		w.Health = asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY
		if w.MemUsedBytes == 0 {
			w.MemUsedBytes = 32 * mb
			w.CpuUtil = 0.01
		}
	case asterismv1.ContainerState_CONTAINER_STATE_PAUSED:
		w.State = "paused"
		w.Health = asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED
		w.CpuUtil = 0
	default:
		w.State = "stopped"
		w.Health = asterismv1.HealthStatus_HEALTH_STATUS_DOWN
		w.CpuUtil = 0
		w.MemUsedBytes = 0
	}
}
