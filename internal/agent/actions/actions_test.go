package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const restart = v1.ContainerAction_CONTAINER_ACTION_RESTART

func ctr(name, id string) *v1.Container {
	return &v1.Container{Id: "ctr:" + name, Name: name, ContainerId: id}
}

func TestDetectSelf(t *testing.T) {
	cases := map[string]string{
		"testdata/cgroup-v2-systemd":  "4f2a9c1e5b7d",
		"testdata/cgroup-v1-cgroupfs": "9b1d4e7a2c5f",
	}
	for file, short := range cases {
		s := DetectSelf(file, "irrelevant")
		if len(s.ContainerIDs) != 1 || !s.Matches(ctr("x", short)) {
			t.Errorf("%s: %+v", file, s)
		}
		if s.Matches(ctr("y", "000000000000")) {
			t.Errorf("%s: matched another container", file)
		}
	}
	host := DetectSelf("testdata/cgroup-host", "prod-1")
	if len(host.ContainerIDs) != 0 || host.Matches(ctr("x", "4f2a9c1e5b7d")) {
		t.Errorf("host cgroup: %+v", host)
	}
	// cgroup namespaces hide the id: fall back to Docker's default hostname.
	ns := DetectSelf("testdata/does-not-exist", "4f2a9c1e5b7d")
	if !ns.Matches(ctr("x", "4f2a9c1e5b7d")) {
		t.Error("hostname fallback")
	}
	if !(Self{}).Matches(&v1.Container{Name: "a", ContainerId: "abc", Labels: map[string]string{"asterism.self": "true"}}) {
		t.Error("asterism.self label")
	}
}

func TestGateMatrix(t *testing.T) {
	self := Self{ContainerIDs: []string{"5e1f00000000" + strings.Repeat("0", 52)}}
	on := config.Actions{Enabled: true, Allow: []string{"restart", "stop"}, Containers: []string{"shop-*", "agent"}, Protect: []string{"*postgres*"}}
	cases := []struct {
		name   string
		cfg    config.Actions
		action v1.ContainerAction
		c      *v1.Container
		deny   string
	}{
		{"allowed", on, restart, ctr("shop-api-1", "aaaaaaaaaaaa"), ""},
		{"disabled", config.Actions{Allow: []string{"restart"}, Containers: []string{"*"}}, restart, ctr("shop-api-1", "a"), "actions are disabled"},
		{"action not allowed", on, v1.ContainerAction_CONTAINER_ACTION_PAUSE, ctr("shop-api-1", "a"), `"pause" is not in actions.allow`},
		{"not in containers", on, restart, ctr("grafana", "a"), "does not match actions.containers"},
		{"protected glob", on, restart, ctr("shop-postgres-1", "a"), "matches actions.protect"},
		{"self", on, restart, ctr("agent", "5e1f00000000"), "own container"},
		{"hub-visible protection", on, restart, &v1.Container{Name: "shop-x", ContainerId: "b", Protected: true, ProtectedReason: "asterism.protected label"}, "is protected"},
		{"empty containers refuses all", config.Actions{Enabled: true, Allow: []string{"restart"}}, restart, ctr("shop-api-1", "a"), "does not match actions.containers"},
	}
	for _, c := range cases {
		g := NewGate(c.cfg, self)
		d := g.Check(c.action, c.c)
		switch {
		case c.deny == "" && d != nil:
			t.Errorf("%s: unexpected denial %q", c.name, d.Reason)
		case c.deny != "" && (d == nil || !strings.Contains(d.Reason, c.deny)):
			t.Errorf("%s: denial %v, want %q", c.name, d, c.deny)
		}
	}
	g := NewGate(on, self)
	if fmt.Sprint(g.Capabilities()) != "[action:stop action:restart]" {
		t.Errorf("capabilities %v", g.Capabilities())
	}
	if NewGate(config.Actions{Allow: []string{"restart"}}, self).Allowed() != nil {
		t.Error("disabled gate must advertise nothing")
	}
	c := ctr("grafana", "c")
	g.Annotate(c)
	if !c.Protected || c.ProtectedReason != ProtectedReason {
		t.Errorf("annotate outside allow-list: %+v", c)
	}
	ok := ctr("shop-api-1", "d")
	g.Annotate(ok)
	if ok.Protected {
		t.Error("annotate must not protect allowed containers")
	}
	// An annotated container is still allowed once config would allow it:
	// the gate's own marker is not a separate refusal.
	marked := ctr("shop-web-1", "e")
	marked.Protected, marked.ProtectedReason = true, ProtectedReason
	if d := g.Check(restart, marked); d != nil {
		t.Errorf("own marker should not deny: %v", d)
	}
}

type fakeExec struct {
	calls int
	err   error
}

func (f *fakeExec) ContainerAction(_ context.Context, id string, a v1.ContainerAction, timeout time.Duration) (*v1.Container, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &v1.Container{ContainerId: id, State: v1.ContainerState_CONTAINER_STATE_RUNNING}, nil
}

func TestServiceAudits(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	gate := NewGate(config.Actions{Enabled: true, Allow: []string{"restart"}, Containers: []string{"shop-*"}}, Self{})
	inv := map[string]*v1.Container{"ctr:shop-api-1": ctr("shop-api-1", "abcdefabcdef"), "ctr:db": ctr("db", "123412341234")}
	lookup := func(ref string) *v1.Container { return inv[ref] }
	exec := &fakeExec{}
	svc := NewService(gate, exec, lookup, logger)

	res := svc.Handle(context.Background(), &v1.CommandRequest{RequestId: "r1", Action: restart, ContainerId: "ctr:shop-api-1", Actor: "admin@example.com"})
	if !res.Ok || res.Container.GetContainerId() != "abcdefabcdef" || res.AuditId == "" || exec.calls != 1 {
		t.Fatalf("success: %+v calls=%d", res, exec.calls)
	}
	res = svc.Handle(context.Background(), &v1.CommandRequest{RequestId: "r2", Action: restart, ContainerId: "ctr:db"})
	if res.Ok || res.Code != "permission_denied" || exec.calls != 1 {
		t.Fatalf("denied: %+v", res)
	}
	res = svc.Handle(context.Background(), &v1.CommandRequest{RequestId: "r3", Action: restart, ContainerId: "ctr:gone"})
	if res.Code != "not_found" {
		t.Fatalf("not found: %+v", res)
	}
	res = svc.Handle(context.Background(), &v1.CommandRequest{RequestId: "r4", Action: 99, ContainerId: "ctr:shop-api-1"})
	if res.Code != "invalid_argument" {
		t.Fatalf("invalid: %+v", res)
	}
	exec.err = fmt.Errorf("engine: %w", ErrConflict)
	res = svc.Handle(context.Background(), &v1.CommandRequest{RequestId: "r5", Action: restart, ContainerId: "ctr:shop-api-1"})
	if res.Code != "failed_precondition" {
		t.Fatalf("conflict: %+v", res)
	}
	if res := NewService(gate, nil, lookup, logger).Handle(context.Background(), &v1.CommandRequest{RequestId: "r6", Action: restart, ContainerId: "ctr:shop-api-1"}); res.Code != "unavailable" {
		t.Fatalf("no executor: %+v", res)
	}

	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	want := []string{"success", "denied", "not_found", "invalid", "failure", "failure"}
	if len(lines) != len(want) {
		t.Fatalf("audit lines %d: %s", len(lines), buf.String())
	}
	for i, l := range lines {
		if l["msg"] != "audit" || l["result"] != want[i] || l["request_id"] != fmt.Sprintf("r%d", i+1) || l["audit_id"] == "" {
			t.Errorf("line %d: %v", i, l)
		}
	}
	if lines[0]["actor"] != "admin@example.com" || lines[0]["container"] != "shop-api-1" {
		t.Errorf("audit attrs: %v", lines[0])
	}
}
