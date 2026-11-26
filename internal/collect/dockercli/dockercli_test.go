package dockercli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

var fixtureNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func byName(cs []*asterismv1.Container) map[string]*asterismv1.Container {
	m := make(map[string]*asterismv1.Container, len(cs))
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func TestParseInspect(t *testing.T) {
	cs := ParseInspect(fixture(t, "inspect.jsonl"), fixtureNow)
	if len(cs) != 4 {
		t.Fatalf("expected 4 containers, got %d", len(cs))
	}
	m := byName(cs)

	tests := []struct {
		name  string
		check func(t *testing.T, c *asterismv1.Container)
	}{
		{"shop-api-1", func(t *testing.T, c *asterismv1.Container) {
			if c.Id != "ctr:shop-api-1" || c.ContainerId != "3f2a9c1b7d4e" || c.ImageId != "sha256:ab12cd34ef56" {
				t.Fatalf("ids: %s %s %s", c.Id, c.ContainerId, c.ImageId)
			}
			if c.ImageRepo != "ghcr.io/acme/shop-api" || c.ImageTag != "2.4.1" {
				t.Fatalf("image split: %s %s", c.ImageRepo, c.ImageTag)
			}
			if c.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING || c.Health != asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY {
				t.Fatalf("state/health: %v %v", c.State, c.Health)
			}
			if c.Status != "Up 3 hours (healthy)" || c.UptimeSeconds != 3*3600 {
				t.Fatalf("status %q uptime %d", c.Status, c.UptimeSeconds)
			}
			if c.ProjectId != "proj:shop" || c.ComposeService != "api" || c.ComposeWorkingDir != "/srv/shop" {
				t.Fatalf("compose: %s %s %s", c.ProjectId, c.ComposeService, c.ComposeWorkingDir)
			}
			if c.RestartPolicy != "unless-stopped" || c.Resources.MemLimitBytes != 1<<30 || c.Resources.CpuLimitCores != 1.5 {
				t.Fatalf("policy/resources: %s %+v", c.RestartPolicy, c.Resources)
			}
			if len(c.Ports) != 2 {
				t.Fatalf("expected 2 ports, got %+v", c.Ports)
			}
			if p := c.Ports[0]; !p.Published || p.HostIp != "127.0.0.1" || p.HostPort != 18080 || p.ContainerPort != 8080 || p.Exposure != asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK {
				t.Fatalf("published port: %+v", p)
			}
			if p := c.Ports[1]; p.Published || p.ContainerPort != 9090 || p.Exposure != asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL {
				t.Fatalf("exposed port: %+v", p)
			}
			if len(c.Networks) != 2 || c.Networks[1].Name != "shop_default" || c.Networks[1].IpAddress != "172.20.0.5" {
				t.Fatalf("networks: %+v", c.Networks)
			}
			if len(c.Mounts) != 2 || c.Mounts[1].Name != "shop_uploads" || !c.Mounts[1].ReadWrite || c.Mounts[0].ReadWrite {
				t.Fatalf("mounts: %+v", c.Mounts)
			}
			if !slices.Equal(c.EnvKeys, []string{"DATABASE_URL", "PATH", "PORT"}) {
				t.Fatalf("env keys: %v", c.EnvKeys)
			}
			if !slices.Equal(c.Command, []string{"serve", "--db-password", "[REDACTED]", "--listen=:8080", "--api-key=[REDACTED]"}) {
				t.Fatalf("command not redacted: %v", c.Command)
			}
			if _, ok := c.Labels["traefik.http.middlewares.auth.basicauth.users"]; ok {
				t.Fatal("secret-looking label leaked")
			}
			if _, ok := c.Labels["app.dsn"]; ok {
				t.Fatal("dsn label leaked")
			}
			if c.RedactedLabelCount != 2 {
				t.Fatalf("redacted label count = %d", c.RedactedLabelCount)
			}
			if v := c.Labels["app.upstream"]; strings.Contains(v, "s3cret") {
				t.Fatalf("url credential leaked: %s", v)
			}
			if c.Healthcheck == nil || c.Healthcheck.IntervalSeconds != 30 || strings.Contains(c.Healthcheck.LastOutput, "abc123") {
				t.Fatalf("healthcheck: %+v", c.Healthcheck)
			}
		}},
		{"shop-worker-1", func(t *testing.T, c *asterismv1.Container) {
			if c.State != asterismv1.ContainerState_CONTAINER_STATE_RESTARTING || c.Health != asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY {
				t.Fatalf("state/health: %v %v", c.State, c.Health)
			}
			if c.RestartCount != 7 || c.StatusLevel != asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED {
				t.Fatalf("restart count %d level %v", c.RestartCount, c.StatusLevel)
			}
			if c.Status != "Restarting (1) 10 seconds ago" {
				t.Fatalf("status %q", c.Status)
			}
			if c.Resources.CpuLimitCores != 0.5 || c.Resources.PidsLimit != 256 || c.Resources.CpusetCpus != "0,1" {
				t.Fatalf("resources: %+v", c.Resources)
			}
			if c.Healthcheck.FailingStreak != 4 || strings.Contains(c.Healthcheck.LastOutput, "pw1") {
				t.Fatalf("healthcheck: %+v", c.Healthcheck)
			}
		}},
		{"backup-cron", func(t *testing.T, c *asterismv1.Container) {
			if c.State != asterismv1.ContainerState_CONTAINER_STATE_EXITED || c.ExitCode != 137 || !c.OomKilled {
				t.Fatalf("exit: %v %d %v", c.State, c.ExitCode, c.OomKilled)
			}
			if c.Status != "Exited (137) About an hour ago" || c.UptimeSeconds != 0 {
				t.Fatalf("status %q uptime %d", c.Status, c.UptimeSeconds)
			}
			if c.ProjectId != "proj:standalone" || c.RestartPolicy != "no" || c.ImageTag != "latest" {
				t.Fatalf("project/policy/tag: %s %s %s", c.ProjectId, c.RestartPolicy, c.ImageTag)
			}
			if !c.Privileged || c.NetworkMode != "host" || len(c.Mounts) != 1 || !c.Mounts[0].DockerSocket {
				t.Fatalf("privileged/socket: %v %s %+v", c.Privileged, c.NetworkMode, c.Mounts)
			}
			// Stopped containers report configured bindings.
			if len(c.Ports) != 1 || c.Ports[0].HostPort != 5432 || c.Ports[0].HostIp != "0.0.0.0" || c.Ports[0].Exposure != asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC {
				t.Fatalf("ports: %+v", c.Ports)
			}
			if c.Health != asterismv1.ContainerHealth_CONTAINER_HEALTH_NONE || c.Healthcheck != nil {
				t.Fatalf("expected no healthcheck: %v %+v", c.Health, c.Healthcheck)
			}
			if strings.Contains(strings.Join(c.Entrypoint, " "), "RESTIC_PASSWORD=abc") {
				t.Fatalf("entrypoint secret leaked: %v", c.Entrypoint)
			}
		}},
		{"paused-thing", func(t *testing.T, c *asterismv1.Container) {
			if c.State != asterismv1.ContainerState_CONTAINER_STATE_PAUSED || c.Status != "Up About an hour (Paused)" {
				t.Fatalf("state %v status %q", c.State, c.Status)
			}
			if len(c.Ports) != 1 || c.Ports[0].HostPort != 8081 {
				t.Fatalf("expected IPv6 twin collapsed: %+v", c.Ports)
			}
			if c.Health != asterismv1.ContainerHealth_CONTAINER_HEALTH_NONE || c.Healthcheck != nil {
				t.Fatalf("NONE healthcheck should be absent: %+v", c.Healthcheck)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := m[tc.name]
			if !ok {
				t.Fatalf("container %s missing", tc.name)
			}
			tc.check(t, c)
		})
	}

	counts := Counts(cs)
	if counts.Total != 4 || counts.Running != 1 || counts.Paused != 1 || counts.Restarting != 1 || counts.Exited != 1 || counts.Unhealthy != 1 {
		t.Fatalf("counts: %+v", counts)
	}
}

func TestParseInspectArray(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(fixture(t, "inspect.jsonl")), "\n")
	arr := "[" + lines[1] + "," + lines[2] + "]"
	if got := ParseInspect(arr, fixtureNow); len(got) != 2 {
		t.Fatalf("expected 2 containers from array, got %d", len(got))
	}
}

func TestParseResources(t *testing.T) {
	eng := ParseInfo(fixture(t, "info.jsonl"), fixtureNow)
	if !eng.Available || eng.ServerVersion != "28.5.1" || eng.CgroupVersion != "2" || eng.Ncpu != 4 || eng.ContainersPaused != 1 {
		t.Fatalf("engine: %+v", eng)
	}
	if e := ParseInfo("", fixtureNow); e.Available || e.UnavailableReason == "" {
		t.Fatalf("empty info should be unavailable: %+v", e)
	}

	imgs := ParseImages(fixture(t, "images.jsonl"))
	if len(imgs) != 3 {
		t.Fatalf("expected 3 deduped images, got %d", len(imgs))
	}
	if imgs[0].Repository != "ghcr.io/acme/shop-api" || imgs[0].Tag != "2.4.1" || len(imgs[0].RepoTags) != 2 || imgs[0].SizeBytes != 187654321 {
		t.Fatalf("first image: %+v", imgs[0])
	}
	if !imgs[2].Dangling || imgs[2].Repository != "<none>" {
		t.Fatalf("dangling image should sort last: %+v", imgs[2])
	}

	vols := ParseVolumes(fixture(t, "volumes.jsonl"))
	if len(vols) != 2 || vols[0].Name != "shop_uploads" || vols[0].ComposeProject != "shop" || vols[0].SizeBytes != -1 || !vols[1].Anonymous {
		t.Fatalf("volumes: %+v", vols)
	}

	nets := ParseNetworks(fixture(t, "networks.jsonl"))
	if len(nets) != 3 || nets[0].Name != "shop_default" || nets[0].Builtin || len(nets[0].Containers) != 1 || nets[0].Containers[0].Name != "shop-api-1" {
		t.Fatalf("networks: %+v", nets)
	}
	if nets[0].Subnets[0] != "172.20.0.0/16" || !nets[1].Builtin {
		t.Fatalf("network details: %+v", nets)
	}

	cs := ParseInspect(fixture(t, "inspect.jsonl"), fixtureNow)
	LinkUsage(cs, imgs, vols)
	if imgs[0].Containers != 2 || imgs[2].Containers != 0 {
		t.Fatalf("image usage: %d %d", imgs[0].Containers, imgs[2].Containers)
	}
	if vols[0].Containers != 1 || vols[0].UsedBy[0] != "shop-api-1" || vols[1].Containers != 0 {
		t.Fatalf("volume usage: %+v", vols)
	}
}

func TestParseDiskUsage(t *testing.T) {
	du := ParseDiskUsage(fixture(t, "df.jsonl"), fixtureNow)
	if len(du.Categories) != 4 {
		t.Fatalf("expected 4 categories, got %d", len(du.Categories))
	}
	want := map[string][2]uint64{
		"images":      {352_600_000, 120_000_000},
		"containers":  {12_500, 0},
		"volumes":     {3_000_000_000, 2_100_000_000},
		"build_cache": {1_500_000_000, 1_500_000_000},
	}
	for _, c := range du.Categories {
		w, ok := want[c.Kind]
		if !ok || c.SizeBytes != w[0] || c.ReclaimableBytes != w[1] {
			t.Fatalf("category %s: size %d recl %d", c.Kind, c.SizeBytes, c.ReclaimableBytes)
		}
	}
	sizes := ParseVolumeSizes(fixture(t, "dfv.jsonl"))
	if sizes["shop_uploads"] != 900_000_000 {
		t.Fatalf("volume sizes: %v", sizes)
	}
	if ParseVolumeSizes("not json") != nil {
		t.Fatal("expected nil sizes for bad input")
	}
}

func TestParseStatsAndCounters(t *testing.T) {
	id1 := "3f2a9c1b7d4e" + strings.Repeat("0", 52)
	stats := ParseStats(fixture(t, "stats.txt"))
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats rows, got %d", len(stats))
	}
	s := stats[id1]
	if s.CPUUtil != 0.125 || s.MemUsed != uint64(342.5*(1<<20)) || s.MemLimit != 1<<30 || s.NetRx != 1_200_000 || s.BlkRd != 10_500_000 || s.PIDs != 23 {
		t.Fatalf("stats: %+v", s)
	}

	io := ParseCgroupIO(fixture(t, "cgroup_io.txt"))
	if io[id1] != (IOCounter{Read: 1500, Write: 2500}) {
		t.Fatalf("cgroup io: %+v", io)
	}
	if len(io) != 2 {
		t.Fatalf("expected empty entry for second cgroup, got %+v", io)
	}

	nd := ParseNetDevByPid(fixture(t, "netdev.txt"))
	if nd[4120] != (IOCounter{Read: 5_001_000, Write: 7_002_000}) || nd[777].Read != 10 {
		t.Fatalf("netdev: %+v", nd)
	}

	rt := NewRateTracker()
	t0 := fixtureNow
	if r := rt.Observe("a", t0, 100, 200, 0, 0); r != (Rates{}) {
		t.Fatalf("first observation should be zero: %+v", r)
	}
	r := rt.Observe("a", t0.Add(2*time.Second), 300, 600, 1000, 0)
	if r.NetRx != 100 || r.NetTx != 200 || r.BlkRd != 500 {
		t.Fatalf("rates: %+v", r)
	}
	if r := rt.Observe("a", t0.Add(4*time.Second), 10, 10, 10, 0); r != (Rates{}) {
		t.Fatalf("counter reset should yield zero: %+v", r)
	}
}

func TestRedaction(t *testing.T) {
	tests := []struct {
		in   []string
		want []string
	}{
		{[]string{"--password", "x", "--user", "bob"}, []string{"--password", "[REDACTED]", "--user", "bob"}},
		{[]string{"DB_PASS=hunter2"}, []string{"DB_PASS=[REDACTED]"}},
		{[]string{"--tls-passthrough"}, []string{"--tls-passthrough"}},
		{[]string{"valkey-server", "--requirepass", "pw"}, []string{"valkey-server", "--requirepass", "[REDACTED]"}},
		{[]string{"redis://default:pw@cache:6379"}, []string{"redis://default:[REDACTED]@cache:6379"}},
		{[]string{"--author", "me"}, []string{"--author", "me"}},
		{nil, nil},
	}
	for _, tc := range tests {
		got := RedactArgs(tc.in)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("RedactArgs(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	labels, dropped := FilterLabels(map[string]string{
		"org.opencontainers.image.authors":      "Acme",
		"com.example.api_key":                   "k",
		"traefik.tcp.routers.x.tls.passthrough": "true",
	})
	if dropped != 1 || labels["org.opencontainers.image.authors"] != "Acme" || labels["traefik.tcp.routers.x.tls.passthrough"] != "true" {
		t.Fatalf("labels %v dropped %d", labels, dropped)
	}
}

func TestCommandBuilders(t *testing.T) {
	good := "3f2a9c1b7d4e"
	cmd, err := InspectCommand(good)
	if err != nil || !strings.HasSuffix(cmd, "'"+good+"'") {
		t.Fatalf("InspectCommand: %q %v", cmd, err)
	}
	for _, bad := range []string{"", "abc", "3f2a9c1b7d4e; rm -rf /", "$(reboot)", "3F2A9C1B7D4E", "shop-api-1"} {
		if _, err := InspectCommand(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
	if s := NetDevScript([]int32{12, -1, 0, 34}); s != `for p in 12 34; do echo "#$p"; cat /proc/$p/net/dev 2>/dev/null; done` {
		t.Fatalf("NetDevScript: %q", s)
	}
	if NetDevScript([]int32{0}) != "" {
		t.Fatal("expected empty script for no pids")
	}
	if ShellQuote("a'b") != `'a'"'"'b'` {
		t.Fatalf("ShellQuote: %s", ShellQuote("a'b"))
	}
	if HumanDuration(90*time.Second) != "About a minute" || HumanDuration(5*24*time.Hour) != "5 days" {
		t.Fatal("HumanDuration")
	}
}

// TestParseInspectRedactsShellStrings covers the SSH bridge (docker CLI)
// path: a secret flag inside a shell string such as
// ["sh", "-c", "redis-server --requirepass x"] is redacted, as it is on the
// agent's Engine API path.
func TestParseInspectRedactsShellStrings(t *testing.T) {
	doc := `{"Id":"5e5e5e5e5e5e0000000000000000000000000000000000000000000000000000","Name":"/cache-1",` +
		`"State":{"Status":"running","Running":true,"StartedAt":"2026-11-24T09:00:00Z"},"HostConfig":{},` +
		`"Config":{"Image":"redis:7-alpine","Cmd":["sh","-c","redis-server --requirepass s3cr3t-value --port 6379"],` +
		`"Entrypoint":["/bin/sh","-c","exec app --token s3cr3t-value"],` +
		`"Healthcheck":{"Test":["CMD-SHELL","redis-cli --password s3cr3t-value ping"]}}}`
	cs := ParseInspect(doc, fixtureNow)
	if len(cs) != 1 {
		t.Fatalf("containers %d", len(cs))
	}
	c := cs[0]
	all := strings.Join(append(append(append([]string{}, c.Command...), c.Entrypoint...), c.GetHealthcheck().GetTest()...), " ")
	if strings.Contains(all, "s3cr3t-value") {
		t.Fatalf("shell-string secret leaked: %q", all)
	}
	if c.Command[2] != "redis-server --requirepass [REDACTED] --port 6379" {
		t.Fatalf("command %q", c.Command)
	}
}
