// Package alerting evaluates the built-in alert rule catalogue against the
// service snapshot and event stream, runs the alert lifecycle
// (inactive -> pending -> firing -> resolved) with silences, grouping,
// repeat and flap protection, and dispatches notifications to channels with
// a bounded retrying queue. docs/BLUEPRINT.md §6.9.
package alerting

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Rule ids of the built-in catalogue.
const (
	RuleContainerExited = "container_exited"
	RuleRestartLoop     = "restart_loop"
	RuleUnhealthy       = "container_unhealthy"
	RuleOOMKill         = "oom_kill"
	RuleContainerMemory = "container_memory"
	RuleHostCPU         = "host_cpu"
	RuleHostMemory      = "host_memory"
	RuleDiskUsage       = "disk_usage"
	RuleAgentOffline    = "agent_offline"
	RuleUnitFailed      = "unit_failed"
	RuleEventForward    = "event_forward"
)

const (
	sevInfo     = asterismv1.AlertSeverity_ALERT_SEVERITY_INFO
	sevWarning  = asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING
	sevCritical = asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL

	kindCondition = asterismv1.AlertRuleKind_ALERT_RULE_KIND_CONDITION
	kindThreshold = asterismv1.AlertRuleKind_ALERT_RULE_KIND_THRESHOLD
	kindCount     = asterismv1.AlertRuleKind_ALERT_RULE_KIND_COUNT
	kindForward   = asterismv1.AlertRuleKind_ALERT_RULE_KIND_FORWARD
)

// DefaultRules returns the built-in catalogue with default settings, in
// display order. The clearly-bad conditions are on by default; host CPU and
// event forwarding are opt-in because they are noisy on busy hosts.
func DefaultRules() []*asterismv1.AlertRule {
	return []*asterismv1.AlertRule{
		{Id: RuleContainerExited, Name: "Container exited unexpectedly", Kind: kindCondition, Enabled: true, Severity: sevCritical,
			Description: "A container stopped with a non-zero exit code and was not stopped by an Asterism action. Resolves when it runs again or is removed."},
		{Id: RuleRestartLoop, Name: "Restart loop", Kind: kindCount, Enabled: true, Severity: sevWarning, Count: 3, WindowSeconds: 600,
			Description: "A container restarted at least N times within the window, or Docker reports it as restarting."},
		{Id: RuleUnhealthy, Name: "Health check failing", Kind: kindCondition, Enabled: true, Severity: sevWarning, ForSeconds: 60,
			Description: "A container's Docker health check reports unhealthy."},
		{Id: RuleOOMKill, Name: "Out-of-memory kill", Kind: kindCondition, Enabled: true, Severity: sevCritical,
			Description: "A container's last exit was an OOM kill (until it runs again), or a systemd unit reported an OOM kill."},
		{Id: RuleContainerMemory, Name: "Container memory near limit", Kind: kindThreshold, Enabled: true, WarningThreshold: 90, CriticalThreshold: 98, ForSeconds: 300,
			Description: "Container memory use as a percentage of its memory limit (containers without a limit are skipped)."},
		{Id: RuleHostCPU, Name: "Host CPU high", Kind: kindThreshold, Enabled: false, WarningThreshold: 90, ForSeconds: 600,
			Description: "Host CPU utilisation across all cores."},
		{Id: RuleHostMemory, Name: "Host memory high", Kind: kindThreshold, Enabled: true, WarningThreshold: 90, CriticalThreshold: 97, ForSeconds: 300,
			Description: "Host memory in use as a percentage of total RAM."},
		{Id: RuleDiskUsage, Name: "Disk usage high", Kind: kindThreshold, Enabled: true, WarningThreshold: 85, CriticalThreshold: 95, ForSeconds: 300,
			Description: "Filesystem usage per mount point."},
		{Id: RuleAgentOffline, Name: "Agent offline", Kind: kindCondition, Enabled: true, Severity: sevCritical,
			Description: "An enrolled asterism-agent stopped reporting (after collect.agent.stale_after). Agent mode only."},
		{Id: RuleUnitFailed, Name: "systemd unit failed", Kind: kindCondition, Enabled: true, Severity: sevWarning,
			Description: "A systemd unit is in the failed state."},
		{Id: RuleEventForward, Name: "Event forwarding", Kind: kindForward, Enabled: false, MinEventSeverity: sevCritical,
			Description: "Forwards events of the chosen kinds at or above the chosen severity as one-shot alerts. Audit, alert, and notification events are never forwarded."},
	}
}

func defaultRule(id string) *asterismv1.AlertRule {
	for _, r := range DefaultRules() {
		if r.Id == id {
			return r
		}
	}
	return nil
}

// mergeRule overlays stored settings on the default (keeping the catalogue
// name, description, and kind).
func mergeRule(def, stored *asterismv1.AlertRule) *asterismv1.AlertRule {
	r := proto.Clone(def).(*asterismv1.AlertRule)
	if stored == nil {
		r.IsDefault = true
		return r
	}
	r.Enabled = stored.Enabled
	r.Severity = stored.Severity
	r.WarningThreshold = stored.WarningThreshold
	r.CriticalThreshold = stored.CriticalThreshold
	r.Count = stored.Count
	r.WindowSeconds = stored.WindowSeconds
	r.ForSeconds = stored.ForSeconds
	r.IncludeProjects = slices.Clone(stored.IncludeProjects)
	r.ExcludeProjects = slices.Clone(stored.ExcludeProjects)
	r.EventKinds = slices.Clone(stored.EventKinds)
	r.MinEventSeverity = stored.MinEventSeverity
	r.IsDefault = sameSettings(r, def)
	return r
}

func sameSettings(a, b *asterismv1.AlertRule) bool {
	x := proto.Clone(a).(*asterismv1.AlertRule)
	y := proto.Clone(b).(*asterismv1.AlertRule)
	x.IsDefault, y.IsDefault = false, false
	return proto.Equal(x, y)
}

func cleanList(in []string, max int) ([]string, error) {
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || slices.Contains(out, v) {
			continue
		}
		if len(v) > 120 || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("invalid value %q", v)
		}
		out = append(out, v)
	}
	if len(out) > max {
		return nil, fmt.Errorf("at most %d values", max)
	}
	return out, nil
}

// ValidateRule normalises an update against the catalogue entry and
// returns the settings to store.
func ValidateRule(in *asterismv1.AlertRule) (*asterismv1.AlertRule, error) {
	def := defaultRule(strings.TrimSpace(in.GetId()))
	if def == nil {
		return nil, fmt.Errorf("unknown rule %q", in.GetId())
	}
	out := proto.Clone(def).(*asterismv1.AlertRule)
	out.Enabled = in.GetEnabled()
	if in.GetForSeconds() > 7*24*3600 {
		return nil, fmt.Errorf("for_seconds must be at most 7 days")
	}
	out.ForSeconds = in.GetForSeconds()
	var err error
	if out.IncludeProjects, err = cleanList(in.GetIncludeProjects(), 50); err != nil {
		return nil, fmt.Errorf("include_projects: %w", err)
	}
	if out.ExcludeProjects, err = cleanList(in.GetExcludeProjects(), 50); err != nil {
		return nil, fmt.Errorf("exclude_projects: %w", err)
	}
	validSev := func(s asterismv1.AlertSeverity) bool { return s >= sevInfo && s <= sevCritical }
	switch def.Kind {
	case kindCondition, kindCount:
		if !validSev(in.GetSeverity()) {
			return nil, fmt.Errorf("severity must be info, warning, or critical")
		}
		out.Severity = in.GetSeverity()
		if def.Kind == kindCount {
			if in.GetCount() < 1 || in.GetCount() > 1000 {
				return nil, fmt.Errorf("count must be 1-1000")
			}
			if in.GetWindowSeconds() < 60 || in.GetWindowSeconds() > 24*3600 {
				return nil, fmt.Errorf("window_seconds must be 60-86400")
			}
			out.Count, out.WindowSeconds = in.GetCount(), in.GetWindowSeconds()
		}
	case kindThreshold:
		w, c := in.GetWarningThreshold(), in.GetCriticalThreshold()
		if w < 0 || w > 100 || c < 0 || c > 100 {
			return nil, fmt.Errorf("thresholds are percentages (0-100; 0 disables a level)")
		}
		if w == 0 && c == 0 {
			return nil, fmt.Errorf("set a warning or a critical threshold")
		}
		if w > 0 && c > 0 && c <= w {
			return nil, fmt.Errorf("the critical threshold must be above the warning threshold")
		}
		out.WarningThreshold, out.CriticalThreshold = w, c
	case kindForward:
		if out.EventKinds, err = cleanList(in.GetEventKinds(), 50); err != nil {
			return nil, fmt.Errorf("event_kinds: %w", err)
		}
		for _, k := range out.EventKinds {
			if forwardExcluded(k) {
				return nil, fmt.Errorf("event kind %q cannot be forwarded", k)
			}
		}
		if !validSev(in.GetMinEventSeverity()) {
			return nil, fmt.Errorf("min_event_severity must be info, warning, or critical")
		}
		out.MinEventSeverity = in.GetMinEventSeverity()
		out.ForSeconds = 0
	}
	out.IsDefault = sameSettings(out, def)
	return out, nil
}

// forwardExcluded reports event kinds that are never forwarded (to avoid
// loops and audit noise).
func forwardExcluded(kind string) bool {
	return strings.HasPrefix(kind, "audit.") || strings.HasPrefix(kind, "alert.") || strings.HasPrefix(kind, "notification.")
}

// projectAllowed applies a rule's project filters to a project id
// ("proj:shop"); names ("shop") match too. Host-level entities have no
// project and pass include filters only when none are set.
func projectAllowed(r *asterismv1.AlertRule, projectID string) bool {
	name := strings.TrimPrefix(projectID, "proj:")
	match := func(list []string) bool {
		for _, p := range list {
			if p == projectID || p == name || "proj:"+p == projectID {
				return true
			}
		}
		return false
	}
	if projectID != "" && match(r.ExcludeProjects) {
		return false
	}
	if len(r.IncludeProjects) > 0 {
		return projectID != "" && match(r.IncludeProjects)
	}
	return true
}

// SeverityFromString maps event severities onto AlertSeverity.
func SeverityFromString(s string) asterismv1.AlertSeverity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "error":
		return sevCritical
	case "warning", "warn":
		return sevWarning
	default:
		return sevInfo
	}
}

// SeverityString is the inverse of SeverityFromString.
func SeverityString(s asterismv1.AlertSeverity) string {
	switch s {
	case sevCritical:
		return "critical"
	case sevWarning:
		return "warning"
	default:
		return "info"
	}
}
