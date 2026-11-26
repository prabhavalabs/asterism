package alerting

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/notify"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type memStore struct {
	mu         sync.Mutex
	rules      map[string]*asterismv1.AlertRule
	alerts     map[string]*asterismv1.Alert
	silences   map[string]*asterismv1.Silence
	deliveries []*asterismv1.DeliveryAttempt
}

func newMemStore() *memStore {
	return &memStore{rules: map[string]*asterismv1.AlertRule{}, alerts: map[string]*asterismv1.Alert{}, silences: map[string]*asterismv1.Silence{}}
}

func (s *memStore) LoadRules(context.Context) (map[string]*asterismv1.AlertRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*asterismv1.AlertRule{}
	for k, v := range s.rules {
		out[k] = proto.Clone(v).(*asterismv1.AlertRule)
	}
	return out, nil
}
func (s *memStore) SaveRule(_ context.Context, r *asterismv1.AlertRule) error {
	s.mu.Lock()
	s.rules[r.Id] = proto.Clone(r).(*asterismv1.AlertRule)
	s.mu.Unlock()
	return nil
}
func (s *memStore) DeleteRule(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.rules, id)
	s.mu.Unlock()
	return nil
}
func (s *memStore) LoadAlerts(context.Context) ([]*asterismv1.Alert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*asterismv1.Alert
	for _, a := range s.alerts {
		out = append(out, proto.Clone(a).(*asterismv1.Alert))
	}
	return out, nil
}
func (s *memStore) SaveAlerts(_ context.Context, up []*asterismv1.Alert, del []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range up {
		s.alerts[a.Id] = proto.Clone(a).(*asterismv1.Alert)
	}
	for _, id := range del {
		delete(s.alerts, id)
	}
	return nil
}
func (s *memStore) ListSilences(context.Context) ([]*asterismv1.Silence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*asterismv1.Silence
	for _, v := range s.silences {
		out = append(out, proto.Clone(v).(*asterismv1.Silence))
	}
	return out, nil
}
func (s *memStore) SaveSilence(_ context.Context, v *asterismv1.Silence) error {
	s.mu.Lock()
	s.silences[v.Id] = proto.Clone(v).(*asterismv1.Silence)
	s.mu.Unlock()
	return nil
}
func (s *memStore) PruneSilences(context.Context, int64) error { return nil }
func (s *memStore) AddDelivery(_ context.Context, a *asterismv1.DeliveryAttempt) error {
	s.mu.Lock()
	s.deliveries = append(s.deliveries, proto.Clone(a).(*asterismv1.DeliveryAttempt))
	s.mu.Unlock()
	return nil
}
func (s *memStore) statuses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, d := range s.deliveries {
		out = append(out, d.Status)
	}
	return out
}

// recorder is a Sender that records messages and can fail on demand.
type recorder struct {
	mu   sync.Mutex
	msgs []notify.Message
	fail []notify.Result // consumed in order; then success
}

func (r *recorder) Send(_ context.Context, m notify.Message) notify.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	if len(r.fail) > 0 {
		res := r.fail[0]
		r.fail = r.fail[1:]
		return res
	}
	return notify.Result{HTTPStatus: 200}
}

func (r *recorder) take() []notify.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.msgs
	r.msgs = nil
	return out
}

// harness wires an engine with a fake clock, store, snapshot, and one
// channel whose deliveries run synchronously.
type harness struct {
	t      *testing.T
	clock  *fakeClock
	store  *memStore
	eng    *Engine
	snap   Snapshot
	ch     *asterismv1.NotificationChannel
	sender *recorder
	events []*asterismv1.Event
}

func newHarness(t *testing.T, mutate func(*Options)) *harness {
	h := &harness{
		t:      t,
		clock:  &fakeClock{t: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)},
		store:  newMemStore(),
		sender: &recorder{},
		ch: &asterismv1.NotificationChannel{Id: "c1", Name: "ops", Type: asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK, Enabled: true,
			MinSeverity: asterismv1.AlertSeverity_ALERT_SEVERITY_INFO, SendResolved: true},
		snap: Snapshot{DockerAvailable: true, Host: &asterismv1.HostInfo{Hostname: "vps", MemTotalBytes: 100, MemUsedBytes: 10}},
	}
	opts := Options{
		Clock:        h.clock,
		Store:        h.store,
		Snapshot:     func() Snapshot { return h.snap },
		Routes:       func() []Route { return []Route{{Channel: h.ch, Sender: h.sender}} },
		OnEvent:      func(e *asterismv1.Event) { h.events = append(h.events, e) },
		HubURL:       "https://monitor.example.com",
		EvalInterval: 15 * time.Second,
		GroupWait:    30 * time.Second,
		StartupGrace: -1, // disabled unless a test sets it
		Dispatch:     DispatchOptions{Sleep: func(context.Context, time.Duration) bool { return true }},
	}
	if mutate != nil {
		mutate(&opts)
	}
	if opts.StartupGrace < 0 {
		opts.StartupGrace = time.Nanosecond
	}
	h.eng = New(opts)
	if err := h.eng.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h
}

// tick runs one evaluation and synchronously delivers queued jobs.
func (h *harness) tick() {
	h.eng.Tick(context.Background())
	h.drain()
}

func (h *harness) drain() {
	for {
		select {
		case j := <-h.eng.disp.queue:
			h.eng.disp.deliver(context.Background(), j)
		default:
			return
		}
	}
}

func (h *harness) step(d time.Duration) {
	h.clock.Advance(d)
	h.tick()
}

func (h *harness) alert(id string) *asterismv1.Alert {
	for _, a := range h.eng.Alerts() {
		if a.Id == id {
			return a
		}
	}
	return nil
}

func (h *harness) eventKinds() []string {
	var out []string
	for _, e := range h.events {
		out = append(out, e.Kind)
	}
	return out
}

func unhealthy(name, project string) *asterismv1.Container {
	return &asterismv1.Container{Id: "ctr:" + name, Name: name, ProjectId: project, State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING,
		Health: asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY, Healthcheck: &asterismv1.ContainerHealthcheck{FailingStreak: 3}}
}

func healthy(name, project string) *asterismv1.Container {
	return &asterismv1.Container{Id: "ctr:" + name, Name: name, ProjectId: project, State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING,
		Health: asterismv1.ContainerHealth_CONTAINER_HEALTH_HEALTHY}
}

const sFiring, sPending, sResolved = asterismv1.AlertState_ALERT_STATE_FIRING, asterismv1.AlertState_ALERT_STATE_PENDING, asterismv1.AlertState_ALERT_STATE_RESOLVED

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestLifecyclePendingFiringResolved(t *testing.T) {
	h := newHarness(t, nil)
	fp := Fingerprint(RuleUnhealthy, "ctr:api", "")
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "proj:shop")}

	h.tick()
	if a := h.alert(fp); a == nil || a.State != sPending {
		t.Fatalf("want pending, got %v", a)
	}
	h.step(45 * time.Second) // 45s < for 60s
	if h.alert(fp).State != sPending {
		t.Fatal("fired before for-duration")
	}
	h.step(15 * time.Second) // 60s
	a := h.alert(fp)
	if a.State != sFiring || a.FiringAtMs == 0 {
		t.Fatalf("want firing, got %v", a.State)
	}
	if got := h.sender.take(); len(got) != 0 {
		t.Fatalf("sent before group_wait: %d", len(got))
	}
	h.step(30 * time.Second) // group_wait elapsed
	msgs := h.sender.take()
	if len(msgs) != 1 || msgs[0].Kind != notify.KindFiring || len(msgs[0].Alerts) != 1 || msgs[0].Project != "shop" {
		t.Fatalf("firing notification = %+v", msgs)
	}
	if msgs[0].Alerts[0].URL != "https://monitor.example.com/alerts?alert="+"container_unhealthy%7Cctr%3Aapi" {
		t.Fatalf("url = %q", msgs[0].Alerts[0].URL)
	}

	// Condition clears: held for keep_firing_for (30s), then resolved.
	h.snap.Containers = []*asterismv1.Container{healthy("api", "proj:shop")}
	h.step(15 * time.Second)
	if h.alert(fp).State != sFiring {
		t.Fatal("resolved before keep_firing_for")
	}
	h.step(30 * time.Second)
	if h.alert(fp).State != sResolved {
		t.Fatalf("want resolved, got %v", h.alert(fp).State)
	}
	h.step(30 * time.Second)
	msgs = h.sender.take()
	if len(msgs) != 1 || msgs[0].Kind != notify.KindResolved || msgs[0].Status() != "resolved" {
		t.Fatalf("resolved notification = %+v", msgs)
	}
	want := []string{"alert.firing", "alert.resolved"}
	if got := h.eventKinds(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %v", got)
	}
	if h.store.alerts[fp].GetState() != sResolved {
		t.Fatal("state not persisted")
	}
}

func TestPendingClearsWithoutNotification(t *testing.T) {
	h := newHarness(t, nil)
	fp := Fingerprint(RuleUnhealthy, "ctr:api", "")
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "")}
	h.tick()
	h.snap.Containers = []*asterismv1.Container{healthy("api", "")}
	h.step(15 * time.Second)
	if h.alert(fp) != nil {
		t.Fatal("pending alert should disappear when the condition clears")
	}
	h.step(time.Minute)
	if n := len(h.sender.take()); n != 0 || len(h.events) != 0 {
		t.Fatalf("pending blip notified %d times, events %v", n, h.eventKinds())
	}
}

func TestRepeatIntervalAndEscalation(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.RepeatInterval = time.Hour; o.GroupWait = 0 })
	h.snap.Host.MemUsedBytes = 92 // warning (90)
	h.step(0)
	h.step(5 * time.Minute) // for = 300s
	fp := Fingerprint(RuleHostMemory, "host", "")
	if h.alert(fp).State != sFiring || len(h.sender.take()) != 1 {
		t.Fatal("expected one firing notification")
	}
	h.step(30 * time.Minute)
	if n := len(h.sender.take()); n != 0 {
		t.Fatalf("re-notified early: %d", n)
	}
	h.snap.Host.MemUsedBytes = 98 // escalates to critical
	h.step(15 * time.Second)
	msgs := h.sender.take()
	if len(msgs) != 1 || msgs[0].Alerts[0].Severity != asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL {
		t.Fatalf("escalation notification = %+v", msgs)
	}
	h.step(time.Hour)
	if n := len(h.sender.take()); n != 1 {
		t.Fatalf("repeat after 1h: %d", n)
	}
}

func TestGroupingByProject(t *testing.T) {
	h := newHarness(t, func(o *Options) {})
	h.eng.rules[RuleUnhealthy].ForSeconds = 0
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "proj:shop")}
	h.tick()
	h.snap.Containers = append(h.snap.Containers, unhealthy("worker", "proj:shop"), unhealthy("blog", "proj:blog"))
	h.step(10 * time.Second)
	h.step(10 * time.Second)
	if n := len(h.sender.take()); n != 0 {
		t.Fatalf("sent inside group_wait: %d", n)
	}
	h.step(10 * time.Second) // 30s after the first shop alert
	msgs := h.sender.take()
	if len(msgs) != 1 || msgs[0].Project != "shop" || len(msgs[0].Alerts) != 2 {
		t.Fatalf("shop group = %+v", msgs)
	}
	h.step(10 * time.Second)
	msgs = h.sender.take()
	if len(msgs) != 1 || msgs[0].Project != "blog" {
		t.Fatalf("blog group = %+v", msgs)
	}
}

func TestSilenceSuppressesUntilExpired(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	h.eng.rules[RuleUnhealthy].ForSeconds = 0
	sil, err := h.eng.CreateSilence(context.Background(), &asterismv1.CreateSilenceRequest{ProjectId: "shop", DurationSeconds: 600, Reason: "deploy"}, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "proj:shop")}
	h.tick()
	a := h.alert(Fingerprint(RuleUnhealthy, "ctr:api", ""))
	if a.State != sFiring || a.SilencedBy != sil.Id {
		t.Fatalf("alert = %v", a)
	}
	if n := len(h.sender.take()); n != 0 {
		t.Fatalf("silenced alert notified: %d", n)
	}
	if h.eng.FiringCount() != 0 {
		t.Fatal("silenced alerts must not count as firing")
	}
	if _, err := h.eng.ExpireSilence(context.Background(), sil.Id); err != nil {
		t.Fatal(err)
	}
	h.step(15 * time.Second)
	if msgs := h.sender.take(); len(msgs) != 1 {
		t.Fatalf("after expiry want 1 notification, got %d", len(msgs))
	}
	if got := h.eng.Silences(false); len(got) != 0 {
		t.Fatalf("expired silence listed as active: %v", got)
	}
	if got := h.eng.Silences(true); len(got) != 1 || got[0].Active {
		t.Fatalf("expired silences = %v", got)
	}
	for _, bad := range []*asterismv1.CreateSilenceRequest{
		{DurationSeconds: 600, Reason: "x"},
		{RuleId: "nope", DurationSeconds: 600, Reason: "x"},
		{RuleId: RuleDiskUsage, DurationSeconds: 10, Reason: "x"},
		{RuleId: RuleDiskUsage, DurationSeconds: 600},
	} {
		if _, err := h.eng.CreateSilence(context.Background(), bad, "a"); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateSilence(%v) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestRestartDoesNotRenotify(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	h.eng.rules[RuleUnhealthy].ForSeconds = 0
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "")}
	h.tick()
	if n := len(h.sender.take()); n != 1 {
		t.Fatalf("first notification: %d", n)
	}
	// A new engine over the same store (a hub restart).
	h2 := &harness{t: t, clock: h.clock, store: h.store, sender: &recorder{}, ch: h.ch, snap: h.snap}
	h2.eng = New(Options{Clock: h.clock, Store: h.store, Snapshot: func() Snapshot { return h2.snap },
		Routes: func() []Route { return []Route{{Channel: h2.ch, Sender: h2.sender}} }, GroupWait: 0, StartupGrace: time.Minute,
		Dispatch: DispatchOptions{Sleep: func(context.Context, time.Duration) bool { return true }}})
	if err := h2.eng.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	h2.step(15 * time.Second)
	if n := len(h2.sender.take()); n != 0 {
		t.Fatalf("restart re-notified: %d", n)
	}
	// Agent data not yet back after restart: no resolution during the grace.
	h2.snap = Snapshot{DockerAvailable: true}
	h2.step(15 * time.Second)
	h2.step(15 * time.Second)
	if a := h2.alert(Fingerprint(RuleUnhealthy, "ctr:api", "")); a.State != sFiring {
		t.Fatalf("resolved during startup grace: %v", a.State)
	}
}

func TestHoldWhenDataMissing(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	h.eng.rules[RuleUnhealthy].ForSeconds = 0
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "")}
	h.tick()
	h.snap.DockerAvailable = false
	h.snap.Containers = nil
	for range 5 {
		h.step(15 * time.Second)
	}
	if h.alert(Fingerprint(RuleUnhealthy, "ctr:api", "")).State != sFiring {
		t.Fatal("alert resolved although Docker data was unavailable")
	}
}

func TestFlapProtection(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0; o.FlapThreshold = 4; o.KeepFiringFor = -1 })
	h.eng.rules[RuleUnhealthy].ForSeconds = 0
	fp := Fingerprint(RuleUnhealthy, "ctr:api", "")
	sent := 0
	for range 4 {
		h.snap.Containers = []*asterismv1.Container{unhealthy("api", "")}
		h.step(15 * time.Second)
		h.snap.Containers = []*asterismv1.Container{healthy("api", "")}
		h.step(15 * time.Second)
		sent += len(h.sender.take())
	}
	a := h.alert(fp)
	if !a.Flapping {
		t.Fatalf("expected flapping after %d transitions", a.Transitions)
	}
	before := sent
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "")}
	h.step(15 * time.Second)
	if n := len(h.sender.take()); n != 0 {
		t.Fatalf("flapping alert notified (%d, earlier %d)", n, before)
	}
	// Stable for half the window clears flapping and notifies once.
	h.step(31 * time.Minute)
	if h.alert(fp).Flapping {
		t.Fatal("flapping not cleared after stabilising")
	}
	if n := len(h.sender.take()); n != 1 {
		t.Fatalf("after stabilising want 1 notification, got %d", n)
	}
}

func TestChannelFilters(t *testing.T) {
	info := &asterismv1.Alert{Id: "a", RuleId: RuleDiskUsage, Severity: asterismv1.AlertSeverity_ALERT_SEVERITY_INFO, ProjectId: "proj:shop"}
	crit := &asterismv1.Alert{Id: "b", RuleId: RuleOOMKill, Severity: asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL, ProjectId: "proj:shop"}
	fwd := &asterismv1.Alert{Id: "c", RuleId: RuleEventForward, Severity: asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL, Labels: map[string]string{"event_kind": "container.oom"}}
	cases := []struct {
		name string
		ch   *asterismv1.NotificationChannel
		kind string
		a    *asterismv1.Alert
		want bool
	}{
		{"default min severity is warning", &asterismv1.NotificationChannel{}, "firing", info, false},
		{"critical passes", &asterismv1.NotificationChannel{}, "firing", crit, true},
		{"resolved needs send_resolved", &asterismv1.NotificationChannel{}, "resolved", crit, false},
		{"resolved with toggle", &asterismv1.NotificationChannel{SendResolved: true}, "resolved", crit, true},
		{"kind filter by rule", &asterismv1.NotificationChannel{Kinds: []string{RuleDiskUsage}}, "firing", crit, false},
		{"kind filter by event kind", &asterismv1.NotificationChannel{Kinds: []string{"container.*"}}, "firing", fwd, true},
		{"project filter by name", &asterismv1.NotificationChannel{Projects: []string{"shop"}}, "firing", crit, true},
		{"project filter excludes host alerts", &asterismv1.NotificationChannel{Projects: []string{"shop"}}, "firing", fwd, false},
	}
	for _, c := range cases {
		if got := accepts(c.ch, c.kind, c.a); got != c.want {
			t.Errorf("%s: accepts = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRetriesBackoffAndFailureEvent(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	h.eng.rules[RuleUnhealthy].ForSeconds = 0
	h.sender.fail = []notify.Result{
		{HTTPStatus: 503, Err: errors.New("HTTP 503"), Retryable: true},
		{HTTPStatus: 503, Err: errors.New("HTTP 503"), Retryable: true},
	}
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", "")}
	h.tick()
	if got := h.store.statuses(); len(got) != 3 || got[0] != "retrying" || got[1] != "retrying" || got[2] != "sent" {
		t.Fatalf("statuses = %v", got)
	}
	h.store.deliveries = nil
	h.sender.fail = []notify.Result{{HTTPStatus: 400, Err: errors.New("HTTP 400"), Retryable: false}}
	h.snap.Containers = []*asterismv1.Container{unhealthy("api", ""), unhealthy("db", "")}
	h.step(15 * time.Second)
	if got := h.store.statuses(); len(got) != 1 || got[0] != "failed" {
		t.Fatalf("permanent failure statuses = %v", got)
	}
	found := false
	for _, e := range h.events {
		if e.Kind == "notification.failed" && e.EntityId == "channel:c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no notification.failed event: %v", h.eventKinds())
	}
	d := h.eng.disp
	for attempt := 1; attempt <= 6; attempt++ {
		if b := d.backoff(attempt, 0); b > d.opts.BackoffMax || b <= 0 {
			t.Fatalf("backoff(%d) = %s", attempt, b)
		}
	}
	if b := d.backoff(1, 90*time.Second); b < 90*time.Second {
		t.Fatalf("Retry-After ignored: %s", b)
	}
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	h.ch.RateLimitPerHour = 2
	h.eng.rules[RuleUnhealthy].ForSeconds = 0
	for i, name := range []string{"a", "b", "c"} {
		h.snap.Containers = append(h.snap.Containers, unhealthy(name, "proj:p"+name))
		h.step(time.Duration(i) * time.Second)
	}
	got := h.store.statuses()
	sort.Strings(got)
	if len(got) != 3 || got[0] != "rate_limited" || got[1] != "sent" || got[2] != "sent" {
		t.Fatalf("statuses = %v", got)
	}
}

func TestEventForwardingOneShot(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	r := h.eng.rules[RuleEventForward]
	r.Enabled = true
	r.EventKinds = []string{"container.oom", "alert.memory"}
	r.MinEventSeverity = asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING
	h.snap.Events = []*asterismv1.Event{{Id: "old", TsMs: h.clock.Now().UnixMilli(), Kind: "container.oom", Severity: "critical", EntityId: "ctr:x"}}
	h.tick() // primes: the existing event is not replayed
	if len(h.sender.take()) != 0 {
		t.Fatal("old event forwarded")
	}
	now := h.clock.Now().UnixMilli()
	h.snap.Events = append(h.snap.Events,
		&asterismv1.Event{Id: "e1", TsMs: now + 1, Kind: "container.oom", Severity: "critical", EntityId: "ctr:x", Message: "OOM"},
		&asterismv1.Event{Id: "e2", TsMs: now + 1, Kind: "container.start", Severity: "critical", EntityId: "ctr:x"},
		&asterismv1.Event{Id: "e3", TsMs: now + 1, Kind: "alert.memory", Severity: "info", EntityId: "ctr:y"},
		&asterismv1.Event{Id: "e4", TsMs: now + 1, Kind: "audit.container.stop", Severity: "warning", EntityId: "ctr:y"},
	)
	h.step(15 * time.Second)
	msgs := h.sender.take()
	if len(msgs) != 1 || msgs[0].Alerts[0].Summary != "OOM" {
		t.Fatalf("forwarded = %+v", msgs)
	}
	fp := Fingerprint(RuleEventForward, "ctr:x", "container.oom")
	h.snap.Events = append(h.snap.Events, &asterismv1.Event{Id: "e5", TsMs: now + 20_000, Kind: "container.oom", Severity: "critical", EntityId: "ctr:x"})
	h.step(15 * time.Second)
	if a := h.alert(fp); a.Labels["occurrences"] != "2" || len(h.sender.take()) != 0 {
		t.Fatalf("repeat event should update, not notify: %v", a)
	}
	h.step(11 * time.Minute)
	if h.alert(fp).State != sResolved || len(h.sender.take()) != 0 {
		t.Fatal("one-shot alert should resolve quietly after the hold")
	}
}

func TestContainerRules(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	now := h.clock.Now()
	stoppedByUs := &asterismv1.Container{Id: "ctr:manual", Name: "manual", State: asterismv1.ContainerState_CONTAINER_STATE_EXITED, ExitCode: 137, StartedAtMs: now.Add(-time.Hour).UnixMilli()}
	crashed := &asterismv1.Container{Id: "ctr:crash", Name: "crash", State: asterismv1.ContainerState_CONTAINER_STATE_EXITED, ExitCode: 1}
	oom := &asterismv1.Container{Id: "ctr:oom", Name: "oom", State: asterismv1.ContainerState_CONTAINER_STATE_EXITED, ExitCode: 137, OomKilled: true}
	clean := &asterismv1.Container{Id: "ctr:clean", Name: "clean", State: asterismv1.ContainerState_CONTAINER_STATE_EXITED, ExitCode: 0}
	looping := &asterismv1.Container{Id: "ctr:loop", Name: "loop", State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING, RestartCount: 1}
	mem := &asterismv1.Container{Id: "ctr:mem", Name: "mem", State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING,
		Resources: &asterismv1.ContainerResources{MemLimitBytes: 1000}, Stats: &asterismv1.ContainerStats{MemUsedBytes: 990}}
	h.snap.Containers = []*asterismv1.Container{stoppedByUs, crashed, oom, clean, looping, mem}
	h.snap.Events = []*asterismv1.Event{{Id: "audit1", Kind: "audit.container.stop", EntityId: "ctr:manual", TsMs: now.Add(-time.Minute).UnixMilli(),
		Audit: &asterismv1.ActionAudit{Result: "success"}}}
	h.tick()
	for _, c := range []struct {
		fp   string
		want bool
	}{
		{Fingerprint(RuleContainerExited, "ctr:crash", ""), true},
		{Fingerprint(RuleContainerExited, "ctr:manual", ""), false},
		{Fingerprint(RuleContainerExited, "ctr:oom", ""), false},
		{Fingerprint(RuleContainerExited, "ctr:clean", ""), false},
		{Fingerprint(RuleOOMKill, "ctr:oom", ""), true},
	} {
		if got := h.alert(c.fp) != nil; got != c.want {
			t.Errorf("%s present = %v, want %v", c.fp, got, c.want)
		}
	}
	// Restart loop: three restarts inside the window.
	for i := 2; i <= 4; i++ {
		looping.RestartCount = uint32(i)
		h.step(time.Minute)
	}
	if a := h.alert(Fingerprint(RuleRestartLoop, "ctr:loop", "")); a == nil || a.State != sFiring {
		t.Fatalf("restart loop = %v", a)
	}
	// Memory: 99% for 5 minutes is critical.
	h.step(5 * time.Minute)
	if a := h.alert(Fingerprint(RuleContainerMemory, "ctr:mem", "")); a == nil || a.State != sFiring || a.Severity != asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL {
		t.Fatalf("container memory = %v", a)
	}
}

func TestHostDiskAgentAndUnitRules(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.GroupWait = 0 })
	h.snap.Filesystems = []*asterismv1.FilesystemUsage{{Mount: "/", TotalBytes: 100, UsedBytes: 96}, {Mount: "/data", TotalBytes: 100, UsedBytes: 86}, {Mount: "/boot", TotalBytes: 100, UsedBytes: 10}}
	h.snap.AgentMode = true
	h.snap.Agents = []*asterismv1.AgentStatus{{Name: "vps-a", State: "offline"}, {Name: "vps-b", State: "connected"}}
	h.snap.Workloads = []*asterismv1.Workload{{Id: "unit:nginx.service", Name: "nginx.service", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT, State: "failed"}}
	h.tick()
	h.step(5 * time.Minute)
	root := h.alert(Fingerprint(RuleDiskUsage, "host", "/"))
	data := h.alert(Fingerprint(RuleDiskUsage, "host", "/data"))
	if root.GetSeverity() != asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL || data.GetSeverity() != asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING || root.Labels["mount"] != "/" {
		t.Fatalf("disk alerts = %v / %v", root, data)
	}
	if h.alert(Fingerprint(RuleDiskUsage, "host", "/boot")) != nil {
		t.Fatal("/boot below threshold alerted")
	}
	if h.alert(Fingerprint(RuleAgentOffline, "agent:vps-a", "")).GetState() != sFiring || h.alert(Fingerprint(RuleAgentOffline, "agent:vps-b", "")) != nil {
		t.Fatal("agent offline alerts wrong")
	}
	if h.alert(Fingerprint(RuleUnitFailed, "unit:nginx.service", "")).GetState() != sFiring {
		t.Fatal("unit failed alert missing")
	}
}

func TestProjectFilters(t *testing.T) {
	r := &asterismv1.AlertRule{IncludeProjects: []string{"shop"}, ExcludeProjects: []string{"proj:shop-staging"}}
	for in, want := range map[string]bool{"proj:shop": true, "proj:blog": false, "": false} {
		if got := projectAllowed(r, in); got != want {
			t.Errorf("include %q = %v", in, got)
		}
	}
	r = &asterismv1.AlertRule{ExcludeProjects: []string{"blog"}}
	if projectAllowed(r, "proj:blog") || !projectAllowed(r, "") || !projectAllowed(r, "proj:shop") {
		t.Fatal("exclude filter wrong")
	}
}

func TestRuleUpdateValidation(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if _, err := h.eng.UpdateRule(ctx, &asterismv1.AlertRule{Id: RuleDiskUsage, Enabled: true, WarningThreshold: 95, CriticalThreshold: 90}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("critical below warning: %v", err)
	}
	if _, err := h.eng.UpdateRule(ctx, &asterismv1.AlertRule{Id: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown rule: %v", err)
	}
	if _, err := h.eng.UpdateRule(ctx, &asterismv1.AlertRule{Id: RuleEventForward, Enabled: true, EventKinds: []string{"audit.container.stop"}, MinEventSeverity: asterismv1.AlertSeverity_ALERT_SEVERITY_INFO}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("audit forwarding: %v", err)
	}
	got, err := h.eng.UpdateRule(ctx, &asterismv1.AlertRule{Id: RuleDiskUsage, Enabled: true, WarningThreshold: 80, CriticalThreshold: 90, ForSeconds: 60, Name: "ignored"})
	if err != nil || got.WarningThreshold != 80 || got.Name != "Disk usage high" || got.IsDefault {
		t.Fatalf("update = %v, %v", got, err)
	}
	if h.store.rules[RuleDiskUsage].GetWarningThreshold() != 80 {
		t.Fatal("rule not stored")
	}
	reset, err := h.eng.ResetRule(ctx, RuleDiskUsage)
	if err != nil || reset.WarningThreshold != 85 || !reset.IsDefault {
		t.Fatalf("reset = %v, %v", reset, err)
	}
	// Loaded overrides survive a restart.
	_, _ = h.eng.UpdateRule(ctx, &asterismv1.AlertRule{Id: RuleHostCPU, Enabled: true, WarningThreshold: 70})
	e2 := New(Options{Store: h.store})
	if err := e2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := e2.Rule(RuleHostCPU); !r.Enabled || r.WarningThreshold != 70 {
		t.Fatalf("loaded rule = %v", r)
	}
}
