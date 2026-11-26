package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const maxStopTimeout = 300 * time.Second

// ActionRequest describes a mutating container action requested by a caller.
type ActionRequest struct {
	Action asterismv1.ContainerAction
	Ref    string
	// TimeoutSeconds applies to stop and restart; nil uses the configured default.
	TimeoutSeconds *uint32
	RemoteAddr     string
}

// actionLocks serialises actions per container so concurrent clicks cannot race.
type actionLocks struct {
	mu    sync.Mutex
	inUse map[string]bool
}

func (l *actionLocks) tryLock(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inUse == nil {
		l.inUse = make(map[string]bool)
	}
	if l.inUse[id] {
		return false
	}
	l.inUse[id] = true
	return true
}

func (l *actionLocks) unlock(id string) {
	l.mu.Lock()
	delete(l.inUse, id)
	l.mu.Unlock()
}

var auditSeq atomic.Uint64

// ActionName returns the lowercase verb for an action ("restart").
func ActionName(a asterismv1.ContainerAction) string {
	return strings.ToLower(strings.TrimPrefix(a.String(), "CONTAINER_ACTION_"))
}

// PerformContainerAction validates, authorises, executes, and audits a container action.
// Every attempt by an authenticated caller produces exactly one audit event.
func (o *Observatory) PerformContainerAction(ctx context.Context, p auth.Principal, req ActionRequest) (*asterismv1.ContainerActionResponse, error) {
	start := time.Now()
	audit := &asterismv1.ActionAudit{
		Actor:      p.Actor(),
		ActorKind:  p.Kind,
		Action:     ActionName(req.Action),
		TargetId:   dockercli.Truncate(req.Ref, 140),
		RemoteAddr: req.RemoteAddr,
	}
	var extra map[string]string
	fail := func(result string, err error) (*asterismv1.ContainerActionResponse, error) {
		audit.Result = result
		audit.Error = err.Error()
		audit.DurationMs = uint64(time.Since(start).Milliseconds())
		o.recordAudit(audit, extra)
		return nil, err
	}

	switch req.Action {
	case asterismv1.ContainerAction_CONTAINER_ACTION_START, asterismv1.ContainerAction_CONTAINER_ACTION_STOP,
		asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE,
		asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE:
	default:
		return fail("invalid", newErr(ErrInvalidArgument, "unsupported action %v", req.Action))
	}

	// Resolve the target first (read-only) so even refused attempts are attributed
	// to the right container in the audit trail.
	snap := o.snapshot()
	target, resolveErr := resolveContainer(snap.Containers, req.Ref)
	if target != nil {
		audit.TargetId = target.Id
		audit.TargetName = target.Name
		audit.ContainerId = target.ContainerId
		audit.StateBefore = dockercli.StateName(target.State)
	}

	// Gate 1: server-wide opt-in.
	if !o.policy.Enabled {
		return fail("denied", newErr(ErrFailedPrecondition, actionsDisabledMsg))
	}
	// Gate 2: caller scope.
	if !p.Has(auth.ScopeActions) {
		return fail("denied", newErr(ErrPermissionDenied, "the %q scope is required for container actions (caller %s has %v)", auth.ScopeActions, p.Actor(), p.ScopeNames()))
	}
	if o.actuator == nil {
		return fail("denied", newErr(ErrFailedPrecondition, "container actions are not available in %q collector mode", o.mode))
	}
	if reason := o.actuatorUnavailable(); reason != "" {
		return fail("denied", newErr(ErrFailedPrecondition, "%s", reason))
	}
	supported := false
	for _, a := range o.actuator.SupportedContainerActions() {
		if a == req.Action {
			supported = true
		}
	}
	if !supported {
		return fail("denied", newErr(ErrFailedPrecondition, "action %q is not supported by the %q collector", audit.Action, o.mode))
	}

	// Gate 3: target validation.
	if resolveErr != nil {
		result := "invalid"
		if errors.Is(resolveErr, ErrNotFound) {
			result = "not_found"
		}
		return fail(result, resolveErr)
	}
	if !dockercli.ValidContainerID(target.ContainerId) {
		return fail("invalid", newErr(ErrInternal, "container %s has no valid id", target.Name))
	}
	// Gate 4: self / protected containers.
	if target.Protected {
		return fail("denied", newErr(ErrPermissionDenied, "container %q is protected (%s); set allow_protected = true under [actions] to override", target.Name, target.ProtectedReason))
	}
	// Gate 5: state preconditions that Docker would otherwise report cryptically.
	switch req.Action {
	case asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE:
		if target.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
			return fail("invalid", newErr(ErrFailedPrecondition, "cannot pause %q: container is %s, not running", target.Name, audit.StateBefore))
		}
	case asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE:
		if target.State != asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
			return fail("invalid", newErr(ErrFailedPrecondition, "cannot unpause %q: container is %s, not paused", target.Name, audit.StateBefore))
		}
	case asterismv1.ContainerAction_CONTAINER_ACTION_STOP, asterismv1.ContainerAction_CONTAINER_ACTION_RESTART:
		if target.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED && req.Action == asterismv1.ContainerAction_CONTAINER_ACTION_RESTART {
			return fail("invalid", newErr(ErrFailedPrecondition, "cannot restart %q while paused; unpause it first", target.Name))
		}
	}

	timeout := o.policy.DefaultTimeout
	if req.TimeoutSeconds != nil {
		timeout = time.Duration(*req.TimeoutSeconds) * time.Second
		if timeout > maxStopTimeout {
			return fail("invalid", newErr(ErrInvalidArgument, "timeout_seconds must be at most %d", int(maxStopTimeout.Seconds())))
		}
	}

	if !o.locks.tryLock(target.ContainerId) {
		return fail("denied", newErr(ErrAborted, "another action on %q is still in progress", target.Name))
	}
	defer o.locks.unlock(target.ContainerId)

	// Detach from the client connection so a closed browser tab cannot leave the
	// action half-applied, but keep a hard upper bound.
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+45*time.Second)
	defer cancel()
	var after *asterismv1.Container
	var err error
	if aa, ok := o.actuator.(AttributedActuator); ok {
		after, extra, err = aa.ContainerActionAttributed(execCtx, req.Action, target.ContainerId, timeout, p.Actor())
		if err != nil {
			var withAttrs interface{ AuditAttrs() map[string]string }
			if errors.As(err, &withAttrs) && extra == nil {
				extra = withAttrs.AuditAttrs()
			}
			kind, result := remoteErrKind(err)
			return fail(result, newErr(kind, "%s %s failed: %s", audit.Action, target.Name, dockercli.Truncate(dockercli.RedactText(err.Error()), 400)))
		}
	} else {
		after, err = o.actuator.ContainerAction(execCtx, req.Action, target.ContainerId, timeout)
		if err != nil {
			return fail("failure", newErr(ErrUnavailable, "docker %s %s failed: %s", audit.Action, target.Name, dockercli.Truncate(dockercli.RedactText(err.Error()), 400)))
		}
	}
	if after != nil {
		o.decorate(after)
		audit.StateAfter = dockercli.StateName(after.State)
	}
	audit.Result = "success"
	audit.DurationMs = uint64(time.Since(start).Milliseconds())
	ev := o.recordAudit(audit, extra)
	return &asterismv1.ContainerActionResponse{
		Action:     req.Action,
		Container:  after,
		AuditEvent: ev,
		DurationMs: audit.DurationMs,
	}, nil
}

// remoteErrKind maps an AttributedActuator error to a service error kind and
// an audit result, using the error's Connect code name when it has one.
func remoteErrKind(err error) (error, string) {
	var coded interface{ ActionCode() string }
	if !errors.As(err, &coded) {
		return ErrUnavailable, "failure"
	}
	switch coded.ActionCode() {
	case "permission_denied":
		return ErrPermissionDenied, "denied"
	case "not_found":
		return ErrNotFound, "not_found"
	case "invalid_argument":
		return ErrInvalidArgument, "invalid"
	case "failed_precondition":
		return ErrFailedPrecondition, "invalid"
	case "aborted", "already_exists":
		return ErrAborted, "denied"
	default:
		return ErrUnavailable, "failure"
	}
}

// recordAudit stores and publishes one audit event. extra attributes (for
// example executed_by and agent_audit_id) are added to Event.attrs; they
// never override actor, action, or result.
func (o *Observatory) recordAudit(a *asterismv1.ActionAudit, extra map[string]string) *asterismv1.Event {
	now := time.Now()
	sev := "info"
	var msg string
	target := a.TargetName
	if target == "" {
		target = a.TargetId
	}
	switch a.Result {
	case "success":
		msg = fmt.Sprintf("%s %s %s (%s → %s, %d ms)", a.Actor, pastTense(a.Action), target, a.StateBefore, a.StateAfter, a.DurationMs)
	default:
		sev = "warning"
		msg = fmt.Sprintf("%s could not %s %s: %s", a.Actor, a.Action, target, a.Error)
	}
	entity := a.TargetId
	if !strings.HasPrefix(entity, "ctr:") {
		entity = "host"
	}
	ev := &asterismv1.Event{
		Id:         fmt.Sprintf("audit:%d-%d", now.UnixMilli(), auditSeq.Add(1)),
		TsMs:       now.UnixMilli(),
		EntityId:   entity,
		EntityName: target,
		Kind:       "audit.container." + a.Action,
		Severity:   sev,
		Message:    msg,
		Attrs: map[string]string{
			"actor":  a.Actor,
			"action": a.Action,
			"result": a.Result,
		},
		Audit: a,
	}
	for k, v := range extra {
		if _, taken := ev.Attrs[k]; !taken && k != "" {
			ev.Attrs[k] = dockercli.Truncate(v, 200)
		}
	}
	o.events.Append(ev)
	o.bus.Publish(bus.Message{Events: []*asterismv1.Event{ev}})
	o.logger.Info("audit",
		"action", a.Action, "result", a.Result, "actor", a.Actor, "actor_kind", a.ActorKind,
		"target", a.TargetId, "container_id", a.ContainerId, "duration_ms", a.DurationMs,
		"state_before", a.StateBefore, "state_after", a.StateAfter, "remote", a.RemoteAddr, "error", a.Error,
		"executed_by", ev.Attrs["executed_by"], "agent_audit_id", ev.Attrs["agent_audit_id"])
	return ev
}

func pastTense(action string) string {
	switch action {
	case "stop":
		return "stopped"
	case "pause":
		return "paused"
	case "unpause":
		return "unpaused"
	default:
		return action + "ed"
	}
}

// containerFingerprint is the state compared between watcher polls.
type containerFingerprint struct {
	name         string
	state        asterismv1.ContainerState
	health       asterismv1.ContainerHealth
	restartCount uint32
	startedAtMs  int64
	exitCode     int32
	oomKilled    bool
}

// RunDockerWatcher diffs Docker snapshots and records lifecycle events
// (start, die, oom, restart, health, pause, unpause, create, destroy).
func (o *Observatory) RunDockerWatcher(ctx context.Context, interval time.Duration) error {
	if o.docker == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var prev map[string]containerFingerprint
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		snap := o.docker.DockerSnapshot()
		if len(snap.Containers) > 0 || prev != nil {
			cur := make(map[string]containerFingerprint, len(snap.Containers))
			for _, c := range snap.Containers {
				cur[c.Id] = containerFingerprint{c.Name, c.State, c.Health, c.RestartCount, c.StartedAtMs, c.ExitCode, c.OomKilled}
			}
			if prev != nil {
				evs := diffContainers(prev, cur, time.Now())
				for _, e := range evs {
					o.events.Append(e)
				}
				if len(evs) > 0 {
					o.bus.Publish(bus.Message{Events: evs})
				}
			}
			prev = cur
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func diffContainers(prev, cur map[string]containerFingerprint, now time.Time) []*asterismv1.Event {
	var out []*asterismv1.Event
	seq := 0
	emit := func(id, name, kind, sev, msg string, attrs map[string]string) {
		seq++
		out = append(out, &asterismv1.Event{
			Id:         fmt.Sprintf("evt:%s:%s:%d-%d", kind, name, now.UnixMilli(), seq),
			TsMs:       now.UnixMilli(),
			EntityId:   id,
			EntityName: name,
			Kind:       kind,
			Severity:   sev,
			Message:    msg,
			Attrs:      attrs,
		})
	}
	for _, id := range dockercli.SortedKeys(cur) {
		c := cur[id]
		p, existed := prev[id]
		if !existed {
			emit(id, c.name, "container.create", "info", fmt.Sprintf("Container %s appeared (%s)", c.name, dockercli.StateName(c.state)), nil)
			continue
		}
		if c.oomKilled && (!p.oomKilled || c.startedAtMs != p.startedAtMs) && c.state != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
			emit(id, c.name, "container.oom", "critical", fmt.Sprintf("Container %s was OOM-killed (exit code %d)", c.name, c.exitCode), map[string]string{"exit_code": fmt.Sprint(c.exitCode)})
		}
		if c.restartCount > p.restartCount {
			emit(id, c.name, "container.restart", "warning", fmt.Sprintf("Container %s restarted by its restart policy (%d restarts)", c.name, c.restartCount), map[string]string{"restart_count": fmt.Sprint(c.restartCount)})
		}
		if c.state != p.state {
			switch c.state {
			case asterismv1.ContainerState_CONTAINER_STATE_RUNNING:
				if p.state == asterismv1.ContainerState_CONTAINER_STATE_PAUSED {
					emit(id, c.name, "container.unpause", "info", fmt.Sprintf("Container %s unpaused", c.name), nil)
				} else {
					emit(id, c.name, "container.start", "info", fmt.Sprintf("Container %s started", c.name), nil)
				}
			case asterismv1.ContainerState_CONTAINER_STATE_PAUSED:
				emit(id, c.name, "container.pause", "info", fmt.Sprintf("Container %s paused", c.name), nil)
			case asterismv1.ContainerState_CONTAINER_STATE_EXITED, asterismv1.ContainerState_CONTAINER_STATE_DEAD:
				sev := "info"
				if c.exitCode != 0 {
					sev = "warning"
				}
				emit(id, c.name, "container.die", sev, fmt.Sprintf("Container %s exited with code %d", c.name, c.exitCode), map[string]string{"exit_code": fmt.Sprint(c.exitCode)})
			case asterismv1.ContainerState_CONTAINER_STATE_RESTARTING:
				emit(id, c.name, "container.restarting", "warning", fmt.Sprintf("Container %s is restarting (exit code %d)", c.name, c.exitCode), map[string]string{"exit_code": fmt.Sprint(c.exitCode)})
			}
		} else if c.state == asterismv1.ContainerState_CONTAINER_STATE_RUNNING && c.startedAtMs > p.startedAtMs && c.restartCount == p.restartCount {
			emit(id, c.name, "container.start", "info", fmt.Sprintf("Container %s was restarted", c.name), nil)
		}
		if c.health != p.health && c.health != asterismv1.ContainerHealth_CONTAINER_HEALTH_UNSPECIFIED {
			switch c.health {
			case asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY:
				emit(id, c.name, "container.health", "warning", fmt.Sprintf("Container %s became unhealthy", c.name), map[string]string{"health": "unhealthy"})
			case asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY:
				if p.health == asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY {
					emit(id, c.name, "container.health", "info", fmt.Sprintf("Container %s recovered and is healthy", c.name), map[string]string{"health": "healthy"})
				}
			}
		}
	}
	for _, id := range dockercli.SortedKeys(prev) {
		if _, ok := cur[id]; !ok {
			emit(id, prev[id].name, "container.destroy", "info", fmt.Sprintf("Container %s was removed", prev[id].name), nil)
		}
	}
	return out
}

func defaultLogger() *slog.Logger { return slog.Default() }
