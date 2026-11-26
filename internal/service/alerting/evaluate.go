package alerting

import (
	"fmt"
	"sort"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Snapshot is the state rules are evaluated against. The service builds it
// from the same providers that serve the UI, so every collector mode works.
type Snapshot struct {
	// DockerAvailable is false when Docker inventory is not collected; alerts
	// of container rules are then held rather than resolved.
	DockerAvailable bool
	Containers      []*asterismv1.Container
	// Host is nil when host metrics are not available yet.
	Host *asterismv1.HostInfo
	// Filesystems per mount (agent mode); when empty the root disk from
	// Host is used.
	Filesystems []*asterismv1.FilesystemUsage
	Workloads   []*asterismv1.Workload
	// AgentMode reports collect.mode = "agent"; Agents lists enrolled agents.
	AgentMode bool
	Agents    []*asterismv1.AgentStatus
	// Events is the retained event log (collector and service events).
	Events []*asterismv1.Event
}

// condition is one active rule match.
type condition struct {
	fp         string
	rule       *asterismv1.AlertRule
	severity   asterismv1.AlertSeverity
	entityID   string
	entityName string
	projectID  string
	summary    string
	value      float64
	labels     map[string]string
}

// Fingerprint builds the dedup key: rule + entity [+ key].
func Fingerprint(rule, entity, key string) string {
	if key == "" {
		return rule + "|" + entity
	}
	return rule + "|" + entity + "|" + key
}

// thresholdSeverity returns the level crossed (0 = none).
func thresholdSeverity(r *asterismv1.AlertRule, pct float64) asterismv1.AlertSeverity {
	switch {
	case r.CriticalThreshold > 0 && pct >= r.CriticalThreshold:
		return sevCritical
	case r.WarningThreshold > 0 && pct >= r.WarningThreshold:
		return sevWarning
	}
	return asterismv1.AlertSeverity_ALERT_SEVERITY_UNSPECIFIED
}

func thresholdFor(r *asterismv1.AlertRule, sev asterismv1.AlertSeverity) float64 {
	if sev == sevCritical {
		return r.CriticalThreshold
	}
	return r.WarningThreshold
}

// restartTrack remembers restart-count increases for one container.
type restartTrack struct {
	last  uint32
	times []time.Time
}

// evaluation holds the conditions of one tick and which rules had data to
// decide on (alerts of other rules are left untouched).
type evaluation struct {
	conds map[string]condition
	known map[string]bool
}

func (e *evaluation) add(c condition) {
	if prev, ok := e.conds[c.fp]; ok && prev.severity >= c.severity {
		return
	}
	e.conds[c.fp] = c
}

// evaluate computes the active conditions for every enabled state rule.
// restarts is updated in place (restart loop history).
func evaluate(snap Snapshot, rules map[string]*asterismv1.AlertRule, restarts map[string]*restartTrack, now time.Time) evaluation {
	ev := evaluation{conds: map[string]condition{}, known: map[string]bool{}}
	enabled := func(id string) *asterismv1.AlertRule {
		if r := rules[id]; r != nil && r.Enabled {
			return r
		}
		return nil
	}

	if snap.DockerAvailable {
		for _, id := range []string{RuleContainerExited, RuleRestartLoop, RuleUnhealthy, RuleOOMKill, RuleContainerMemory} {
			ev.known[id] = true
		}
		stoppedAt := actionStops(snap.Events)
		seen := map[string]bool{}
		for _, c := range snap.Containers {
			seen[c.Id] = true
			evalContainer(&ev, c, enabled, restarts, stoppedAt, now)
		}
		for id := range restarts {
			if !seen[id] {
				delete(restarts, id)
			}
		}
	}

	if snap.Host != nil {
		ev.known[RuleHostCPU], ev.known[RuleHostMemory], ev.known[RuleDiskUsage] = true, true, true
		evalHost(&ev, snap, enabled)
	}

	if snap.AgentMode {
		ev.known[RuleAgentOffline] = true
		if r := enabled(RuleAgentOffline); r != nil {
			for _, a := range snap.Agents {
				if a.State != "offline" {
					continue
				}
				entity := "agent:" + a.Name
				last := "never"
				if a.LastSeenMs > 0 {
					last = time.UnixMilli(a.LastSeenMs).UTC().Format(time.RFC3339)
				}
				ev.add(condition{fp: Fingerprint(r.Id, entity, ""), rule: r, severity: r.Severity, entityID: entity, entityName: a.Name,
					summary: fmt.Sprintf("Agent %s is offline (last seen %s)", a.Name, last), labels: map[string]string{"agent": a.Name}})
			}
		}
	}

	ev.known[RuleUnitFailed] = len(snap.Workloads) > 0
	if r := enabled(RuleUnitFailed); r != nil {
		for _, w := range snap.Workloads {
			if w.Kind != asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT || !strings.Contains(strings.ToLower(w.State), "failed") || !projectAllowed(r, w.ProjectId) {
				continue
			}
			ev.add(condition{fp: Fingerprint(r.Id, w.Id, ""), rule: r, severity: r.Severity, entityID: w.Id, entityName: w.Name, projectID: w.ProjectId,
				summary: fmt.Sprintf("systemd unit %s failed", w.Name), labels: map[string]string{"state": w.State}})
		}
	}
	return ev
}

// actionStops maps container entity ids to the time of the newest
// successful Asterism stop or restart action.
func actionStops(events []*asterismv1.Event) map[string]int64 {
	out := map[string]int64{}
	for _, e := range events {
		if e.Kind != "audit.container.stop" && e.Kind != "audit.container.restart" {
			continue
		}
		if e.GetAudit().GetResult() != "success" {
			continue
		}
		if e.TsMs > out[e.EntityId] {
			out[e.EntityId] = e.TsMs
		}
	}
	return out
}

func evalContainer(ev *evaluation, c *asterismv1.Container, enabled func(string) *asterismv1.AlertRule, restarts map[string]*restartTrack, stoppedAt map[string]int64, now time.Time) {
	labels := func(kv ...string) map[string]string {
		m := map[string]string{}
		if c.Image != "" {
			m["image"] = c.Image
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	stopped := c.State == asterismv1.ContainerState_CONTAINER_STATE_EXITED || c.State == asterismv1.ContainerState_CONTAINER_STATE_DEAD
	base := func(r *asterismv1.AlertRule, sev asterismv1.AlertSeverity, summary string, value float64, l map[string]string) condition {
		return condition{fp: Fingerprint(r.Id, c.Id, ""), rule: r, severity: sev, entityID: c.Id, entityName: c.Name, projectID: c.ProjectId, summary: summary, value: value, labels: l}
	}

	// Restart history is tracked even while the rule is disabled so that
	// enabling it sees recent restarts.
	tr := restarts[c.Id]
	if tr == nil {
		tr = &restartTrack{last: c.RestartCount}
		restarts[c.Id] = tr
	}
	if c.RestartCount > tr.last {
		for range min(c.RestartCount-tr.last, 100) {
			tr.times = append(tr.times, now)
		}
	}
	tr.last = c.RestartCount

	if r := enabled(RuleRestartLoop); r != nil && projectAllowed(r, c.ProjectId) {
		window := time.Duration(r.WindowSeconds) * time.Second
		kept := tr.times[:0]
		for _, t := range tr.times {
			if now.Sub(t) <= window {
				kept = append(kept, t)
			}
		}
		tr.times = kept
		n := uint32(len(kept))
		restarting := c.State == asterismv1.ContainerState_CONTAINER_STATE_RESTARTING
		if n >= r.Count || restarting {
			msg := fmt.Sprintf("Container %s restarted %d times in %s", c.Name, n, window)
			if restarting && n < r.Count {
				msg = fmt.Sprintf("Container %s is in a restart loop (%d restarts in total)", c.Name, c.RestartCount)
			}
			ev.add(base(r, r.Severity, msg, float64(n), labels("restart_count", fmt.Sprint(c.RestartCount))))
		}
	} else if len(tr.times) > 200 {
		tr.times = tr.times[len(tr.times)-200:]
	}

	if r := enabled(RuleOOMKill); r != nil && projectAllowed(r, c.ProjectId) && c.OomKilled && c.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
		ev.add(base(r, r.Severity, fmt.Sprintf("Container %s was killed for running out of memory", c.Name), 0, labels("exit_code", fmt.Sprint(c.ExitCode))))
	}

	if r := enabled(RuleContainerExited); r != nil && projectAllowed(r, c.ProjectId) && stopped && c.ExitCode != 0 && !c.OomKilled {
		if at, ok := stoppedAt[c.Id]; !ok || at < c.StartedAtMs {
			ev.add(base(r, r.Severity, fmt.Sprintf("Container %s exited with code %d", c.Name, c.ExitCode), float64(c.ExitCode), labels("exit_code", fmt.Sprint(c.ExitCode))))
		}
	}

	if r := enabled(RuleUnhealthy); r != nil && projectAllowed(r, c.ProjectId) && c.Health == asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY {
		streak := uint32(0)
		if c.Healthcheck != nil {
			streak = c.Healthcheck.FailingStreak
		}
		ev.add(base(r, r.Severity, fmt.Sprintf("Container %s health check is failing (%d consecutive failures)", c.Name, streak), float64(streak), labels()))
	}

	if r := enabled(RuleContainerMemory); r != nil && projectAllowed(r, c.ProjectId) && c.Stats != nil && c.GetResources().GetMemLimitBytes() > 0 &&
		c.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
		limit := c.Resources.MemLimitBytes
		pct := 100 * float64(c.Stats.MemUsedBytes) / float64(limit)
		if sev := thresholdSeverity(r, pct); sev != asterismv1.AlertSeverity_ALERT_SEVERITY_UNSPECIFIED {
			ev.add(base(r, sev, fmt.Sprintf("Container %s uses %.1f%% of its %s memory limit (threshold %.0f%%)", c.Name, pct, bytesIEC(limit), thresholdFor(r, sev)), pct, labels()))
		}
	}
}

func evalHost(ev *evaluation, snap Snapshot, enabled func(string) *asterismv1.AlertRule) {
	h := snap.Host
	if r := enabled(RuleHostCPU); r != nil && projectAllowed(r, "") {
		pct := h.CpuUtil * 100
		if sev := thresholdSeverity(r, pct); sev != 0 {
			ev.add(condition{fp: Fingerprint(r.Id, "host", ""), rule: r, severity: sev, entityID: "host", entityName: hostName(h), value: pct,
				summary: fmt.Sprintf("Host CPU at %.1f%% (threshold %.0f%%)", pct, thresholdFor(r, sev))})
		}
	}
	if r := enabled(RuleHostMemory); r != nil && projectAllowed(r, "") && h.MemTotalBytes > 0 {
		pct := 100 * float64(h.MemUsedBytes) / float64(h.MemTotalBytes)
		if sev := thresholdSeverity(r, pct); sev != 0 {
			ev.add(condition{fp: Fingerprint(r.Id, "host", ""), rule: r, severity: sev, entityID: "host", entityName: hostName(h), value: pct,
				summary: fmt.Sprintf("Host memory at %.1f%% of %s (threshold %.0f%%)", pct, bytesIEC(h.MemTotalBytes), thresholdFor(r, sev))})
		}
	}
	if r := enabled(RuleDiskUsage); r != nil && projectAllowed(r, "") {
		fss := snap.Filesystems
		if len(fss) == 0 && h.DiskTotalBytes > 0 {
			fss = []*asterismv1.FilesystemUsage{{Mount: "/", TotalBytes: h.DiskTotalBytes, UsedBytes: h.DiskUsedBytes}}
		}
		sort.Slice(fss, func(i, j int) bool { return fss[i].Mount < fss[j].Mount })
		for _, fs := range fss {
			if fs.TotalBytes == 0 {
				continue
			}
			pct := 100 * float64(fs.UsedBytes) / float64(fs.TotalBytes)
			if sev := thresholdSeverity(r, pct); sev != 0 {
				ev.add(condition{fp: Fingerprint(r.Id, "host", fs.Mount), rule: r, severity: sev, entityID: "host", entityName: hostName(h), value: pct,
					summary: fmt.Sprintf("Filesystem %s is %.1f%% full (%s of %s, threshold %.0f%%)", fs.Mount, pct, bytesIEC(fs.UsedBytes), bytesIEC(fs.TotalBytes), thresholdFor(r, sev)),
					labels:  map[string]string{"mount": fs.Mount}})
			}
		}
	}
}

func hostName(h *asterismv1.HostInfo) string {
	if h.GetHostname() != "" {
		return h.GetHostname()
	}
	return "host"
}

func bytesIEC(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// eventCondition turns one new event into a one-shot alert condition, or
// returns ok=false.
func eventCondition(e *asterismv1.Event, rules map[string]*asterismv1.AlertRule, projectOf func(entityID string) string) (condition, bool) {
	if e.Kind == "unit.oom" {
		if r := rules[RuleOOMKill]; r != nil && r.Enabled && (projectOf == nil || projectAllowed(r, projectOf(e.EntityId))) {
			return condition{fp: Fingerprint(r.Id, e.EntityId, ""), rule: r, severity: r.Severity, entityID: e.EntityId, entityName: e.EntityName,
				summary: oneLine(e.Message, "systemd unit "+e.EntityName+" was OOM-killed"), labels: map[string]string{"event_kind": e.Kind}}, true
		}
		return condition{}, false
	}
	r := rules[RuleEventForward]
	if r == nil || !r.Enabled || forwardExcluded(e.Kind) {
		return condition{}, false
	}
	if len(r.EventKinds) > 0 && !matchKind(r.EventKinds, e.Kind) {
		return condition{}, false
	}
	sev := SeverityFromString(e.Severity)
	if sev < r.MinEventSeverity {
		return condition{}, false
	}
	project := e.GetAttrs()["project_id"]
	if project == "" && projectOf != nil {
		project = projectOf(e.EntityId)
	}
	if !projectAllowed(r, project) {
		return condition{}, false
	}
	return condition{fp: Fingerprint(r.Id, e.EntityId, e.Kind), rule: r, severity: sev, entityID: e.EntityId, entityName: e.EntityName, projectID: project,
		summary: oneLine(e.Message, e.Kind), labels: map[string]string{"event_kind": e.Kind, "event_id": e.Id}}, true
}

// matchKind supports exact kinds and "prefix.*" patterns.
func matchKind(list []string, kind string) bool {
	for _, k := range list {
		if k == kind || (strings.HasSuffix(k, ".*") && strings.HasPrefix(kind, strings.TrimSuffix(k, "*"))) {
			return true
		}
	}
	return false
}

func oneLine(s, fallback string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return fallback
	}
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
