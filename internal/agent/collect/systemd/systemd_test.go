package systemd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const root = "../../procfs/testdata/root"

func TestDiscoverListening(t *testing.T) {
	cfg := config.Default().Sources.Systemd
	reg := targets.New()
	reg.SetListeningPids([]int{777}) // billing.service owns a socket
	s := New(cfg, 30*time.Second, reg)
	caps, err := s.Init(context.Background(), state.Env{Root: root})
	if err != nil || caps[0] != "systemd" || s.HasSystemctl() {
		t.Fatalf("caps %v err %v (systemctl must not be used under a host root)", caps, err)
	}
	sink := collecttest.New()
	if err := s.Tick(time.Unix(1_800_000_000, 0), sink); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for id := range sink.Workloads {
		ids = append(ids, id)
	}
	// nginx (include), postgresql@16-main (include glob; no pids = inactive),
	// billing (listening); systemd-journald is excluded.
	want := map[string]string{"unit:nginx.service": "active", "unit:billing.service": "active", "unit:postgresql@16-main.service": "inactive"}
	if len(sink.Workloads) != len(want) {
		t.Fatalf("workloads %v", ids)
	}
	for id, st := range want {
		w := sink.Workloads[id]
		if w == nil || w.State != st || w.Kind != v1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT {
			t.Fatalf("%s: %v", id, w)
		}
	}
	if w := sink.Workloads["unit:nginx.service"]; w.Pid != 888 || w.Name != "nginx.service" || w.ServiceName != "nginx" || w.Health != v1.HealthStatus_HEALTH_STATUS_HEALTHY {
		t.Fatalf("nginx %v", w)
	}
	units := reg.Units(nil)
	if len(units) != 2 || units[0].Name != "billing.service" {
		t.Fatalf("registry units %v", units)
	}
}

func TestDiscoverModes(t *testing.T) {
	for mode, want := range map[string]int{"all": 3, "include-only": 2} {
		cfg := config.Default().Sources.Systemd
		cfg.Discover = mode
		s := New(cfg, time.Second, targets.New())
		if _, err := s.Init(context.Background(), state.Env{Root: root}); err != nil {
			t.Fatal(err)
		}
		sink := collecttest.New()
		_ = s.Tick(time.Unix(1, 0), sink)
		if len(sink.Workloads) != want {
			t.Errorf("%s: %d workloads", mode, len(sink.Workloads))
		}
	}
}

const showOut = `Id=nginx.service
LoadState=loaded
ActiveState=active
SubState=running
MainPID=888
Result=success
Description=A high performance web server

Id=redis-server.service
LoadState=loaded
ActiveState=failed
SubState=failed
MainPID=0
Result=exit-code
Description=Advanced key-value store

Id=mysql.service
LoadState=not-found
ActiveState=inactive
SubState=dead
MainPID=0
Result=success
Description=mysql.service
`

func TestSystemctlStatesAndFailedEvent(t *testing.T) {
	cfg := config.Default().Sources.Systemd
	cfg.Include = []string{"nginx.service", "redis-server.service", "mysql.service", "bad name;rm -rf.service"}
	s := New(cfg, time.Second, targets.New())
	if _, err := s.Init(context.Background(), state.Env{Root: root}); err != nil {
		t.Fatal(err)
	}
	s.systemctl = "/usr/bin/systemctl"
	var gotArgs []string
	failed := false
	s.Exec = func(_ context.Context, bin string, args ...string) ([]byte, error) {
		gotArgs = append([]string{bin}, args...)
		out := showOut
		if !failed {
			out = strings.Replace(out, "ActiveState=failed\nSubState=failed", "ActiveState=active\nSubState=running", 1)
		}
		return []byte(out), nil
	}
	sink := collecttest.New()
	_ = s.Tick(time.Unix(1, 0), sink)
	s.RefreshNow(context.Background())
	_ = s.Tick(time.Unix(2, 0), sink)
	if w := sink.Workloads["unit:redis-server.service"]; w == nil || w.State != "active" {
		t.Fatalf("redis via systemctl: %v", w)
	}
	if _, ok := sink.Workloads["unit:mysql.service"]; ok {
		t.Fatal("not-found unit shown")
	}
	for _, a := range gotArgs {
		if strings.Contains(a, ";") {
			t.Fatalf("unvalidated unit name passed to systemctl: %q", gotArgs)
		}
	}
	if gotArgs[0] != "/usr/bin/systemctl" || gotArgs[1] != "show" {
		t.Fatalf("argv %q", gotArgs)
	}
	failed = true
	s.RefreshNow(context.Background())
	_ = s.Tick(time.Unix(3, 0), sink)
	w := sink.Workloads["unit:redis-server.service"]
	if w.State != "failed" || w.Health != v1.HealthStatus_HEALTH_STATUS_DOWN {
		t.Fatalf("failed state %v", w)
	}
	if len(sink.Events) != 1 || sink.Events[0].Kind != "unit.failed" || sink.Events[0].Attrs["result"] != "exit-code" {
		t.Fatalf("events %v", sink.Events)
	}
	_ = s.Tick(time.Unix(4, 0), sink)
	if len(sink.Events) != 1 {
		t.Fatal("unit.failed emitted twice")
	}
}

func TestParseShow(t *testing.T) {
	m := ParseShow([]byte(showOut))
	if len(m) != 3 || m["nginx.service"].MainPID != 888 || m["redis-server.service"].Result != "exit-code" {
		t.Fatalf("%+v", m)
	}
}

func TestNotDetected(t *testing.T) {
	if _, err := New(config.Default().Sources.Systemd, time.Second, nil).Init(context.Background(), state.Env{Root: t.TempDir()}); err == nil {
		t.Fatal("systemd detected in an empty root")
	}
}
