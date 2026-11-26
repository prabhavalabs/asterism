package dockercli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const fixtureSecret = "s3cr3t-value"

func engineFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "engine", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertNoSecret(t *testing.T, what string, m proto.Message) {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(fixtureSecret)) {
		t.Fatalf("%s: encoded message contains the fixture secret", what)
	}
}

func TestContainerFromEngine(t *testing.T) {
	ec, err := ContainerFromEngine(engineFixture(t, "inspect-shop-api-1.json"), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	c := ec.Container
	if ec.FullID != "317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25" || ec.Tty {
		t.Fatalf("full id %q tty %v", ec.FullID, ec.Tty)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"id", c.Id == "ctr:shop-api-1" && c.ContainerId == "317e4d06ef07"},
		{"image", c.Image == "alpine:3.20" && c.ImageRepo == "alpine" && c.ImageTag == "3.20" && c.ImageId == "sha256:d9e853e87e55"},
		{"state", c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING && c.Health == asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY},
		{"compose", c.ProjectId == "proj:shop" && c.ComposeService == "api" && c.ComposeWorkingDir == "/srv/shop"},
		{"env keys only", slices.Equal(c.EnvKeys, []string{"DB_PASSWORD", "PATH", "PORT"})},
		{"cmd redacted", slices.Equal(c.Command, []string{"node", "server.js", "--db-password=[REDACTED]"})},
		{"secret labels dropped", c.RedactedLabelCount == 2 && c.Labels["com.example.api_token"] == "" && c.Labels["com.example.dsn"] == ""},
		{"url creds redacted", c.Labels["com.example.upstream"] == "https://user:[REDACTED]@example.com/x"},
		{"traefik label kept", c.Labels["traefik.http.routers.api.rule"] == "Host(`api.example.com`)"},
		{"health output redacted", c.Healthcheck != nil && !strings.Contains(c.Healthcheck.LastOutput, fixtureSecret)},
		{"ports", len(c.Ports) == 1 && c.Ports[0].HostIp == "127.0.0.1" && c.Ports[0].HostPort == 18080 && c.Ports[0].ContainerPort == 8080 && c.Ports[0].Exposure == asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK},
		{"mounts", len(c.Mounts) == 2 && c.Mounts[0].Destination == "/data" && c.Mounts[1].DockerSocket},
		{"networks", len(c.Networks) == 1 && c.Networks[0].Name == "shop_default" && c.Networks[0].IpAddress == "172.20.0.3"},
		{"resources", c.Resources.MemLimitBytes == 512<<20 && c.Resources.CpuLimitCores == 1.5},
		{"restart", c.RestartPolicy == "unless-stopped" && c.Pid == 4242},
	}
	for _, ch := range checks {
		if !ch.ok {
			t.Errorf("%s: %v", ch.name, c)
		}
	}
	assertNoSecret(t, "shop-api-1", c)

	ec, err = ContainerFromEngine(engineFixture(t, "inspect-cache-redis.json"), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	r := ec.Container
	if r.State != asterismv1.ContainerState_CONTAINER_STATE_EXITED || !r.OomKilled || r.ExitCode != 137 || r.ProjectId != "proj:standalone" {
		t.Fatalf("redis state: %v", r)
	}
	if !slices.Equal(r.Command, []string{"redis-server --requirepass [REDACTED]"}) {
		t.Fatalf("shell-string command not redacted: %q", r.Command)
	}
	assertNoSecret(t, "cache-redis", r)
	if !slices.Equal(r.EnvKeys, []string{"PATH", "REDIS_PASSWORD"}) {
		t.Fatalf("env keys %v", r.EnvKeys)
	}
	if len(r.Ports) != 1 || r.Ports[0].Exposure != asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL {
		t.Fatalf("exposed-only port: %v", r.Ports)
	}

	if _, err := ContainerFromEngine([]byte(`{"Id":"nope"}`), fixtureNow); err != ErrNotContainer {
		t.Fatalf("want ErrNotContainer, got %v", err)
	}
	if _, err := ContainerFromEngine([]byte(`{`), fixtureNow); err == nil {
		t.Fatal("bad json accepted")
	}
}

func TestRedactShellArgs(t *testing.T) {
	got := RedactShellArgs([]string{"sh", "-c", "exec app --token  abc --name x", "--password", "p"})
	want := []string{"sh", "-c", "exec app --token  [REDACTED] --name x", "--password", "[REDACTED]"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if got := RedactShellArgs([]string{"echo hello  world"}); got[0] != "echo hello  world" {
		t.Fatalf("unchanged shell strings must keep their spacing: %q", got)
	}
}

func TestEnvKeysScanner(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`null`, nil},
		{`[]`, nil},
		{`["A=1","B=two=2","A=dup","NOVALUE"," SPACED =x"]`, []string{"A", "B", "NOVALUE", "SPACED"}},
		{`["ESC\"APED=1","OK=\"quoted\" value \\ here"]`, []string{"OK"}},
		{`["K=v,with,commas","L=]bracket["]`, []string{"K", "L"}},
	}
	for _, c := range cases {
		var e envKeys
		if err := e.UnmarshalJSON([]byte(c.in)); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if !slices.Equal([]string(e), c.want) {
			t.Errorf("%s: %q want %q", c.in, e, c.want)
		}
	}
	for _, bad := range []string{`{}`, `["unterminated]`, `[1]`} {
		var e envKeys
		if err := e.UnmarshalJSON([]byte(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestParseEngineContainerList(t *testing.T) {
	list, err := ParseEngineContainerList(engineFixture(t, "containers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != "buildx_buildkit_default" || list[1].Name != "cache-redis" || list[2].Name != "shop-api-1" {
		t.Fatalf("%+v", list)
	}
	if list[1].State != asterismv1.ContainerState_CONTAINER_STATE_EXITED || list[2].State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
		t.Fatalf("states %v %v", list[1].State, list[2].State)
	}
}

func TestParseEngineEvents(t *testing.T) {
	sc := bufio.NewScanner(bytes.NewReader(engineFixture(t, "events.ndjson")))
	var got []string
	bad := 0
	for sc.Scan() {
		e, err := ParseEngineEvent(sc.Bytes())
		if err != nil {
			bad++
			continue
		}
		got = append(got, e.BaseAction()+":"+e.Name)
		if e.Time.IsZero() || e.ID == "" {
			t.Fatalf("event missing fields: %+v", e)
		}
		if e.BaseAction() == "die" && e.ExitCode != "137" {
			t.Fatalf("die exit code %q", e.ExitCode)
		}
	}
	want := []string{"kill:shop-api-1", "exec_create:shop-api-1", "die:shop-api-1", "start:shop-api-1", "health_status:shop-api-1", "oom:cache-redis", "destroy:buildx_buildkit_default"}
	if !slices.Equal(got, want) || bad != 1 {
		t.Fatalf("got %v (bad=%d)", got, bad)
	}
}

func TestEngineResources(t *testing.T) {
	eng, err := EngineInfoFromJSON(engineFixture(t, "info.json"), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if !eng.Available || eng.ServerVersion != "29.9.3" || eng.CgroupDriver != "systemd" || eng.Ncpu != 10 || eng.ContainersRunning != 2 {
		t.Fatalf("%v", eng)
	}
	assertNoSecret(t, "engine", eng)

	imgs, err := ImagesFromEngine(engineFixture(t, "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 3 || imgs[0].Repository != "ghcr.io/acme/shop-api" || imgs[0].Tag != "2.4.1" || !imgs[2].Dangling || imgs[0].CreatedAtMs != 1795366892000 {
		t.Fatalf("%v", imgs)
	}

	vols, err := VolumesFromEngine(engineFixture(t, "volumes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 || vols[0].Name != "shop_data" || vols[0].ComposeProject != "shop" || !vols[1].Anonymous || vols[0].SizeBytes != -1 {
		t.Fatalf("%v", vols)
	}
	for _, v := range vols {
		assertNoSecret(t, "volume", v)
	}

	nets, err := NetworksFromEngine(engineFixture(t, "networks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 4 || nets[0].Name != "shop_default" || nets[0].Subnets[0] != "172.20.0.0/16" || !nets[1].Builtin {
		t.Fatalf("%v", nets)
	}
	ec, _ := ContainerFromEngine(engineFixture(t, "inspect-shop-api-1.json"), fixtureNow)
	LinkNetworks([]*asterismv1.Container{ec.Container}, nets)
	if len(nets[0].Containers) != 1 || nets[0].Containers[0].Name != "shop-api-1" || nets[0].Containers[0].Ipv4Address != "172.20.0.3" {
		t.Fatalf("attachments %v", nets[0].Containers)
	}
	for _, n := range nets {
		assertNoSecret(t, "network", n)
	}

	du, sizes, err := DiskUsageFromEngine(engineFixture(t, "system-df.json"), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	cats := map[string]*asterismv1.DiskUsageCategory{}
	for _, c := range du.Categories {
		cats[c.Kind] = c
	}
	if c := cats["images"]; c.TotalCount != 3 || c.ActiveCount != 2 || c.SizeBytes != 250000000 || c.ReclaimableBytes != 250000000-(118000000+40000000) {
		t.Fatalf("images %v", c)
	}
	if c := cats["containers"]; c.ActiveCount != 1 || c.SizeBytes != 4096+1048576 || c.ReclaimableBytes != 1048576 {
		t.Fatalf("containers %v", c)
	}
	if c := cats["volumes"]; c.ActiveCount != 1 || c.SizeBytes != 402935167+1000 || c.ReclaimableBytes != 1000 {
		t.Fatalf("volumes %v", c)
	}
	if c := cats["build_cache"]; c.ActiveCount != 1 || c.SizeBytes != 144134144+1000 || c.ReclaimableBytes != 144134144 {
		t.Fatalf("build cache %v", c)
	}
	if sizes["shop_data"] != 402935167 || len(sizes) != 2 {
		t.Fatalf("sizes %v", sizes)
	}
	ApplyVolumeSizes(vols, sizes)
	if vols[0].SizeBytes != 402935167 {
		t.Fatal("ApplyVolumeSizes")
	}
}
