package showcase

import (
	"fmt"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Options tunes the size of the showcase data set.
type Options struct {
	// Fleet adds this many generated compose projects (3–4 containers each,
	// with ports, ingress routes, volumes, and networks) next to the curated
	// ones, to exercise pagination at realistic scale. 0 keeps only the
	// curated topology; values above MaxFleet are capped.
	Fleet int
}

// MaxFleet caps Options.Fleet.
const MaxFleet = 40

var fleetNames = []string{
	"billing", "status-page", "docs-site", "crm", "chat-bot", "image-resizer",
	"auth-gateway", "search", "newsletter", "uptime", "forms", "feature-flags",
	"invoicing", "media-cdn", "geo-lookup", "webhooks", "reports", "scheduler",
	"pdf-render", "translations",
}

var fleetCategories = []string{"production", "staging", "internal", "tools"}

// fleetName returns a stable, unique project name for index i.
func fleetName(i int) string {
	if i < len(fleetNames) {
		return fleetNames[i]
	}
	return fmt.Sprintf("%s-%d", fleetNames[i%len(fleetNames)], i/len(fleetNames)+1)
}

// fhash is a small deterministic hash for showcase numbers.
func fhash(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// seedFleetLocked appends n generated compose projects to the topology.
func (p *Provider) seedFleetLocked(n int, nowMs int64) {
	n = min(max(n, 0), MaxFleet)
	type dbKind struct {
		image, svc, proc string
		port             uint32
		health           []string
	}
	dbs := []dbKind{
		{"postgres:17.4-alpine", "postgres", "postgres", 5432, []string{"CMD-SHELL", "pg_isready"}},
		{"mariadb:11.8", "mariadb", "mariadbd", 3306, []string{"CMD", "healthcheck.sh", "--connect"}},
		{"mongo:8.0", "mongo", "mongod", 27017, []string{"CMD", "mongosh", "--eval", "db.adminCommand('ping')"}},
	}
	for i := 0; i < n; i++ {
		name := fleetName(i)
		projID := "proj:" + name
		net := name + "_net"
		cat := fleetCategories[i%len(fleetCategories)]
		h := fhash(name)
		db := dbs[i%len(dbs)]
		version := fmt.Sprintf("v%d.%d.%d", 1+h%3, h%12, h%7)
		image := "ghcr.io/example/" + name + ":" + version
		ageH := int64(12 + h%600)
		labels := map[string]string{"asterism.category": cat}

		mk := func(suffix, svc, img string, role asterismv1.WorkloadRole, cpu float64, memMB, limitMB uint64) *asterismv1.Workload {
			w := &asterismv1.Workload{
				Id: "ctr:" + name + "-" + suffix, Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER, Role: role,
				Name: name + "-" + suffix, ProjectId: projID, ServiceName: svc, Image: img,
				Pid: int32(7000 + i*40 + len(suffix)), State: "running", Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY,
				StartedAtMs: nowMs - ageH*3600*1000, CpuUtil: cpu, MemUsedBytes: memMB * mb, MemLimitBytes: limitMB * mb,
				NetRxBps: float64(20_000 + h%90_000), NetTxBps: float64(15_000 + h%120_000), Replicas: 1,
				Networks: []string{net}, Labels: labels,
			}
			p.workloads[w.Id] = w
			return w
		}
		web := mk("web", "web", image, asterismv1.WorkloadRole_WORKLOAD_ROLE_APP, 0.01+float64(h%40)/1000, 80+uint64(h%220), 512)
		worker := mk("worker", "worker", image, asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER, 0.005+float64(h%25)/1000, 60+uint64(h%160), 384)
		dbw := mk("db", db.svc, db.image, asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE, 0.008+float64(h%30)/1000, 180+uint64(h%500), 1024)
		web.EnvKeys = []string{"DATABASE_URL", "PORT", "APP_ENV"}
		worker.EnvKeys = []string{"DATABASE_URL", "QUEUE_CONCURRENCY"}

		webPort := uint32(10100 + i)
		web.Ports = []*asterismv1.PortBinding{p.addFleetPort(fmt.Sprintf("port:%d", webPort), web, "127.0.0.1", webPort, 8080, asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, name)}
		dbw.Ports = []*asterismv1.PortBinding{p.addFleetPort(fmt.Sprintf("port:%s-db-%d", name, db.port), dbw, "", 0, db.port, asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, db.proc)}

		workloads := []*asterismv1.Workload{web, worker, dbw}
		status, score := asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, uint32(92+h%9)
		if i%5 == 4 {
			worker.Health = asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED
			worker.RestartCount = 3 + h%4
			status, score = asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, 70+h%10
		}
		p.edges = append(p.edges,
			&asterismv1.TopologyEdge{Id: "edge:" + name + "-web-db", SourceId: web.Id, TargetId: dbw.Id, Proto: "tcp", Port: db.port, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: fmt.Sprintf("%s :%d", db.svc, db.port), ConnCount: 4 + h%20, BytesPerSec: float64(30_000 + h%200_000)},
			&asterismv1.TopologyEdge{Id: "edge:" + name + "-worker-db", SourceId: worker.Id, TargetId: dbw.Id, Proto: "tcp", Port: db.port, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: fmt.Sprintf("%s :%d", db.svc, db.port), ConnCount: 2 + h%6, BytesPerSec: float64(10_000 + h%80_000)},
			&asterismv1.TopologyEdge{Id: "edge:caddy-" + name, SourceId: "ctr:caddy-edge", TargetId: web.Id, Proto: "tcp", Port: 8080, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_INGRESS, Source: asterismv1.EdgeSource_EDGE_SOURCE_PROXY, Label: "HTTPS :8080", ConnCount: 2 + h%30, BytesPerSec: float64(20_000 + h%300_000), Weight: 1},
		)
		if i%3 == 0 {
			cache := mk("cache", "valkey", "valkey/valkey:8.1-alpine", asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE, 0.004, 24+uint64(h%60), 256)
			cache.Ports = []*asterismv1.PortBinding{p.addFleetPort("port:"+name+"-cache-6379", cache, "", 0, 6379, asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, "valkey-server")}
			workloads = append(workloads, cache)
			p.edges = append(p.edges, &asterismv1.TopologyEdge{Id: "edge:" + name + "-web-cache", SourceId: web.Id, TargetId: cache.Id, Proto: "tcp", Port: 6379, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_CACHE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "valkey :6379", ConnCount: 3 + h%9, BytesPerSec: float64(8_000 + h%50_000)})
			p.specs[cache.Name] = dockerSpec{created: time.Duration(ageH) * time.Hour, restartPolicy: "always", cmd: []string{"valkey-server"}, health: []string{"CMD", "valkey-cli", "ping"}, blkBase: 3_000}
		}

		p.projects = append(p.projects, &asterismv1.Project{
			Id: projID, Name: name, Kind: "compose", Category: cat, WorkingDir: "/srv/fleet/" + name,
			Status: status, HealthScore: score, Workloads: workloads,
		})
		p.ingress = append(p.ingress, &asterismv1.IngressRoute{
			Id: "ingress:caddy/" + name + ".example.com", ProxyKind: asterismv1.ProxyKind_PROXY_KIND_CADDY,
			ProxyWorkloadId: "ctr:caddy-edge", Hostnames: []string{name + ".example.com"},
			ListenPort: 8443, Tls: true, PathPrefix: "/", Strategy: "single",
			Upstreams: []*asterismv1.UpstreamTarget{{WorkloadId: web.Id, Address: fmt.Sprintf("%s:8080", web.Name), Weight: 1, Healthy: true}},
		})

		created := time.Duration(ageH) * time.Hour
		p.specs[web.Name] = dockerSpec{created: created, restartPolicy: "unless-stopped", cmd: []string{"/app/server", "--listen=:8080"}, health: []string{"CMD", "wget", "-qO-", "http://localhost:8080/healthz"}, user: "app", cpuLimit: 1, blkBase: 4_000}
		p.specs[worker.Name] = dockerSpec{created: created, restartPolicy: "unless-stopped", cmd: []string{"/app/server", "worker"}, user: "app", cpuLimit: 0.5, blkBase: 2_000}
		p.specs[dbw.Name] = dockerSpec{created: created + 200*time.Hour, restartPolicy: "always", health: db.health, mounts: []*asterismv1.ContainerMount{vol(name+"_dbdata", "/var/lib/"+db.svc, true)}, cpuLimit: 1, blkBase: 40_000}
	}
}

func (p *Provider) addFleetPort(id string, w *asterismv1.Workload, ip string, hostPort, ctrPort uint32, exp asterismv1.PortExposure, proc string) *asterismv1.PortBinding {
	pb := &asterismv1.PortBinding{
		Id: id, WorkloadId: w.Id, WorkloadName: w.Name, ProjectId: w.ProjectId, HostIp: ip,
		HostPort: hostPort, ContainerPort: ctrPort, Proto: "tcp", Exposure: exp, ProcessName: proc,
	}
	p.ports = append(p.ports, pb)
	return pb
}

// standalone describes a container started outside compose (docker run).
type standalone struct {
	name, image string
	state       asterismv1.ContainerState
	ago         time.Duration // since start (running) or since exit
	exitCode    int32
	oom         bool
	cpu         float64
	memMB       uint64
	spec        dockerSpec
	ports       []*asterismv1.PortBinding
	network     string
}

func standaloneContainers() []standalone {
	running, exited := asterismv1.ContainerState_CONTAINER_STATE_RUNNING, asterismv1.ContainerState_CONTAINER_STATE_EXITED
	sock := bind("/var/run/docker.sock", "/var/run/docker.sock", true)
	port := func(name, ip string, host, ctr uint32, exp asterismv1.PortExposure, proc string) *asterismv1.PortBinding {
		return &asterismv1.PortBinding{Id: fmt.Sprintf("port:%d", host), WorkloadId: "ctr:" + name, WorkloadName: name, HostIp: ip, HostPort: host, ContainerPort: ctr, Proto: "tcp", Exposure: exp, ProcessName: proc}
	}
	return []standalone{
		{name: "watchtower", image: "containrrr/watchtower:1.7.1", state: running, ago: 300 * time.Hour, cpu: 0.002, memMB: 14,
			spec: dockerSpec{created: 300 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"--cleanup", "--schedule", "0 0 4 * * *"}, mounts: []*asterismv1.ContainerMount{sock}}},
		{name: "node-exporter", image: "prom/node-exporter:v1.9.1", state: running, ago: 612 * time.Hour, cpu: 0.004, memMB: 18, network: "host",
			spec:  dockerSpec{created: 612 * time.Hour, restartPolicy: "unless-stopped", cmd: []string{"--path.rootfs=/host"}, mounts: []*asterismv1.ContainerMount{bind("/", "/host", false)}, user: "nobody"},
			ports: []*asterismv1.PortBinding{port("node-exporter", "0.0.0.0", 9100, 9100, asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, "node_exporter")}},
		{name: "cadvisor", image: "gcr.io/cadvisor/cadvisor:v0.52.1", state: running, ago: 612 * time.Hour, cpu: 0.031, memMB: 92,
			spec:  dockerSpec{created: 612 * time.Hour, restartPolicy: "unless-stopped", privileged: true, mounts: []*asterismv1.ContainerMount{bind("/var/run", "/var/run", false), bind("/sys", "/sys", false)}, health: []string{"CMD", "wget", "-qO-", "http://localhost:8080/healthz"}, blkBase: 4_000},
			ports: []*asterismv1.PortBinding{port("cadvisor", "127.0.0.1", 8088, 8080, asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, "cadvisor")}},
		{name: "portainer_agent", image: "portainer/agent:2.27.6", state: running, ago: 150 * time.Hour, cpu: 0.003, memMB: 22,
			spec: dockerSpec{created: 150 * time.Hour, restartPolicy: "always", mounts: []*asterismv1.ContainerMount{sock, bind("/var/lib/docker/volumes", "/var/lib/docker/volumes", false)}}},
		{name: "buildx_buildkit_builder0", image: "moby/buildkit:buildx-stable-1", state: running, ago: 30 * time.Hour, cpu: 0.001, memMB: 64,
			spec: dockerSpec{created: 400 * time.Hour, restartPolicy: "unless-stopped", privileged: true, mounts: []*asterismv1.ContainerMount{vol("buildx_buildkit_builder0_state", "/var/lib/buildkit", true)}, blkBase: 9_000}},
		{name: "registry-mirror", image: "registry:2.8.3", state: running, ago: 400 * time.Hour, cpu: 0.002, memMB: 26,
			spec:  dockerSpec{created: 400 * time.Hour, restartPolicy: "always", mounts: []*asterismv1.ContainerMount{vol("registry_data", "/var/lib/registry", true)}, blkBase: 7_000},
			ports: []*asterismv1.PortBinding{port("registry-mirror", "127.0.0.1", 5000, 5000, asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, "registry")}},
		{name: "uptime-probe", image: "louislam/uptime-kuma:1.23.16", state: running, ago: 72 * time.Hour, cpu: 0.012, memMB: 118,
			spec:  dockerSpec{created: 200 * time.Hour, restartPolicy: "unless-stopped", mounts: []*asterismv1.ContainerMount{vol("uptime-kuma_data", "/app/data", true)}, health: []string{"CMD", "extra/healthcheck"}, blkBase: 5_000},
			ports: []*asterismv1.PortBinding{port("uptime-probe", "127.0.0.1", 3001, 3001, asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, "node")}},
		{name: "nostalgic_hopper", image: "alpine:3.22", state: exited, ago: 70 * time.Hour,
			spec: dockerSpec{created: 70 * time.Hour, cmd: []string{"sh"}}},
		{name: "eager_turing", image: "curlimages/curl:8.14.1", state: exited, ago: 46 * time.Hour, exitCode: 6,
			spec: dockerSpec{created: 46 * time.Hour, cmd: []string{"-fsS", "https://analytics.internal/health"}}},
		{name: "pg-dump-20261120", image: "postgres:17.4-alpine", state: exited, ago: 96 * time.Hour,
			spec: dockerSpec{created: 97 * time.Hour, cmd: []string{"pg_dump", "-Fc", "-f", "/backup/core.dump", "--password", "dump-pass"}, mounts: []*asterismv1.ContainerMount{bind("/srv/backups", "/backup", true)}}},
		{name: "certbot-renew", image: "certbot/certbot:v4.1.1", state: exited, ago: 20 * time.Hour,
			spec: dockerSpec{created: 900 * time.Hour, cmd: []string{"renew", "--quiet"}, mounts: []*asterismv1.ContainerMount{vol("certbot_conf", "/etc/letsencrypt", true)}}},
		{name: "trivy-scan", image: "aquasec/trivy:0.63.0", state: exited, ago: 5 * time.Hour, exitCode: 1,
			spec: dockerSpec{created: 5 * time.Hour, cmd: []string{"image", "--exit-code", "1", "--severity", "CRITICAL", "ghcr.io/example/core-api:v2.4.1"}, mounts: []*asterismv1.ContainerMount{vol("trivy_cache", "/root/.cache", true)}}},
		{name: "k6-loadtest", image: "grafana/k6:1.1.0", state: exited, ago: 28 * time.Hour, exitCode: 99,
			spec: dockerSpec{created: 28 * time.Hour, cmd: []string{"run", "/scripts/api.js"}, mounts: []*asterismv1.ContainerMount{bind("/srv/loadtest", "/scripts", false)}}},
		{name: "hello-world-check", image: "hello-world:latest", state: exited, ago: 800 * time.Hour,
			spec: dockerSpec{created: 800 * time.Hour, cmd: []string{"/hello"}}},
		{name: "staging-seed", image: "ghcr.io/example/core-api:sha-9c4f21a", state: asterismv1.ContainerState_CONTAINER_STATE_CREATED,
			spec: dockerSpec{created: 2 * time.Hour, cmd: []string{"/app/core-api", "seed", "--fixtures=demo"}}},
		{name: "preview-pr-212", image: "ghcr.io/example/core-api:sha-71be0d3", state: exited, ago: 52 * time.Hour, exitCode: 137, oom: true,
			spec: dockerSpec{created: 60 * time.Hour, restartPolicy: "no", cmd: []string{"/app/core-api", "serve", "--listen=:4174"}}},
		{name: "orphan-sidecar", image: "busybox:1.37", state: asterismv1.ContainerState_CONTAINER_STATE_DEAD, ago: 130 * time.Hour,
			spec: dockerSpec{created: 140 * time.Hour, cmd: []string{"tail", "-f", "/dev/null"}}},
	}
}

// addStandaloneLocked adds docker-run containers that belong to no compose
// project and are not part of the topology (like db-migrate).
func (p *Provider) addStandaloneLocked(nowMs int64, build func(*asterismv1.Workload, string, string) *asterismv1.Container) {
	for _, s := range standaloneContainers() {
		p.specs[s.name] = s.spec
		network := s.network
		if network == "" {
			network = "bridge"
		}
		w := &asterismv1.Workload{
			Id: "ctr:" + s.name, Name: s.name, ProjectId: "proj:standalone", Image: s.image,
			Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER, Networks: []string{network},
			Ports: s.ports, StartedAtMs: nowMs - s.ago.Milliseconds(), Pid: int32(9000 + fhash(s.name)%5000),
		}
		c := build(w, "", "")
		c.ComposeProject, c.ComposeService, c.ComposeWorkingDir = "", "", ""
		c.NetworkMode = network
		labels := map[string]string{}
		for k, v := range c.Labels {
			if !strings.HasPrefix(k, "com.docker.compose.") {
				labels[k] = v
			}
		}
		c.Labels = labels
		c.State = s.state
		switch s.state {
		case asterismv1.ContainerState_CONTAINER_STATE_RUNNING:
			c.Stats = &asterismv1.ContainerStats{CpuUtil: s.cpu, MemUsedBytes: s.memMB * mb}
		case asterismv1.ContainerState_CONTAINER_STATE_CREATED:
			c.StartedAtMs, c.Pid = 0, 0
			c.Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_NONE
			c.Healthcheck = nil
		default:
			c.ExitCode, c.OomKilled, c.Pid = s.exitCode, s.oom, 0
			c.FinishedAtMs = nowMs - s.ago.Milliseconds()
			c.StartedAtMs = c.FinishedAtMs - int64(30_000+fhash(s.name)%600_000)
		}
		if network == "host" {
			c.Networks = nil
		}
		p.containers[s.name] = c
		p.ports = append(p.ports, s.ports...)
	}
}

// extraImages are older tags, base images, and dangling layers a long-lived
// VPS accumulates.
func extraImages(nowMs int64) []*asterismv1.DockerImage {
	day := int64(24 * 3600 * 1000)
	var out []*asterismv1.DockerImage
	add := func(ref string, sizeMB uint64, ageDays int64) {
		repo, tag := dockercli.SplitImageRef(ref)
		out = append(out, &asterismv1.DockerImage{
			Id: dockercli.ShortImageID(fakeID("img:" + ref)), RepoTags: []string{ref}, Repository: repo, Tag: tag,
			SizeBytes: sizeMB * 1_000_000, CreatedAtMs: nowMs - ageDays*day,
		})
	}
	for i, v := range []string{"v2.3.8", "v2.3.5", "v2.2.0", "sha-5e1d2f0", "sha-0b9a7c4"} {
		add("ghcr.io/example/core-api:"+v, 178+uint64(i)*3, 30+int64(i)*9)
	}
	for i, v := range []string{"v1.8.4", "v1.8.0", "v1.7.2"} {
		add("ghcr.io/example/analytics-web:"+v, 141+uint64(i)*2, 20+int64(i)*15)
		add("ghcr.io/example/ingest:"+v, 64+uint64(i), 20+int64(i)*15)
	}
	add("ghcr.io/example/mailer:v0.4.1", 38, 33)
	add("ghcr.io/prabhavalabs/asterism:0.0.9", 29, 41)
	for _, b := range []struct {
		ref  string
		size uint64
		age  int64
	}{
		{"node:22-alpine", 162, 18}, {"golang:1.25-alpine", 268, 22}, {"python:3.13-slim", 131, 26},
		{"ubuntu:24.04", 78, 40}, {"debian:bookworm-slim", 74, 44}, {"nginx:1.28-alpine", 48, 51},
		{"postgres:16.9-alpine", 271, 120}, {"redis:7.4-alpine", 41, 140}, {"grafana/grafana:12.1.0", 662, 60},
		{"prom/prometheus:v3.5.0", 312, 64}, {"traefik:v3.4", 182, 90}, {"minio/minio:RELEASE.2025-09-07T16-13-09Z", 175, 70},
		{"timescale/timescaledb:2.17-pg16", 812, 150}, {"caddy:2.9-alpine", 46, 160},
	} {
		add(b.ref, b.size, b.age)
	}
	for i := 0; i < 9; i++ {
		out = append(out, &asterismv1.DockerImage{
			Id:         dockercli.ShortImageID(fakeID(fmt.Sprintf("img:dangling-%d", i+3))),
			Repository: "<none>", Tag: "<none>", Dangling: true,
			SizeBytes:   uint64(40+fhash(fmt.Sprint(i))%220) * 1_000_000,
			CreatedAtMs: nowMs - int64(3+i*4)*day,
		})
	}
	return out
}

// extraVolumes are leftovers from removed stacks and anonymous volumes.
func extraVolumes() map[string]int64 {
	out := map[string]int64{
		"staging-lab_pr-198_pgdata": 420_000_000, "staging-lab_pr-205_pgdata": 388_000_000,
		"staging-lab_pr-212_pgdata": 512_000_000, "monitoring_grafana": 96_000_000,
		"monitoring_prometheus": 2_840_000_000, "legacy-wp_db": 610_000_000, "legacy-wp_uploads": 1_930_000_000,
		"minio_data": 5_200_000_000, "traefik_acme": 120_000, "buildx_buildkit_builder0_state": 3_410_000_000,
		"registry_data": 2_060_000_000, "uptime-kuma_data": 54_000_000, "certbot_conf": 410_000, "trivy_cache": 690_000_000,
		"analytics-hub_ch_old": -1, "nova-core_pgdata_v16": 4_100_000_000,
	}
	for i := 0; i < 12; i++ {
		out[fakeID(fmt.Sprintf("anon-vol-%d", i))] = int64(1+fhash(fmt.Sprint("v", i))%400) * 1_000_000
	}
	return out
}

// extraNetworks are user networks with no running containers.
var extraNetworks = []string{
	"monitoring_net", "legacy-wp_default", "staging-lab_pr-198", "staging-lab_pr-205", "staging-lab_pr-212",
	"minio_net", "loadtest_net", "buildx_default",
}

// seedEventHistoryLocked adds a week of plausible events, newest last.
func (p *Provider) seedEventHistoryLocked(nowMs int64) {
	type tmpl struct {
		entity, kind, sev, msg string
	}
	ts := []tmpl{
		{"ctr:nova-api", "container.health", "info", "Health check passed (HTTP 200 in %dms)"},
		{"ctr:ingest-collector", "alert.memory", "warning", "Memory reached %d%% of 512 MiB cgroup limit"},
		{"unit:nginx.service", "ingress.reload", "info", "Reloaded upstream pool api.example.com (%d healthy backends)"},
		{"ctr:mailer", "container.restart", "warning", "Container restarted (exit code 1), restart #%d"},
		{"ctr:postgres-primary", "alert.cpu", "warning", "CPU above 80%% for %d minutes"},
		{"ctr:caddy-edge", "tls.renew", "info", "ACME certificate verified (%dd remaining)"},
		{"ctr:backup-nightly", "backup.complete", "info", "Nightly backup finished in %d s"},
		{"ctr:timescale-db", "alert.disk", "warning", "Volume analytics-hub_tsdata at %d%% of disk"},
		{"ctr:backup-nightly", "container.oom", "critical", "OOM kill during restic prune (%d MiB peak)"},
		{"ctr:analytics-web", "container.start", "info", "Container started after deploy v1.9.%d"},
		{"ctr:cloudflared", "ingress.tunnel", "info", "Tunnel connection %d re-established"},
		{"ctr:mailer", "alert.health", "critical", "Healthcheck failing for %d consecutive probes"},
	}
	for i := 0; i < 180; i++ {
		t := ts[(i*7)%len(ts)]
		at := nowMs - 4*60*60*1000 - int64(i)*55*60*1000 - int64(fhash(fmt.Sprint(i))%600)*1000
		if i%17 == 1 {
			at = p.events[len(p.events)-1].TsMs // same-ms burst: exercises the id tie-break
		}
		name := strings.TrimPrefix(strings.TrimPrefix(t.entity, "ctr:"), "unit:")
		name = strings.TrimSuffix(name, ".service")
		p.events = append(p.events, &asterismv1.Event{
			Id: fmt.Sprintf("evt-h%03d", i), TsMs: at, EntityId: t.entity, EntityName: name,
			Kind: t.kind, Severity: t.sev, Message: fmt.Sprintf(t.msg, 2+int(fhash(fmt.Sprint(i, t.kind))%90)),
		})
	}
}

// seedLogsLocked gives every container a few hundred older log lines so the
// logs cursor has pages to walk. Existing recent lines are kept at the end.
func (p *Provider) seedLogsLocked(nowMs int64) {
	const lines = 360
	tmpls := []string{
		"level=info service=%s msg=\"request completed\" status=200 latency_ms=%d path=/api/items",
		"level=info service=%s msg=\"request completed\" status=304 latency_ms=%d path=/static/app.js",
		"level=debug service=%s msg=\"pool stats\" idle=%d busy=2",
		"level=info service=%s msg=\"job processed\" queue=default duration_ms=%d",
		"level=warn service=%s msg=\"slow query\" duration_ms=%d table=events",
		"level=info service=%s msg=\"cache hit ratio\" pct=%d",
		"level=error service=%s msg=\"upstream timeout\" retry_in_ms=%d",
	}
	seen := map[string]bool{}
	for _, c := range p.containers {
		seen[c.Id] = true
	}
	for id := range p.workloads {
		seen[id] = true
	}
	for _, id := range dockercli.SortedKeys(seen) {
		name := strings.TrimPrefix(strings.TrimPrefix(id, "ctr:"), "unit:")
		h := int(fhash(id))
		older := make([]*asterismv1.LogLine, 0, lines)
		start := nowMs - 40*60*1000
		for i := 0; i < lines; i++ {
			at := start + int64(i)*6_500
			if i%45 == 44 {
				at = older[len(older)-1].TsMs // two lines in the same millisecond
			}
			stream := "stdout"
			t := tmpls[(i+h)%len(tmpls)]
			if strings.HasPrefix(t, "level=error") || strings.HasPrefix(t, "level=warn") {
				stream = "stderr"
			}
			older = append(older, &asterismv1.LogLine{TsMs: at, WorkloadId: id, Stream: stream, Text: fmt.Sprintf(t, name, 1+(i*7+h)%400)})
		}
		p.logs[id] = append(older, p.logs[id]...)
	}
}
