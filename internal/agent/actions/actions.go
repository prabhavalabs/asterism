// Package actions is the agent-side gate for mutating container actions.
//
// A hub command runs only when every local rule allows it: actions.enabled,
// the action listed in actions.allow, the container name matching an
// actions.containers glob, no actions.protect glob matching, and the
// container not being the agent itself. Every decision is written as a
// structured "audit" log line carrying the hub's request_id.
package actions

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Executor performs a container action against the Docker Engine API. It is
// implemented by the agent's Docker client (internal/agent/dockerapi), which
// has only the start/stop/restart/pause/unpause endpoints compiled in.
type Executor interface {
	// ContainerAction runs action on the container with Docker id
	// containerID (short or full). timeout is the stop/restart grace period.
	// It returns the container as inspected after the action, or nil.
	ContainerAction(ctx context.Context, containerID string, action v1.ContainerAction, timeout time.Duration) (*v1.Container, error)
}

// Errors an Executor may wrap so results carry the right code.
var (
	ErrNotFound = errors.New("container not found")
	ErrConflict = errors.New("container is not in a state that allows this action")
)

// Lookup resolves a CommandRequest.container_id (Docker id or "ctr:<name>")
// to the current inventory copy, or nil. state.Store.FindContainer fits.
type Lookup func(ref string) *v1.Container

// ProtectedReason is set on containers outside the agent allow-list.
const ProtectedReason = "not in agent allow-list"

// Default and maximum stop/restart grace periods.
const (
	DefaultTimeout = 10 * time.Second
	MaxTimeout     = 120 * time.Second
)

// Self identifies the agent's own container.
type Self struct {
	// ContainerIDs are full or short Docker ids found in /proc/self/cgroup.
	ContainerIDs []string
	// Hostname is the kernel hostname; Docker defaults it to the short id.
	Hostname string
}

var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)
var shortIDRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// DetectSelf reads cgroupPath (normally /proc/self/cgroup) for container ids,
// e.g. "0::/system.slice/docker-<id>.scope" or "12:pids:/docker/<id>".
// A missing file yields only the hostname.
func DetectSelf(cgroupPath, hostname string) Self {
	s := Self{Hostname: hostname}
	f, err := os.Open(cgroupPath)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		for _, id := range containerIDRe.FindAllString(sc.Text(), -1) {
			if !slices.Contains(s.ContainerIDs, id) {
				s.ContainerIDs = append(s.ContainerIDs, id)
			}
		}
	}
	return s
}

// Matches reports whether c is the agent's own container.
func (s Self) Matches(c *v1.Container) bool {
	if c.GetLabels()["asterism.self"] == "true" {
		return true
	}
	cid := c.GetContainerId()
	if cid == "" {
		return false
	}
	for _, id := range s.ContainerIDs {
		if strings.HasPrefix(id, cid) || strings.HasPrefix(cid, id) {
			return true
		}
	}
	return shortIDRe.MatchString(s.Hostname) && strings.HasPrefix(cid, s.Hostname)
}

// Gate evaluates the local allow-list.
type Gate struct {
	enabled    bool
	allow      []v1.ContainerAction
	containers []string
	protect    []string
	self       Self
}

// NewGate builds a gate from actions config.
func NewGate(cfg config.Actions, self Self) *Gate {
	g := &Gate{enabled: cfg.Enabled, containers: cfg.Containers, protect: cfg.Protect, self: self}
	for _, name := range cfg.Allow {
		if a, ok := ParseAction(name); ok && !slices.Contains(g.allow, a) {
			g.allow = append(g.allow, a)
		}
	}
	slices.Sort(g.allow)
	return g
}

// ParseAction maps "restart" to CONTAINER_ACTION_RESTART.
func ParseAction(name string) (v1.ContainerAction, bool) {
	v, ok := v1.ContainerAction_value["CONTAINER_ACTION_"+strings.ToUpper(strings.TrimSpace(name))]
	if !ok || v == 0 {
		return 0, false
	}
	return v1.ContainerAction(v), true
}

// ActionName maps CONTAINER_ACTION_RESTART to "restart".
func ActionName(a v1.ContainerAction) string {
	return strings.ToLower(strings.TrimPrefix(a.String(), "CONTAINER_ACTION_"))
}

// Allowed lists the actions the agent may perform on at least one container
// (advertised in Hello as allowed_actions and "action:<name>" capabilities).
func (g *Gate) Allowed() []v1.ContainerAction {
	if !g.enabled || len(g.containers) == 0 {
		return nil
	}
	return slices.Clone(g.allow)
}

// Capabilities returns "action:<name>" for every allowed action.
func (g *Gate) Capabilities() []string {
	var out []string
	for _, a := range g.Allowed() {
		out = append(out, "action:"+ActionName(a))
	}
	return out
}

// Denial explains why the gate refused.
type Denial struct {
	Reason string
}

func (d *Denial) Error() string { return d.Reason }

// Check returns nil when action on c is allowed by every local rule.
func (g *Gate) Check(action v1.ContainerAction, c *v1.Container) *Denial {
	name := ActionName(action)
	switch {
	case !g.enabled:
		return &Denial{"agent actions are disabled (actions.enabled is false in agent.yaml)"}
	case !slices.Contains(g.allow, action):
		return &Denial{fmt.Sprintf("action %q is not in actions.allow in agent.yaml", name)}
	}
	return g.containerDenial(c)
}

func (g *Gate) containerDenial(c *v1.Container) *Denial {
	n := c.GetName()
	switch {
	case g.self.Matches(c):
		return &Denial{"refusing to act on the agent's own container"}
	case matchAny(g.protect, n):
		return &Denial{fmt.Sprintf("container %q matches actions.protect in agent.yaml", n)}
	case !matchAny(g.containers, n):
		return &Denial{fmt.Sprintf("container %q does not match actions.containers in agent.yaml", n)}
	case c.GetProtected() && c.GetProtectedReason() != ProtectedReason:
		return &Denial{fmt.Sprintf("container %q is protected: %s", n, c.GetProtectedReason())}
	}
	return nil
}

// Annotate marks containers the agent will refuse as protected, so the hub
// UI shows them disabled with a reason. It is a no-op while actions are
// disabled (the hub then sees no supported actions at all).
func (g *Gate) Annotate(c *v1.Container) {
	if len(g.Allowed()) == 0 || c.GetProtected() {
		return
	}
	if d := g.containerDenial(c); d != nil {
		c.Protected = true
		c.ProtectedReason = ProtectedReason
		if g.self.Matches(c) {
			c.ProtectedReason = "asterism-agent itself"
		}
	}
}

func matchAny(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

// Service executes hub commands behind the gate and audits each one.
type Service struct {
	gate   *Gate
	exec   Executor
	lookup Lookup
	log    *slog.Logger
	now    func() time.Time
}

// NewService returns a Service. exec may be nil until a Docker client exists;
// commands then fail with code "unavailable".
func NewService(gate *Gate, exec Executor, lookup Lookup, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{gate: gate, exec: exec, lookup: lookup, log: logger, now: time.Now}
}

// Gate returns the service's gate.
func (s *Service) Gate() *Gate { return s.gate }

// Handle runs one command and returns its result. It never panics on
// malformed input.
func (s *Service) Handle(ctx context.Context, req *v1.CommandRequest) *v1.CommandResult {
	start := s.now()
	res := &v1.CommandResult{RequestId: req.GetRequestId(), AuditId: newAuditID()}
	var target *v1.Container
	result := "success"
	finish := func(code, msg string) *v1.CommandResult {
		res.Ok = code == ""
		res.Code = code
		res.Error = truncate(msg, 512)
		res.DurationMs = uint64(s.now().Sub(start).Milliseconds())
		attrs := []any{
			"audit_id", res.AuditId, "request_id", req.GetRequestId(), "actor", req.GetActor(),
			"action", ActionName(req.GetAction()), "target_ref", req.GetContainerId(),
			"result", result, "duration_ms", res.DurationMs,
		}
		if target != nil {
			attrs = append(attrs, "container", target.GetName(), "container_id", target.GetContainerId())
		}
		if msg != "" {
			attrs = append(attrs, "error", res.Error)
		}
		s.log.Info("audit", attrs...)
		return res
	}
	action := req.GetAction()
	if _, ok := v1.ContainerAction_name[int32(action)]; !ok || action == v1.ContainerAction_CONTAINER_ACTION_UNSPECIFIED {
		result = "invalid"
		return finish("invalid_argument", fmt.Sprintf("unknown action %d", action))
	}
	if s.lookup != nil {
		target = s.lookup(req.GetContainerId())
	}
	if target == nil {
		result = "not_found"
		return finish("not_found", fmt.Sprintf("container %q is not in the agent inventory", req.GetContainerId()))
	}
	if d := s.gate.Check(action, target); d != nil {
		result = "denied"
		return finish("permission_denied", d.Reason)
	}
	if s.exec == nil {
		result = "failure"
		return finish("unavailable", "the agent has no Docker client (sources.docker disabled or unreachable)")
	}
	timeout := DefaultTimeout
	if t := req.GetTimeoutSeconds(); t > 0 {
		timeout = min(time.Duration(t)*time.Second, MaxTimeout)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	after, err := s.exec.ContainerAction(cctx, target.GetContainerId(), action, timeout)
	if err != nil {
		result = "failure"
		switch {
		case errors.Is(err, ErrNotFound):
			result = "not_found"
			return finish("not_found", err.Error())
		case errors.Is(err, ErrConflict):
			return finish("failed_precondition", err.Error())
		case errors.Is(err, context.DeadlineExceeded):
			return finish("deadline_exceeded", err.Error())
		case errors.Is(err, context.Canceled):
			return finish("canceled", err.Error())
		default:
			return finish("internal", err.Error())
		}
	}
	res.Container = after
	return finish("", "")
}

func newAuditID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
