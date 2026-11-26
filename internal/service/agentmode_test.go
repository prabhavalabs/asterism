package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

func TestOverviewCountsAlertsOverWholeEventLog(t *testing.T) {
	inv := &listInventory{}
	// 20 old warning alerts, then 30 newer info/lifecycle/audit events: the
	// old 12-event window saw none of the alerts.
	for i := range 20 {
		inv.events = append(inv.events, &asterismv1.Event{Id: fmt.Sprintf("alert-%d", i), TsMs: int64(100 + i), Kind: "alert.memory", Severity: []string{"warning", "critical", "error"}[i%3]})
	}
	for i := range 30 {
		kind := []string{"container.die", "audit.container.restart", "agent.sync"}[i%3]
		sev := "warning"
		if kind == "agent.sync" {
			sev = "info"
		}
		inv.events = append(inv.events, &asterismv1.Event{Id: fmt.Sprintf("new-%d", i), TsMs: int64(1000 + i), Kind: kind, Severity: sev})
	}
	o := New(bus.New(4), ring.New(4), nil, inv, "agent")
	// Service-generated lifecycle and audit events are not alerts either.
	o.events.Append(&asterismv1.Event{Id: "svc-1", TsMs: 5000, Kind: "container.oom", Severity: "critical"})
	o.events.Append(&asterismv1.Event{Id: "svc-2", TsMs: 5001, Kind: "audit.container.stop", Severity: "warning"})
	o.events.Append(&asterismv1.Event{Id: "svc-3", TsMs: 5002, Kind: "agent.offline", Severity: "warning"})

	ov := o.Overview(adminWithActions)
	if ov.ActiveAlertCount != 21 {
		t.Fatalf("active alerts = %d, want 21 (20 collector alerts + agent.offline)", ov.ActiveAlertCount)
	}
	if len(ov.RecentEvents) != 12 {
		t.Fatalf("recent events = %d", len(ov.RecentEvents))
	}
	// Same rule as the events page "alerts" filter.
	page, err := o.ListEventsPage(&asterismv1.ListEventsRequest{KindGroup: "alerts", Severities: []string{"warning", "critical"}})
	if err != nil || page.PageInfo.TotalSize != int32(ov.ActiveAlertCount) {
		t.Fatalf("alerts page total %v, err %v", page.GetPageInfo(), err)
	}
}

func TestListNetworksUserDefinedFilter(t *testing.T) {
	o := New(bus.New(4), ring.New(4), nil, nil, "agent")
	o.ConfigureDocker(&snapDocker{snap: dockercli.Snapshot{
		Engine: &asterismv1.DockerEngine{Available: true},
		Networks: []*asterismv1.DockerNetwork{
			{Id: "n1", Name: "bridge", Driver: "bridge", Builtin: true},
			{Id: "n2", Name: "host", Driver: "host", Builtin: true},
			{Id: "n3", Name: "none", Driver: "null", Builtin: true},
			{Id: "n4", Name: "shop_default", Driver: "bridge"},
			{Id: "n5", Name: "platform", Driver: "bridge"},
		},
	}}, nil, ActionPolicy{})

	all, err := o.ListNetworks(&asterismv1.ListNetworksRequest{})
	if err != nil || len(all.Networks) != 5 {
		t.Fatalf("all = %v %v", all, err)
	}
	user, _ := o.ListNetworks(&asterismv1.ListNetworksRequest{UserDefinedOnly: true})
	var names []string
	for _, n := range user.Networks {
		names = append(names, n.Name)
	}
	if strings.Join(names, ",") != "platform,shop_default" || user.PageInfo.TotalSize != 2 {
		t.Fatalf("user-defined = %v", names)
	}
	// Counts ignore the toggle so both labels can show totals; they respect the query.
	for _, resp := range []*asterismv1.ListNetworksResponse{all, user} {
		if c := resp.Counts; c.Total != 5 || c.Builtin != 3 || c.UserDefined != 2 {
			t.Fatalf("counts = %+v", c)
		}
	}
	q, _ := o.ListNetworks(&asterismv1.ListNetworksRequest{UserDefinedOnly: true, Query: "shop"})
	if len(q.Networks) != 1 || q.Counts.Total != 1 || q.Counts.UserDefined != 1 {
		t.Fatalf("query = %v counts %+v", q.Networks, q.Counts)
	}
}

// remoteActuator is an AttributedActuator + ActuatorStatus fake.
type remoteActuator struct {
	*fakeDocker
	reason string
	err    error
	actor  string
}

type codedErr struct{ code string }

func (e codedErr) Error() string      { return "agent says " + e.code }
func (e codedErr) ActionCode() string { return e.code }
func (e codedErr) AuditAttrs() map[string]string {
	return map[string]string{"executed_by": "agent:prod-1", "agent_audit_id": "x9"}
}
func (r *remoteActuator) ActionsUnavailableReason() string { return r.reason }
func (r *remoteActuator) ContainerActionAttributed(ctx context.Context, a asterismv1.ContainerAction, id string, timeout time.Duration, actor string) (*asterismv1.Container, map[string]string, error) {
	r.actor = actor
	if r.err != nil {
		return nil, nil, r.err
	}
	c, err := r.fakeDocker.ContainerAction(ctx, a, id, timeout)
	return c, map[string]string{"executed_by": "agent:prod-1", "agent_audit_id": "a1", "actor": "forged"}, err
}

func TestAttributedActuatorAuditAndErrors(t *testing.T) {
	fd := &fakeDocker{containers: []*asterismv1.Container{
		{Id: "ctr:shop-api-1", Name: "shop-api-1", ContainerId: "3f2a9c1b7d4e", State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING},
	}}
	ra := &remoteActuator{fakeDocker: fd}
	o := New(bus.New(8), ring.New(10), nil, nil, "agent")
	o.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.ConfigureDocker(ra, ra, ActionPolicy{Enabled: true, DefaultTimeout: time.Second})
	req := ActionRequest{Action: asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, Ref: "shop-api-1"}

	resp, err := o.PerformContainerAction(context.Background(), adminWithActions, req)
	if err != nil {
		t.Fatal(err)
	}
	attrs := resp.AuditEvent.Attrs
	if attrs["executed_by"] != "agent:prod-1" || attrs["agent_audit_id"] != "a1" || attrs["actor"] != "admin@example.com" || ra.actor != "admin@example.com" {
		t.Fatalf("audit attrs %v (actor passed %q)", attrs, ra.actor)
	}

	for code, want := range map[string]error{
		"permission_denied": ErrPermissionDenied, "not_found": ErrNotFound,
		"failed_precondition": ErrFailedPrecondition, "unavailable": ErrUnavailable, "internal": ErrUnavailable,
	} {
		ra.err = codedErr{code}
		_, err := o.PerformContainerAction(context.Background(), adminWithActions, req)
		if !errors.Is(err, want) {
			t.Fatalf("%s -> %v, want %v", code, err, want)
		}
		evs := o.ListEvents("ctr:shop-api-1", 1)
		if evs[0].Attrs["agent_audit_id"] != "x9" || evs[0].Audit.Result == "success" {
			t.Fatalf("failure audit %v", evs[0])
		}
	}

	ra.err, ra.reason = nil, "agent \"prod-1\" is offline"
	if _, err := o.PerformContainerAction(context.Background(), adminWithActions, req); !errors.Is(err, ErrFailedPrecondition) || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline actuator: %v", err)
	}
	if c := o.Capabilities(adminWithActions); c.CanPerformActions || !strings.Contains(c.ActionsDisabledReason, "offline") {
		t.Fatalf("capabilities %v", c)
	}
}

func TestLiveViewerHook(t *testing.T) {
	o := New(bus.New(4), ring.New(4), nil, nil, "agent")
	var seen []int
	o.OnLiveViewers(func(n int) { seen = append(seen, n) })
	_, unsub1 := o.SubscribeBus()
	_, unsub2 := o.SubscribeBus()
	unsub1()
	unsub1() // idempotent
	unsub2()
	if fmt.Sprint(seen) != "[1 2 1 0]" || o.LiveViewers() != 0 {
		t.Fatalf("viewer counts %v", seen)
	}
}

func TestProjectRuleIconFromLabels(t *testing.T) {
	inv := &listInventory{projects: []*asterismv1.Project{
		{Id: "proj:shop", Name: "shop", Labels: map[string]string{LabelIcon: "postgres", LabelColor: "chart-3"}},
		{Id: "proj:blog", Name: "blog", Labels: map[string]string{LabelIcon: "postgres"}},
	}}
	o := New(bus.New(4), ring.New(4), nil, inv, "agent")
	o.ConfigureProjects(map[string]ProjectDisplay{"blog": {Icon: "ghost"}})
	got := map[string]*asterismv1.ProjectIcon{}
	for _, p := range o.ListProjects("") {
		got[p.Name] = p.Icon
	}
	if i := got["shop"]; i.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED || i.Slug != "postgres" || i.ColorToken != "chart-3" {
		t.Fatalf("agent rule icon %v", i)
	}
	if i := got["blog"]; i.Slug != "ghost" {
		t.Fatalf("hub config must win over the agent rule: %v", i)
	}
	if o.ListAgents() == nil || len(o.ListAgents().Agents) != 0 {
		t.Fatal("ListAgents outside agent mode must be empty")
	}
}
