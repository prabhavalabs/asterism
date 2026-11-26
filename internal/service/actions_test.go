package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

type fakeDocker struct {
	containers []*asterismv1.Container
	calls      []string
	failWith   error
}

func (f *fakeDocker) DockerSnapshot() dockercli.Snapshot {
	out := dockercli.Snapshot{Engine: &asterismv1.DockerEngine{Available: true}}
	for _, c := range f.containers {
		out.Containers = append(out.Containers, proto.Clone(c).(*asterismv1.Container))
	}
	return out
}

func (f *fakeDocker) SupportedContainerActions() []asterismv1.ContainerAction {
	return []asterismv1.ContainerAction{
		asterismv1.ContainerAction_CONTAINER_ACTION_START,
		asterismv1.ContainerAction_CONTAINER_ACTION_STOP,
		asterismv1.ContainerAction_CONTAINER_ACTION_RESTART,
		asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE,
		asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE,
	}
}

func (f *fakeDocker) ContainerAction(_ context.Context, a asterismv1.ContainerAction, id string, timeout time.Duration) (*asterismv1.Container, error) {
	f.calls = append(f.calls, ActionName(a)+":"+id+":"+timeout.String())
	if f.failWith != nil {
		return nil, f.failWith
	}
	for _, c := range f.containers {
		if c.ContainerId == id {
			switch a {
			case asterismv1.ContainerAction_CONTAINER_ACTION_STOP:
				c.State = asterismv1.ContainerState_CONTAINER_STATE_EXITED
			default:
				c.State = asterismv1.ContainerState_CONTAINER_STATE_RUNNING
			}
			return proto.Clone(c).(*asterismv1.Container), nil
		}
	}
	return nil, errors.New("no such container")
}

func newFixture(enabled bool) (*Observatory, *fakeDocker) {
	fd := &fakeDocker{containers: []*asterismv1.Container{
		{Id: "ctr:shop-api-1", Name: "shop-api-1", ContainerId: "3f2a9c1b7d4e", State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING, ImageRepo: "ghcr.io/acme/shop-api"},
		{Id: "ctr:shop-api-2", Name: "shop-api-2", ContainerId: "3f2a00000000", State: asterismv1.ContainerState_CONTAINER_STATE_EXITED, ImageRepo: "ghcr.io/acme/shop-api"},
		{Id: "ctr:asterism", Name: "asterism", ContainerId: "aaaabbbbcccc", State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING, ImageRepo: "ghcr.io/prabhavalabs/asterism"},
		{Id: "ctr:vault", Name: "vault", ContainerId: "ddddeeeeffff", State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING, Labels: map[string]string{"asterism.protected": "true"}},
	}}
	o := New(bus.New(8), ring.New(10), nil, nil, "showcase")
	o.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.ConfigureDocker(fd, fd, ActionPolicy{Enabled: enabled, DefaultTimeout: 10 * time.Second})
	return o, fd
}

var (
	adminWithActions = auth.Principal{Kind: "session", Subject: "admin@example.com", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeActions}}
	readOnlyToken    = auth.Principal{Kind: "token", Subject: "grafana", Scopes: []auth.Scope{auth.ScopeRead}}
)

func u32(v uint32) *uint32 { return &v }

func TestPerformContainerActionGates(t *testing.T) {
	restart := asterismv1.ContainerAction_CONTAINER_ACTION_RESTART
	tests := []struct {
		name       string
		enabled    bool
		principal  auth.Principal
		req        ActionRequest
		wantErr    error
		wantResult string
		wantCalls  int
	}{
		{"disabled", false, adminWithActions, ActionRequest{Action: restart, Ref: "ctr:shop-api-1"}, ErrFailedPrecondition, "denied", 0},
		{"missing scope", true, readOnlyToken, ActionRequest{Action: restart, Ref: "ctr:shop-api-1"}, ErrPermissionDenied, "denied", 0},
		{"injection attempt", true, adminWithActions, ActionRequest{Action: restart, Ref: "shop-api-1; reboot"}, ErrInvalidArgument, "invalid", 0},
		{"unknown", true, adminWithActions, ActionRequest{Action: restart, Ref: "ctr:nope"}, ErrNotFound, "not_found", 0},
		{"ambiguous prefix", true, adminWithActions, ActionRequest{Action: restart, Ref: "3f2a"}, ErrInvalidArgument, "invalid", 0},
		{"self protected", true, adminWithActions, ActionRequest{Action: restart, Ref: "asterism"}, ErrPermissionDenied, "denied", 0},
		{"label protected", true, adminWithActions, ActionRequest{Action: restart, Ref: "vault"}, ErrPermissionDenied, "denied", 0},
		{"timeout too large", true, adminWithActions, ActionRequest{Action: restart, Ref: "shop-api-1", TimeoutSeconds: u32(301)}, ErrInvalidArgument, "invalid", 0},
		{"pause stopped", true, adminWithActions, ActionRequest{Action: asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE, Ref: "shop-api-2"}, ErrFailedPrecondition, "invalid", 0},
		{"unspecified action", true, adminWithActions, ActionRequest{Ref: "shop-api-1"}, ErrInvalidArgument, "invalid", 0},
		{"ok by name", true, adminWithActions, ActionRequest{Action: restart, Ref: "shop-api-1", TimeoutSeconds: u32(3)}, nil, "success", 1},
		{"ok by id prefix", true, adminWithActions, ActionRequest{Action: asterismv1.ContainerAction_CONTAINER_ACTION_STOP, Ref: "3f2a9c"}, nil, "success", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, fd := newFixture(tc.enabled)
			resp, err := o.PerformContainerAction(context.Background(), tc.principal, tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(fd.calls) != tc.wantCalls {
				t.Fatalf("actuator calls = %v, want %d", fd.calls, tc.wantCalls)
			}
			evs := o.events.List("", 0)
			if len(evs) != 1 {
				t.Fatalf("expected exactly one audit event, got %d", len(evs))
			}
			a := evs[0].Audit
			if a == nil || a.Result != tc.wantResult || a.Actor != tc.principal.Actor() || !strings.HasPrefix(evs[0].Kind, "audit.container.") {
				t.Fatalf("audit = %+v kind %s", a, evs[0].Kind)
			}
			if tc.wantErr == nil {
				if resp == nil || resp.AuditEvent == nil || resp.Container == nil || a.StateAfter == "" || a.ContainerId == "" {
					t.Fatalf("response/audit incomplete: %+v %+v", resp, a)
				}
			}
		})
	}
}

func TestActionTimeoutDefaultAndOverride(t *testing.T) {
	o, fd := newFixture(true)
	stop := asterismv1.ContainerAction_CONTAINER_ACTION_STOP
	if _, err := o.PerformContainerAction(context.Background(), adminWithActions, ActionRequest{Action: stop, Ref: "shop-api-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PerformContainerAction(context.Background(), adminWithActions, ActionRequest{Action: asterismv1.ContainerAction_CONTAINER_ACTION_START, Ref: "shop-api-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PerformContainerAction(context.Background(), adminWithActions, ActionRequest{Action: stop, Ref: "shop-api-1", TimeoutSeconds: u32(0)}); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop:3f2a9c1b7d4e:10s", "start:3f2a9c1b7d4e:10s", "stop:3f2a9c1b7d4e:0s"}
	for i, w := range want {
		if fd.calls[i] != w {
			t.Fatalf("call %d = %s, want %s", i, fd.calls[i], w)
		}
	}
}

func TestActionFailureIsAuditedAndRedacted(t *testing.T) {
	o, fd := newFixture(true)
	fd.failWith = errors.New("ssh: Error response from daemon: token=abc123 bad")
	_, err := o.PerformContainerAction(context.Background(), adminWithActions, ActionRequest{Action: asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, Ref: "shop-api-1"})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "abc123") {
		t.Fatalf("err = %v", err)
	}
	ev := o.events.List("ctr:shop-api-1", 0)
	if len(ev) != 1 || ev[0].Audit.Result != "failure" || ev[0].Severity != "warning" {
		t.Fatalf("events = %+v", ev)
	}
}

func TestCapabilities(t *testing.T) {
	o, _ := newFixture(false)
	c := o.Capabilities(adminWithActions)
	if c.ActionsEnabled || c.CanPerformActions || !strings.Contains(c.ActionsDisabledReason, "allow_actions") || !c.DockerAvailable {
		t.Fatalf("disabled caps: %+v", c)
	}
	o, _ = newFixture(true)
	if c := o.Capabilities(readOnlyToken); !c.ActionsEnabled || c.CanPerformActions || !strings.Contains(c.ActionsDisabledReason, "scope") {
		t.Fatalf("read-only caps: %+v", c)
	}
	if c := o.Capabilities(adminWithActions); !c.CanPerformActions || len(c.ContainerActions) != 5 {
		t.Fatalf("full caps: %+v", c)
	}
	local := New(bus.New(1), ring.New(1), nil, nil, "local")
	if c := local.Capabilities(adminWithActions); c.CanPerformActions || c.DockerAvailable {
		t.Fatalf("local caps: %+v", c)
	}
	if resp, _ := local.ListContainers(&asterismv1.ListContainersRequest{}); resp.Engine.Available || resp.Engine.UnavailableReason == "" {
		t.Fatalf("local engine: %+v", resp.Engine)
	}
}

func TestListAndGetContainers(t *testing.T) {
	o, _ := newFixture(true)
	resp, _ := o.ListContainers(&asterismv1.ListContainersRequest{States: []asterismv1.ContainerState{asterismv1.ContainerState_CONTAINER_STATE_EXITED}})
	if len(resp.Containers) != 1 || resp.Containers[0].Name != "shop-api-2" || resp.Counts.Total != 4 {
		t.Fatalf("filtered list: %+v", resp)
	}
	resp, _ = o.ListContainers(&asterismv1.ListContainersRequest{Query: "SHOP"})
	if len(resp.Containers) != 2 {
		t.Fatalf("query list: %d", len(resp.Containers))
	}
	got, err := o.GetContainer("asterism")
	if err != nil || !got.Container.Protected || got.Container.ProtectedReason == "" {
		t.Fatalf("protected container: %+v %v", got, err)
	}
	if _, err := o.GetContainer("shop-api"); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("expected suggestion, got %v", err)
	}
}

func TestContainerInsights(t *testing.T) {
	c := &asterismv1.Container{
		State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING, Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE,
		Privileged: true, RestartCount: 6, NetworkMode: "host", Resources: &asterismv1.ContainerResources{},
		Mounts: []*asterismv1.ContainerMount{{DockerSocket: true}},
		Ports:  []*asterismv1.ContainerPort{{Published: true, HostIp: "0.0.0.0", HostPort: 5432, Exposure: asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC}},
	}
	codes := map[string]bool{}
	for _, in := range containerInsights(c) {
		codes[in.Code] = true
	}
	for _, want := range []string{"public_datastore", "restart_loop", "privileged", "docker_socket", "host_network", "no_memory_limit"} {
		if !codes[want] {
			t.Fatalf("missing insight %s in %v", want, codes)
		}
	}
	if in := containerInsights(c); in[0].Severity != "critical" {
		t.Fatalf("insights must sort by severity: %+v", in)
	}
}

func TestDiffContainers(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	run := asterismv1.ContainerState_CONTAINER_STATE_RUNNING
	exited := asterismv1.ContainerState_CONTAINER_STATE_EXITED
	healthy := asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY
	unhealthy := asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY
	prev := map[string]containerFingerprint{
		"ctr:a": {name: "a", state: run, health: healthy, startedAtMs: 1},
		"ctr:b": {name: "b", state: run, health: healthy, startedAtMs: 1},
		"ctr:c": {name: "c", state: run, startedAtMs: 1},
		"ctr:d": {name: "d", state: run, startedAtMs: 1},
	}
	cur := map[string]containerFingerprint{
		"ctr:a": {name: "a", state: exited, health: healthy, exitCode: 137, oomKilled: true, startedAtMs: 1},
		"ctr:b": {name: "b", state: run, health: unhealthy, startedAtMs: 1},
		"ctr:c": {name: "c", state: run, startedAtMs: 5, restartCount: 1},
		"ctr:e": {name: "e", state: run},
	}
	kinds := map[string]string{}
	for _, e := range diffContainers(prev, cur, now) {
		kinds[e.EntityId+"/"+e.Kind] = e.Severity
	}
	want := map[string]string{
		"ctr:a/container.oom":     "critical",
		"ctr:a/container.die":     "warning",
		"ctr:b/container.health":  "warning",
		"ctr:c/container.restart": "warning",
		"ctr:e/container.create":  "info",
		"ctr:d/container.destroy": "info",
	}
	for k, sev := range want {
		if kinds[k] != sev {
			t.Fatalf("missing %s (%s); got %v", k, sev, kinds)
		}
	}
	if len(kinds) != len(want) {
		t.Fatalf("unexpected extra events: %v", kinds)
	}
}

func TestListEventsMergesAuditNewestFirst(t *testing.T) {
	o, _ := newFixture(false)
	_, _ = o.PerformContainerAction(context.Background(), adminWithActions, ActionRequest{Action: asterismv1.ContainerAction_CONTAINER_ACTION_START, Ref: "shop-api-1"})
	evs := o.ListEvents("ctr:shop-api-1", 5)
	if len(evs) != 1 || evs[0].Audit == nil {
		t.Fatalf("events: %+v", evs)
	}
	ov := o.Overview(adminWithActions)
	if ov.ActiveAlertCount != 0 {
		t.Fatalf("audit warnings must not count as alerts: %d", ov.ActiveAlertCount)
	}
}
