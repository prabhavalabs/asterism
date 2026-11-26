package agentingest

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

// respond answers every command on s with success and counts them.
func respond(s *session, n *atomic.Int32) {
	go func() {
		for {
			select {
			case f := <-s.s.out:
				if cmd := f.GetCommand(); cmd != nil {
					n.Add(1)
					c := shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)
					select {
					case s.s.in <- &asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_CommandResult{CommandResult: &asterismv1.CommandResult{
						RequestId: cmd.GetRequestId(), Ok: true, Container: c, AuditId: "aud-1",
					}}}:
					case <-s.s.eof:
						return
					}
				}
			case <-s.s.eof:
				return
			}
		}
	}()
}

// TestActionGatingMatrix checks every combination of the five action gates:
// hub [actions] allow_actions × per-agent allow_actions × caller scope ×
// agent allow-list (Hello.allowed_actions) × protected container. Only the
// all-open combination reaches the agent.
func TestActionGatingMatrix(t *testing.T) {
	type gates struct{ hub, perAgent, scope, agentList, protected bool }
	for i := range 32 {
		g := gates{i&1 != 0, i&2 != 0, i&4 != 0, i&8 != 0, i&16 != 0}
		name := fmt.Sprintf("hub=%t/agent=%t/scope=%t/list=%t/protected=%t", g.hub, g.perAgent, g.scope, g.agentList, g.protected)
		t.Run(name, func(t *testing.T) {
			h := newTestHub(t, nil, Agent{Name: "prod-1", AllowActions: g.perAgent})
			hl := hello("prod-1", "sess-a")
			if !g.agentList {
				hl.AllowedActions = nil
			}
			s, err := h.dial("prod-1", hl, h.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			c := shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING)
			if g.protected {
				c.Protected, c.ProtectedReason = true, "not in agent allow-list"
			}
			s.send(snapshotFrame(1, c))
			s.sync()
			var sent atomic.Int32
			respond(s, &sent)

			b := bus.New(64)
			v := h.reg.Primary()
			svc := service.New(b, ring.New(16), v, v, "agent")
			svc.ConfigureDocker(v, v, service.ActionPolicy{Enabled: g.hub, DefaultTimeout: time.Second})
			p := auth.Principal{Kind: "session", Subject: "admin@example.com", Scopes: []auth.Scope{auth.ScopeRead}}
			if g.scope {
				p.Scopes = append(p.Scopes, auth.ScopeActions)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := svc.PerformContainerAction(ctx, p, service.ActionRequest{Action: asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, Ref: "ctr:shop-api-1"})

			var want error
			switch {
			case !g.hub:
				want = service.ErrFailedPrecondition
			case !g.scope:
				want = service.ErrPermissionDenied
			case !g.perAgent, !g.agentList:
				want = service.ErrFailedPrecondition
			case g.protected:
				want = service.ErrPermissionDenied
			}
			caps := svc.Capabilities(p)
			if want == nil {
				if err != nil {
					t.Fatalf("all gates open but %v", err)
				}
				ev := resp.GetAuditEvent()
				if ev.GetAttrs()["executed_by"] != "agent:prod-1" || ev.GetAttrs()["agent_audit_id"] != "aud-1" || ev.GetAudit().GetResult() != "success" {
					t.Fatalf("audit event %v", ev)
				}
				if sent.Load() != 1 || !caps.GetCanPerformActions() {
					t.Fatalf("commands sent %d, caps %v", sent.Load(), caps)
				}
				return
			}
			if !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if sent.Load() != 0 {
				t.Fatal("a refused action reached the agent")
			}
			// Gates other than "protected" are visible in capabilities.
			if !g.protected && caps.GetCanPerformActions() {
				t.Fatalf("capabilities allow actions: %v", caps)
			}
		})
	}
}

func TestCapabilitiesInAgentMode(t *testing.T) {
	h := newTestHub(t, nil, Agent{Name: "prod-1", AllowActions: true})
	v := h.reg.Primary()
	svc := service.New(bus.New(4), ring.New(4), v, v, "agent")
	svc.ConfigureDocker(v, v, service.ActionPolicy{Enabled: true})
	p := auth.Principal{Kind: "session", Subject: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeActions}}
	caps := svc.Capabilities(p)
	if caps.GetSourceMode() != "agent" || caps.GetCanPerformActions() || caps.GetActionsDisabledReason() == "" || !caps.GetDockerAvailable() {
		t.Fatalf("before connect: %v", caps)
	}
	h.mustDial("prod-1", "s")
	caps = svc.Capabilities(p)
	if !caps.GetCanPerformActions() || len(caps.GetContainerActions()) != 1 {
		t.Fatalf("connected: %v", caps)
	}
}
