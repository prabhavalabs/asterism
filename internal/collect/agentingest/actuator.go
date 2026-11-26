package agentingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/iconscan"
)

// CommandError is an agent-side action failure. ActionCode is a Connect code
// name ("permission_denied", "not_found", "unavailable", ...); AuditAttrs
// links the hub audit event to the agent's audit line.
type CommandError struct {
	Code  string
	Msg   string
	Attrs map[string]string
}

func (e *CommandError) Error() string { return e.Msg }

// ActionCode returns the Connect code name.
func (e *CommandError) ActionCode() string { return e.Code }

// AuditAttrs returns audit attributes (executed_by, agent_request_id, ...).
func (e *CommandError) AuditAttrs() map[string]string { return e.Attrs }

var allActions = []asterismv1.ContainerAction{
	asterismv1.ContainerAction_CONTAINER_ACTION_START,
	asterismv1.ContainerAction_CONTAINER_ACTION_STOP,
	asterismv1.ContainerAction_CONTAINER_ACTION_RESTART,
	asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE,
	asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE,
}

// SupportedContainerActions is the intersection of what the hub implements
// and what the agent's own allow-list permits (Hello.allowed_actions). It is
// empty when the hub-side [[agents]] allow_actions gate is off.
func (v *HostView) SupportedContainerActions() []asterismv1.ContainerAction {
	hs := v.hs
	if !hs.agent.AllowActions {
		return nil
	}
	hs.mu.Lock()
	defer hs.mu.Unlock()
	var out []asterismv1.ContainerAction
	for _, a := range allActions {
		if slices.Contains(hs.allowed, a) {
			out = append(out, a)
		}
	}
	return out
}

// ActionsUnavailableReason explains why no action can run right now, or "".
func (v *HostView) ActionsUnavailableReason() string {
	hs := v.hs
	name := hs.agent.Name
	if !hs.agent.AllowActions {
		return fmt.Sprintf("container actions are disabled for agent %q: set allow_actions = true in its [[agents]] entry in asterism.toml and restart Asterism", name)
	}
	now := hs.reg.now()
	hs.mu.Lock()
	defer hs.mu.Unlock()
	switch st := hs.stateNameLocked(now); st {
	case "never":
		return fmt.Sprintf("agent %q has not connected yet; actions need a connected agent", name)
	case "connected":
	default:
		return fmt.Sprintf("agent %q is %s (last seen %s ago); actions need a connected agent", name, st, now.Sub(hs.lastSeen).Round(time.Second))
	}
	if len(hs.allowed) == 0 {
		return fmt.Sprintf("agent %q does not allow container actions: set actions.enabled, actions.allow and actions.containers in its agent.yaml", name)
	}
	return ""
}

// ContainerAction implements service.ContainerActuator.
func (v *HostView) ContainerAction(ctx context.Context, action asterismv1.ContainerAction, containerID string, timeout time.Duration) (*asterismv1.Container, error) {
	c, _, err := v.ContainerActionAttributed(ctx, action, containerID, timeout, "")
	return c, err
}

// ContainerActionAttributed sends the action to the agent over the live
// session and waits for its result. Commands are never replayed: if the
// connection drops before the result arrives the outcome is unknown and the
// error says so (Unavailable).
func (v *HostView) ContainerActionAttributed(ctx context.Context, action asterismv1.ContainerAction, containerID string, timeout time.Duration, actor string) (*asterismv1.Container, map[string]string, error) {
	hs := v.hs
	reqID := randID(12)
	attrs := map[string]string{"executed_by": "agent:" + hs.agent.Name, "agent_request_id": reqID}
	fail := func(code, format string, args ...any) (*asterismv1.Container, map[string]string, error) {
		return nil, attrs, &CommandError{Code: code, Msg: fmt.Sprintf(format, args...), Attrs: attrs}
	}
	if !hs.agent.AllowActions {
		return fail("failed_precondition", "container actions are disabled for agent %q ([[agents]] allow_actions)", hs.agent.Name)
	}
	if !slices.Contains(v.SupportedContainerActions(), action) {
		return fail("failed_precondition", "agent %q does not allow %s (agent.yaml actions.allow)", hs.agent.Name, actionVerb(action))
	}
	if !dockercli.ValidContainerID(containerID) {
		return fail("invalid_argument", "refusing action on invalid container id %q", dockercli.Truncate(containerID, 80))
	}
	if timeout < 0 {
		timeout = 0
	}
	now := hs.reg.now()
	req := &asterismv1.CommandRequest{
		RequestId:      reqID,
		Action:         action,
		ContainerId:    containerID,
		TimeoutSeconds: uint32(timeout / time.Second),
		// The agent refuses the command after this (hub wall clock, which the
		// agent compares against its own clock).
		DeadlineMs: now.Add(timeout + 30*time.Second).UnixMilli(),
		Actor:      dockercli.Truncate(actor, 120),
	}
	start := time.Now()
	res, err := hs.reg.runCommand(ctx, hs, req)
	if err != nil {
		return nil, attrs, &CommandError{Code: "unavailable", Msg: err.Error(), Attrs: attrs}
	}
	if res.GetAuditId() != "" {
		attrs["agent_audit_id"] = dockercli.Truncate(res.GetAuditId(), 120)
	}
	attrs["agent_duration_ms"] = fmt.Sprint(res.GetDurationMs())
	if !res.GetOk() {
		code := strings.ToLower(res.GetCode())
		if code == "" {
			code = "unknown"
		}
		msg := dockercli.Truncate(dockercli.RedactText(res.GetError()), 400)
		if msg == "" {
			msg = "the agent refused the action"
		}
		return nil, attrs, &CommandError{Code: code, Msg: "agent " + hs.agent.Name + ": " + msg, Attrs: attrs}
	}
	hs.reg.log.Debug("agent action done", "host", hs.agent.Name, "request_id", reqID, "took", time.Since(start).Round(time.Millisecond))
	var after *asterismv1.Container
	if c := res.GetContainer(); c != nil {
		after = proto.Clone(c).(*asterismv1.Container)
		v.applyContainerUpdate(after)
	}
	return after, attrs, nil
}

func actionVerb(a asterismv1.ContainerAction) string {
	return strings.ToLower(strings.TrimPrefix(a.String(), "CONTAINER_ACTION_"))
}

var (
	errCommandNotSent = errors.New("the agent connection closed before the command was sent; nothing was executed")
)

// runCommand queues req on the host's current connection and waits for the
// matching CommandResult.
func (r *Registry) runCommand(ctx context.Context, hs *hostState, req *asterismv1.CommandRequest) (*asterismv1.CommandResult, error) {
	hs.mu.Lock()
	c := hs.conn
	if c == nil {
		hs.mu.Unlock()
		return nil, fmt.Errorf("agent %q is not connected; nothing was executed", hs.agent.Name)
	}
	pc := &pendingCmd{conn: c, ch: make(chan *asterismv1.CommandResult, 1)}
	hs.pending[req.GetRequestId()] = pc
	hs.mu.Unlock()
	defer func() {
		hs.mu.Lock()
		delete(hs.pending, req.GetRequestId())
		hs.mu.Unlock()
	}()

	frame := &asterismv1.HubFrame{Payload: &asterismv1.HubFrame_Command{Command: req}}
	select {
	case c.out <- frame:
	case <-c.done:
		return nil, errCommandNotSent
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out queueing the command for agent %q; nothing was executed", hs.agent.Name)
	}
	select {
	case res := <-pc.ch:
		return res, nil
	case <-c.done:
		return nil, fmt.Errorf("result unknown: the connection to agent %q dropped while %s was in flight (%v); inventory will refresh when it reconnects",
			hs.agent.Name, actionVerb(req.GetAction()), c.err)
	case <-ctx.Done():
		return nil, fmt.Errorf("result unknown: agent %q did not answer %s in time; inventory will refresh", hs.agent.Name, actionVerb(req.GetAction()))
	}
}

// fetchLogs runs one non-follow LogRequest and collects lines until EOF or
// LogTimeout.
func (r *Registry) fetchLogs(hs *hostState, workloadID string) []*asterismv1.LogLine {
	now := r.now()
	line := func(text string) []*asterismv1.LogLine {
		return []*asterismv1.LogLine{{TsMs: now.UnixMilli(), WorkloadId: workloadID, Stream: "stderr", Text: text}}
	}
	if workloadID == "" || len(workloadID) > 200 {
		return nil
	}
	hs.mu.Lock()
	c := hs.conn
	if c == nil {
		hs.mu.Unlock()
		return line(fmt.Sprintf("agent %s is offline; logs are read on demand from the agent", hs.agent.Name))
	}
	reqID := randID(12)
	pl := &pendingLog{conn: c, ch: make(chan *asterismv1.LogChunk, 32)}
	hs.logs[reqID] = pl
	skew := hs.skewAdjMs
	hs.mu.Unlock()
	defer func() {
		hs.mu.Lock()
		delete(hs.logs, reqID)
		hs.mu.Unlock()
	}()

	req := &asterismv1.HubFrame{Payload: &asterismv1.HubFrame_LogRequest{LogRequest: &asterismv1.LogRequest{RequestId: reqID, WorkloadId: workloadID, Tail: 200}}}
	if !c.trySend(req) {
		return line(fmt.Sprintf("agent %s is busy; try again", hs.agent.Name))
	}
	timer := time.NewTimer(r.opts.LogTimeout)
	defer timer.Stop()
	var out []*asterismv1.LogLine
	for {
		select {
		case ch := <-pl.ch:
			for _, l := range ch.GetLines() {
				cl := proto.Clone(l).(*asterismv1.LogLine)
				cl.WorkloadId = workloadID
				if cl.TsMs > 0 {
					cl.TsMs += skew
				}
				cl.Text = dockercli.RedactText(cl.Text)
				out = append(out, cl)
				if len(out) >= 500 {
					return out
				}
			}
			if e := ch.GetError(); e != "" {
				return append(out, line("unable to read logs: "+dockercli.Truncate(dockercli.RedactText(e), 300))...)
			}
			if ch.GetEof() {
				return out
			}
		case <-c.done:
			return append(out, line("agent connection closed while reading logs")...)
		case <-timer.C:
			return append(out, line(fmt.Sprintf("agent %s did not finish sending logs within %s", hs.agent.Name, r.opts.LogTimeout))...)
		}
	}
}

// fetchIcon asks the agent for the bytes of a reported project icon and
// checks them against the digest.
func (r *Registry) fetchIcon(ctx context.Context, hs *hostState, digest string) ([]byte, error) {
	hs.mu.Lock()
	c := hs.conn
	if c == nil {
		hs.mu.Unlock()
		return nil, fmt.Errorf("agent %q is offline", hs.agent.Name)
	}
	reqID := randID(12)
	pi := &pendingIcon{conn: c, ch: make(chan *asterismv1.IconData, 1)}
	hs.icons[reqID] = pi
	hs.mu.Unlock()
	defer func() {
		hs.mu.Lock()
		delete(hs.icons, reqID)
		hs.mu.Unlock()
	}()
	req := &asterismv1.HubFrame{Payload: &asterismv1.HubFrame_IconRequest{IconRequest: &asterismv1.IconRequest{RequestId: reqID, Sha256: digest}}}
	if !c.trySend(req) {
		return nil, fmt.Errorf("agent %q is busy", hs.agent.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, r.opts.LogTimeout*2)
	defer cancel()
	select {
	case d := <-pi.ch:
		if e := d.GetError(); e != "" {
			return nil, fmt.Errorf("agent %q: %s", hs.agent.Name, dockercli.Truncate(e, 200))
		}
		data := d.GetData()
		if len(data) == 0 || len(data) > iconscan.MaxBytes {
			return nil, fmt.Errorf("agent %q sent an icon of %d bytes", hs.agent.Name, len(data))
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			return nil, fmt.Errorf("agent %q sent an icon that does not match its digest", hs.agent.Name)
		}
		return data, nil
	case <-c.done:
		return nil, fmt.Errorf("agent %q disconnected", hs.agent.Name)
	case <-ctx.Done():
		return nil, fmt.Errorf("agent %q did not send the icon in time", hs.agent.Name)
	}
}
