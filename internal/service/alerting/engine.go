package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Clock is the time source (a fake in tests).
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Store persists rules, alert state, silences, and the delivery log.
// internal/store/sqlite implements it.
type Store interface {
	LoadRules(ctx context.Context) (map[string]*asterismv1.AlertRule, error)
	SaveRule(ctx context.Context, r *asterismv1.AlertRule) error
	DeleteRule(ctx context.Context, id string) error
	LoadAlerts(ctx context.Context) ([]*asterismv1.Alert, error)
	SaveAlerts(ctx context.Context, upserts []*asterismv1.Alert, deletes []string) error
	ListSilences(ctx context.Context) ([]*asterismv1.Silence, error)
	SaveSilence(ctx context.Context, s *asterismv1.Silence) error
	PruneSilences(ctx context.Context, beforeMs int64) error
	AddDelivery(ctx context.Context, a *asterismv1.DeliveryAttempt) error
}

// Errors returned to the service layer (mapped to API codes there).
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
)

// Options configures an Engine. Zero durations take the defaults below.
type Options struct {
	Clock    Clock
	Store    Store
	Logger   *slog.Logger
	Snapshot func() Snapshot
	// Routes returns the enabled channels and their senders.
	Routes func() []Route
	// OnEvent records alert.* and notification.* events (event log + live bus).
	OnEvent func(*asterismv1.Event)
	// HubURL is server.public_url, used for links in notifications.
	HubURL string

	EvalInterval   time.Duration // 15s
	GroupWait      time.Duration // 30s (0 sends at once)
	RepeatInterval time.Duration // 4h
	// KeepFiringFor holds a firing alert this long after its condition
	// clears, so a value hovering around a threshold does not resolve and
	// re-fire every tick (default 2 x EvalInterval).
	KeepFiringFor time.Duration
	// OneShotHold resolves event-sourced alerts quietly after no new
	// matching event for this long (10m).
	OneShotHold time.Duration
	// StartupGrace suppresses resolutions right after start while
	// collectors (and agents) deliver their first data (max(60s, 2 x eval)).
	StartupGrace time.Duration
	// FlapWindow and FlapThreshold: an alert with this many state changes
	// within the window is marked flapping and its notifications are
	// suppressed until it is stable for half the window (1h, 6).
	FlapWindow    time.Duration
	FlapThreshold int
	// HistoryRetention and MaxHistory bound resolved alerts (7d, 500).
	HistoryRetention time.Duration
	MaxHistory       int

	Dispatch DispatchOptions
}

func (o *Options) defaults() {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.EvalInterval <= 0 {
		o.EvalInterval = 15 * time.Second
	}
	if o.GroupWait < 0 {
		o.GroupWait = 0
	}
	if o.RepeatInterval <= 0 {
		o.RepeatInterval = 4 * time.Hour
	}
	if o.KeepFiringFor == 0 {
		o.KeepFiringFor = 2 * o.EvalInterval
	}
	if o.OneShotHold <= 0 {
		o.OneShotHold = 10 * time.Minute
	}
	if o.StartupGrace == 0 {
		o.StartupGrace = max(time.Minute, 2*o.EvalInterval)
	}
	if o.FlapWindow <= 0 {
		o.FlapWindow = time.Hour
	}
	if o.FlapThreshold <= 0 {
		o.FlapThreshold = 6
	}
	if o.HistoryRetention <= 0 {
		o.HistoryRetention = 7 * 24 * time.Hour
	}
	if o.MaxHistory <= 0 {
		o.MaxHistory = 500
	}
	if o.Snapshot == nil {
		o.Snapshot = func() Snapshot { return Snapshot{} }
	}
	if o.Routes == nil {
		o.Routes = func() []Route { return nil }
	}
	if o.OnEvent == nil {
		o.OnEvent = func(*asterismv1.Event) {}
	}
}

// Engine runs rule evaluation and the alert lifecycle.
type Engine struct {
	opts Options
	disp *Dispatcher

	mu       sync.Mutex
	started  time.Time
	rules    map[string]*asterismv1.AlertRule
	alerts   map[string]*asterismv1.Alert
	silences []*asterismv1.Silence
	restarts map[string]*restartTrack
	absent   map[string]time.Time
	flaps    map[string][]time.Time

	primed    bool
	seen      map[string]int64
	watermark int64
}

var eventSeq atomic.Uint64

// New returns an engine; call Load before Run or Tick.
func New(opts Options) *Engine {
	opts.defaults()
	e := &Engine{
		opts:     opts,
		rules:    map[string]*asterismv1.AlertRule{},
		alerts:   map[string]*asterismv1.Alert{},
		restarts: map[string]*restartTrack{},
		absent:   map[string]time.Time{},
		flaps:    map[string][]time.Time{},
		seen:     map[string]int64{},
	}
	for _, r := range DefaultRules() {
		e.rules[r.Id] = mergeRule(r, nil)
	}
	e.disp = newDispatcher(opts.Dispatch, opts.Clock, opts.Store, opts.Logger, opts.Routes, opts.OnEvent, opts.GroupWait, opts.HubURL)
	return e
}

// Dispatcher returns the notification dispatcher.
func (e *Engine) Dispatcher() *Dispatcher { return e.disp }

// Load reads rules, alert state, and silences from the store. Firing alerts
// survive a restart with their last notification time, so they are not
// re-notified.
func (e *Engine) Load(ctx context.Context) error {
	stored, err := e.opts.Store.LoadRules(ctx)
	if err != nil {
		return err
	}
	alerts, err := e.opts.Store.LoadAlerts(ctx)
	if err != nil {
		return err
	}
	silences, err := e.opts.Store.ListSilences(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, def := range DefaultRules() {
		e.rules[def.Id] = mergeRule(def, stored[def.Id])
	}
	for _, a := range alerts {
		e.alerts[a.Id] = a
	}
	e.silences = silences
	return nil
}

// Run evaluates every EvalInterval and flushes notification groups until
// ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	e.disp.Start(ctx)
	e.mu.Lock()
	e.started = e.opts.Clock.Now()
	e.mu.Unlock()
	eval := time.NewTicker(e.opts.EvalInterval)
	defer eval.Stop()
	flush := time.NewTicker(5 * time.Second)
	defer flush.Stop()
	e.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-eval.C:
			e.Tick(ctx)
		case <-flush.C:
			e.disp.Flush(ctx)
		}
	}
}

type notice struct {
	kind  string
	alert *asterismv1.Alert
}

// Tick runs one evaluation at the clock's current time.
func (e *Engine) Tick(ctx context.Context) {
	snap := e.opts.Snapshot()
	now := e.opts.Clock.Now()
	nowMs := now.UnixMilli()

	e.mu.Lock()
	if e.started.IsZero() {
		e.started = now
	}
	before := make(map[string]*asterismv1.Alert, len(e.alerts))
	for fp, a := range e.alerts {
		before[fp] = proto.Clone(a).(*asterismv1.Alert)
	}
	var notices []notice
	var events []*asterismv1.Event

	ev := evaluate(snap, e.rules, e.restarts, now)
	oneShot := e.eventConditions(snap, now)

	for fp, c := range oneShot {
		a := e.alerts[fp]
		if a == nil || a.State != asterismv1.AlertState_ALERT_STATE_FIRING {
			a = newAlert(c, nowMs)
			a.OneShot = true
			a.Labels["occurrences"] = "1"
			e.alerts[fp] = a
			events = append(events, e.fire(a, now))
			continue
		}
		e.refresh(a, c, nowMs)
		n := 1
		_, _ = fmt.Sscan(a.Labels["occurrences"], &n)
		a.Labels["occurrences"] = fmt.Sprint(n + 1)
	}

	for fp, c := range ev.conds {
		a := e.alerts[fp]
		switch {
		case a == nil || a.State == asterismv1.AlertState_ALERT_STATE_RESOLVED:
			a = newAlert(c, nowMs)
			e.alerts[fp] = a
			if c.rule.ForSeconds == 0 {
				events = append(events, e.fire(a, now))
			}
		case a.State == asterismv1.AlertState_ALERT_STATE_PENDING:
			e.refresh(a, c, nowMs)
			if nowMs-a.StartedAtMs >= int64(c.rule.ForSeconds)*1000 {
				events = append(events, e.fire(a, now))
			}
		default: // firing
			if c.severity > a.Severity {
				a.LastNotifiedMs = 0 // escalation: notify again
			}
			e.refresh(a, c, nowMs)
			delete(e.absent, fp)
		}
	}

	inGrace := now.Sub(e.started) < e.opts.StartupGrace
	var deletes []string
	for fp, a := range e.alerts {
		if _, ok := ev.conds[fp]; ok {
			continue
		}
		if _, ok := oneShot[fp]; ok {
			continue
		}
		if a.State == asterismv1.AlertState_ALERT_STATE_RESOLVED {
			continue
		}
		if a.OneShot {
			if nowMs-a.LastSeenMs >= e.opts.OneShotHold.Milliseconds() {
				a.State = asterismv1.AlertState_ALERT_STATE_RESOLVED
				a.ResolvedAtMs = nowMs
			}
			continue
		}
		rule := e.rules[a.RuleId]
		active := rule != nil && rule.Enabled
		if active && !ev.known[a.RuleId] {
			continue // no data to decide on: hold
		}
		switch a.State {
		case asterismv1.AlertState_ALERT_STATE_PENDING:
			delete(e.alerts, fp)
			deletes = append(deletes, fp)
		case asterismv1.AlertState_ALERT_STATE_FIRING:
			if active && inGrace {
				continue
			}
			since, ok := e.absent[fp]
			if !ok {
				since = now
				e.absent[fp] = now
			}
			if !active || now.Sub(since) >= e.opts.KeepFiringFor {
				delete(e.absent, fp)
				rev, n := e.resolve(a, now)
				events = append(events, rev)
				if n != nil {
					notices = append(notices, *n)
				}
			}
		}
	}

	for _, a := range e.alerts {
		a.SilencedBy = e.matchSilence(a, nowMs)
		e.updateFlapping(a, now)
		if a.State != asterismv1.AlertState_ALERT_STATE_FIRING || a.SilencedBy != "" || a.Flapping || a.OneShot && a.LastNotifiedMs > 0 {
			continue
		}
		if a.LastNotifiedMs < a.FiringAtMs || nowMs-a.LastNotifiedMs >= e.opts.RepeatInterval.Milliseconds() {
			a.LastNotifiedMs = nowMs
			notices = append(notices, notice{kind: "firing", alert: proto.Clone(a).(*asterismv1.Alert)})
		}
	}

	deletes = append(deletes, e.pruneHistory(nowMs)...)
	var upserts []*asterismv1.Alert
	for fp, a := range e.alerts {
		if b, ok := before[fp]; !ok || !proto.Equal(a, b) {
			upserts = append(upserts, proto.Clone(a).(*asterismv1.Alert))
		}
	}
	e.mu.Unlock()

	if err := e.opts.Store.SaveAlerts(ctx, upserts, deletes); err != nil {
		e.opts.Logger.Warn("alerts: save state", "err", err)
	}
	for _, ev := range events {
		if ev != nil {
			e.opts.OnEvent(ev)
		}
	}
	for _, n := range notices {
		e.disp.Enqueue(n.kind, n.alert)
	}
	e.disp.Flush(ctx)
}

func newAlert(c condition, nowMs int64) *asterismv1.Alert {
	labels := map[string]string{}
	for k, v := range c.labels {
		labels[k] = v
	}
	return &asterismv1.Alert{
		Id: c.fp, RuleId: c.rule.Id, RuleName: c.rule.Name, Severity: c.severity,
		State:    asterismv1.AlertState_ALERT_STATE_PENDING,
		EntityId: c.entityID, EntityName: c.entityName, ProjectId: c.projectID,
		Summary: c.summary, Value: c.value, Labels: labels,
		StartedAtMs: nowMs, LastSeenMs: nowMs,
	}
}

func (e *Engine) refresh(a *asterismv1.Alert, c condition, nowMs int64) {
	a.Severity = c.severity
	a.Summary = c.summary
	a.Value = c.value
	a.EntityName = c.entityName
	a.LastSeenMs = nowMs
	a.RuleName = c.rule.Name
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	for k, v := range c.labels {
		a.Labels[k] = v
	}
}

func (e *Engine) fire(a *asterismv1.Alert, now time.Time) *asterismv1.Event {
	a.State = asterismv1.AlertState_ALERT_STATE_FIRING
	a.FiringAtMs = now.UnixMilli()
	a.ResolvedAtMs = 0
	e.flaps[a.Id] = append(e.flaps[a.Id], now)
	return alertEvent(a, "alert.firing", SeverityString(a.Severity), fmt.Sprintf("%s: %s", a.RuleName, a.Summary), now)
}

func (e *Engine) resolve(a *asterismv1.Alert, now time.Time) (*asterismv1.Event, *notice) {
	notified := a.LastNotifiedMs >= a.FiringAtMs && a.LastNotifiedMs > 0
	a.State = asterismv1.AlertState_ALERT_STATE_RESOLVED
	a.ResolvedAtMs = now.UnixMilli()
	e.flaps[a.Id] = append(e.flaps[a.Id], now)
	silenced := e.matchSilence(a, now.UnixMilli()) != ""
	ev := alertEvent(a, "alert.resolved", "info", fmt.Sprintf("Resolved: %s: %s", a.RuleName, a.Summary), now)
	if !notified || silenced || a.Flapping {
		return ev, nil
	}
	return ev, &notice{kind: "resolved", alert: proto.Clone(a).(*asterismv1.Alert)}
}

func alertEvent(a *asterismv1.Alert, kind, sev, msg string, now time.Time) *asterismv1.Event {
	attrs := map[string]string{"alert_id": a.Id, "rule": a.RuleId}
	if a.ProjectId != "" {
		attrs["project_id"] = a.ProjectId
	}
	return &asterismv1.Event{
		Id:         fmt.Sprintf("alert:%d-%d", now.UnixMilli(), eventSeq.Add(1)),
		TsMs:       now.UnixMilli(),
		EntityId:   a.EntityId,
		EntityName: a.EntityName,
		Kind:       kind,
		Severity:   sev,
		Message:    msg,
		Attrs:      attrs,
	}
}

func (e *Engine) updateFlapping(a *asterismv1.Alert, now time.Time) {
	ts := e.flaps[a.Id]
	kept := ts[:0]
	for _, t := range ts {
		if now.Sub(t) <= e.opts.FlapWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(e.flaps, a.Id)
	} else {
		e.flaps[a.Id] = kept
	}
	switch {
	case a.Flapping && (len(kept) == 0 || now.Sub(kept[len(kept)-1]) >= e.opts.FlapWindow/2):
		// Stable for half the window: start counting afresh.
		a.Flapping = false
		kept = nil
		delete(e.flaps, a.Id)
	case len(kept) >= e.opts.FlapThreshold:
		a.Flapping = true
	}
	a.Transitions = uint32(len(kept))
}

// eventConditions turns events that arrived since the last tick into
// one-shot conditions. The first tick only primes the cursor, so restarts
// never replay old events.
func (e *Engine) eventConditions(snap Snapshot, now time.Time) map[string]condition {
	nowMs := now.UnixMilli()
	out := map[string]condition{}
	projects := map[string]string{}
	for _, c := range snap.Containers {
		projects[c.Id] = c.ProjectId
	}
	for _, w := range snap.Workloads {
		if _, ok := projects[w.Id]; !ok {
			projects[w.Id] = w.ProjectId
		}
	}
	projectOf := func(id string) string { return projects[id] }
	const slack = 5 * 60 * 1000
	for _, ev := range snap.Events {
		if ev.GetId() == "" {
			continue
		}
		if _, ok := e.seen[ev.Id]; ok {
			continue
		}
		if !e.primed || ev.TsMs < e.watermark-slack {
			e.seen[ev.Id] = ev.TsMs
			continue
		}
		e.seen[ev.Id] = ev.TsMs
		if c, ok := eventCondition(ev, e.rules, projectOf); ok {
			out[c.fp] = c
		}
	}
	e.primed = true
	e.watermark = nowMs
	for id, ts := range e.seen {
		if ts < nowMs-2*slack && len(e.seen) > 4096 {
			delete(e.seen, id)
		}
	}
	return out
}

func (e *Engine) matchSilence(a *asterismv1.Alert, nowMs int64) string {
	for _, s := range e.silences {
		if s.EndsAtMs <= nowMs {
			continue
		}
		if s.RuleId != "" && s.RuleId != a.RuleId {
			continue
		}
		if s.EntityId != "" && s.EntityId != a.EntityId {
			continue
		}
		if s.ProjectId != "" && s.ProjectId != a.ProjectId && "proj:"+s.ProjectId != a.ProjectId {
			continue
		}
		return s.Id
	}
	return ""
}

func (e *Engine) pruneHistory(nowMs int64) []string {
	var resolved []*asterismv1.Alert
	var deletes []string
	for fp, a := range e.alerts {
		if a.State != asterismv1.AlertState_ALERT_STATE_RESOLVED {
			continue
		}
		if nowMs-a.ResolvedAtMs > e.opts.HistoryRetention.Milliseconds() {
			delete(e.alerts, fp)
			deletes = append(deletes, fp)
			continue
		}
		resolved = append(resolved, a)
	}
	if len(resolved) > e.opts.MaxHistory {
		sort.Slice(resolved, func(i, j int) bool { return resolved[i].ResolvedAtMs > resolved[j].ResolvedAtMs })
		for _, a := range resolved[e.opts.MaxHistory:] {
			delete(e.alerts, a.Id)
			deletes = append(deletes, a.Id)
		}
	}
	return deletes
}

// ---------------------------------------------------------------------------
// Read and admin operations (authorisation and audit live in the service)
// ---------------------------------------------------------------------------

// Alerts returns copies of every alert (active and recent history).
func (e *Engine) Alerts() []*asterismv1.Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*asterismv1.Alert, 0, len(e.alerts))
	for _, a := range e.alerts {
		out = append(out, proto.Clone(a).(*asterismv1.Alert))
	}
	return out
}

// FiringCount counts firing alerts that are not silenced.
func (e *Engine) FiringCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, a := range e.alerts {
		if a.State == asterismv1.AlertState_ALERT_STATE_FIRING && a.SilencedBy == "" {
			n++
		}
	}
	return n
}

// Rules returns the effective rules in catalogue order.
func (e *Engine) Rules() []*asterismv1.AlertRule {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*asterismv1.AlertRule, 0, len(e.rules))
	for _, def := range DefaultRules() {
		out = append(out, proto.Clone(e.rules[def.Id]).(*asterismv1.AlertRule))
	}
	return out
}

// Rule returns one effective rule.
func (e *Engine) Rule(id string) (*asterismv1.AlertRule, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.rules[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(r).(*asterismv1.AlertRule), true
}

// UpdateRule validates and stores new settings for a rule.
func (e *Engine) UpdateRule(ctx context.Context, in *asterismv1.AlertRule) (*asterismv1.AlertRule, error) {
	r, err := ValidateRule(in)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if err := e.opts.Store.SaveRule(ctx, r); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.rules[r.Id] = r
	e.mu.Unlock()
	return proto.Clone(r).(*asterismv1.AlertRule), nil
}

// ResetRule restores a rule's defaults.
func (e *Engine) ResetRule(ctx context.Context, id string) (*asterismv1.AlertRule, error) {
	def := defaultRule(id)
	if def == nil {
		return nil, fmt.Errorf("%w: unknown rule %q", ErrNotFound, id)
	}
	if err := e.opts.Store.DeleteRule(ctx, id); err != nil {
		return nil, err
	}
	r := mergeRule(def, nil)
	e.mu.Lock()
	e.rules[id] = r
	e.mu.Unlock()
	return proto.Clone(r).(*asterismv1.AlertRule), nil
}

// Silences returns active silences, plus those ended in the last 7 days
// when includeExpired is set, newest end first.
func (e *Engine) Silences(includeExpired bool) []*asterismv1.Silence {
	nowMs := e.opts.Clock.Now().UnixMilli()
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*asterismv1.Silence
	for _, s := range e.silences {
		c := proto.Clone(s).(*asterismv1.Silence)
		c.Active = c.EndsAtMs > nowMs
		if !c.Active && (!includeExpired || nowMs-c.EndsAtMs > 7*24*3600*1000) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].EndsAtMs > out[j].EndsAtMs
	})
	return out
}

// Silence durations.
const (
	MinSilence = time.Minute
	MaxSilence = 30 * 24 * time.Hour
)

// CreateSilence validates and stores a silence. createdBy is the actor.
func (e *Engine) CreateSilence(ctx context.Context, in *asterismv1.CreateSilenceRequest, createdBy string) (*asterismv1.Silence, error) {
	rule, entity, project := strings.TrimSpace(in.GetRuleId()), strings.TrimSpace(in.GetEntityId()), strings.TrimSpace(in.GetProjectId())
	if rule == "" && entity == "" && project == "" {
		return nil, fmt.Errorf("%w: a silence needs a rule, an entity, or a project", ErrInvalid)
	}
	if rule != "" && defaultRule(rule) == nil {
		return nil, fmt.Errorf("%w: unknown rule %q", ErrInvalid, rule)
	}
	for _, v := range []string{entity, project} {
		if len(v) > 200 || strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%w: invalid matcher %q", ErrInvalid, v)
		}
	}
	d := time.Duration(in.GetDurationSeconds()) * time.Second
	if d < MinSilence || d > MaxSilence {
		return nil, fmt.Errorf("%w: duration must be between 1 minute and 30 days", ErrInvalid)
	}
	reason := strings.TrimSpace(in.GetReason())
	if reason == "" || len(reason) > 500 {
		return nil, fmt.Errorf("%w: a reason (up to 500 characters) is required", ErrInvalid)
	}
	now := e.opts.Clock.Now()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	s := &asterismv1.Silence{
		Id: "sil_" + hex.EncodeToString(b), RuleId: rule, EntityId: entity, ProjectId: project,
		Reason: reason, CreatedBy: createdBy, CreatedAtMs: now.UnixMilli(), EndsAtMs: now.Add(d).UnixMilli(), Active: true,
	}
	if err := e.opts.Store.SaveSilence(ctx, s); err != nil {
		return nil, err
	}
	_ = e.opts.Store.PruneSilences(ctx, now.Add(-7*24*time.Hour).UnixMilli())
	e.mu.Lock()
	e.silences = slices.DeleteFunc(e.silences, func(x *asterismv1.Silence) bool { return x.EndsAtMs < now.Add(-7*24*time.Hour).UnixMilli() })
	e.silences = append(e.silences, s)
	for _, a := range e.alerts {
		a.SilencedBy = e.matchSilence(a, now.UnixMilli())
	}
	e.mu.Unlock()
	return proto.Clone(s).(*asterismv1.Silence), nil
}

// ExpireSilence ends a silence now.
func (e *Engine) ExpireSilence(ctx context.Context, id string) (*asterismv1.Silence, error) {
	now := e.opts.Clock.Now().UnixMilli()
	e.mu.Lock()
	var target *asterismv1.Silence
	for _, s := range e.silences {
		if s.Id == id {
			target = s
		}
	}
	if target == nil {
		e.mu.Unlock()
		return nil, fmt.Errorf("%w: unknown silence %q", ErrNotFound, id)
	}
	if target.EndsAtMs > now {
		target.EndsAtMs = now
	}
	target.Active = false
	saved := proto.Clone(target).(*asterismv1.Silence)
	for _, a := range e.alerts {
		a.SilencedBy = e.matchSilence(a, now)
	}
	e.mu.Unlock()
	if err := e.opts.Store.SaveSilence(ctx, saved); err != nil {
		return nil, err
	}
	return saved, nil
}
