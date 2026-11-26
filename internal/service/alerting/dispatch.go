package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/notify"
)

// Route is one enabled channel and its sender.
type Route struct {
	Channel *asterismv1.NotificationChannel
	Sender  notify.Sender
	// Secrets are redacted from any error text before it is logged.
	Secrets []string
}

// DispatchOptions tunes delivery. Zero values take the defaults.
type DispatchOptions struct {
	Workers     int           // 4
	QueueSize   int           // 256
	MaxAttempts int           // 4
	BackoffBase time.Duration // 2s
	BackoffMax  time.Duration // 60s
	// AttemptTimeout bounds one send (30s).
	AttemptTimeout time.Duration
	// FailureEventEvery rate-limits notification.failed events per channel (10m).
	FailureEventEvery time.Duration
	// Sleep waits between retries; it returns false when ctx ended (tests
	// replace it to run without real waits).
	Sleep func(ctx context.Context, d time.Duration) bool
}

func (o *DispatchOptions) defaults() {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 256
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 4
	}
	if o.BackoffBase <= 0 {
		o.BackoffBase = 2 * time.Second
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = time.Minute
	}
	if o.AttemptTimeout <= 0 {
		o.AttemptTimeout = 30 * time.Second
	}
	if o.FailureEventEvery <= 0 {
		o.FailureEventEvery = 10 * time.Minute
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}
	}
}

type group struct {
	kind    string
	key     string
	project string
	due     time.Time
	alerts  map[string]*asterismv1.Alert
}

type job struct {
	route Route
	msg   notify.Message
}

type bucket struct {
	rate   uint32
	tokens float64
	last   time.Time
}

// Dispatcher groups notifications, routes them to channels, and delivers
// them from a bounded queue with retries. Enqueue and Flush never block on
// network I/O.
type Dispatcher struct {
	opts      DispatchOptions
	clock     Clock
	store     Store
	log       *slog.Logger
	routes    func() []Route
	onEvent   func(*asterismv1.Event)
	groupWait time.Duration
	hubURL    string

	queue chan job

	mu          sync.Mutex
	groups      map[string]*group
	buckets     map[string]*bucket
	lastFailEvt map[string]time.Time
	started     bool
}

func newDispatcher(opts DispatchOptions, clock Clock, store Store, log *slog.Logger, routes func() []Route, onEvent func(*asterismv1.Event), groupWait time.Duration, hubURL string) *Dispatcher {
	opts.defaults()
	return &Dispatcher{
		opts: opts, clock: clock, store: store, log: log, routes: routes, onEvent: onEvent,
		groupWait: groupWait, hubURL: hubURL,
		queue:       make(chan job, opts.QueueSize),
		groups:      map[string]*group{},
		buckets:     map[string]*bucket{},
		lastFailEvt: map[string]time.Time{},
	}
}

// Start launches the delivery workers (idempotent).
func (d *Dispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started {
		return
	}
	d.started = true
	for range d.opts.Workers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-d.queue:
					d.deliver(ctx, j)
				}
			}
		}()
	}
}

// groupKey is the project id, or "host" for host-level alerts.
func groupKey(a *asterismv1.Alert) string {
	if a.ProjectId != "" {
		return a.ProjectId
	}
	return "host"
}

// Enqueue adds an alert to its notification group (kind "firing" or
// "resolved"). The group is sent group_wait after its first alert.
func (d *Dispatcher) Enqueue(kind string, a *asterismv1.Alert) {
	now := d.clock.Now()
	key := kind + "|" + groupKey(a)
	d.mu.Lock()
	g := d.groups[key]
	if g == nil {
		g = &group{kind: kind, key: groupKey(a), project: strings.TrimPrefix(a.ProjectId, "proj:"), due: now.Add(d.groupWait), alerts: map[string]*asterismv1.Alert{}}
		d.groups[key] = g
	}
	g.alerts[a.Id] = proto.Clone(a).(*asterismv1.Alert)
	// A resolve cancels a pending firing notice for the same alert (and vice
	// versa) so a short blip inside group_wait sends nothing stale.
	other := "resolved"
	if kind == "resolved" {
		other = "firing"
	}
	if og := d.groups[other+"|"+groupKey(a)]; og != nil {
		delete(og.alerts, a.Id)
		if len(og.alerts) == 0 {
			delete(d.groups, other+"|"+groupKey(a))
		}
	}
	d.mu.Unlock()
}

// Pending returns the number of grouped alerts waiting for group_wait.
func (d *Dispatcher) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, g := range d.groups {
		n += len(g.alerts)
	}
	return n
}

// Flush routes every group whose group_wait has elapsed.
func (d *Dispatcher) Flush(ctx context.Context) {
	now := d.clock.Now()
	d.mu.Lock()
	var due []*group
	for k, g := range d.groups {
		if !now.Before(g.due) {
			due = append(due, g)
			delete(d.groups, k)
		}
	}
	d.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].kind+due[i].key < due[j].kind+due[j].key })
	for _, g := range due {
		d.route(ctx, g, now)
	}
}

func newDeliveryID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Notice converts an alert for a notification.
func (d *Dispatcher) notice(a *asterismv1.Alert) notify.Notice {
	n := notify.Notice{
		ID: a.Id, RuleID: a.RuleId, RuleName: a.RuleName, Severity: a.Severity, State: a.State,
		EntityID: a.EntityId, EntityName: a.EntityName, ProjectID: a.ProjectId, Summary: a.Summary, Value: a.Value,
		StartsAt: msTime(a.StartedAtMs), FiringAt: msTime(a.FiringAtMs), EndsAt: msTime(a.ResolvedAtMs), Labels: a.Labels,
	}
	if d.hubURL != "" {
		n.URL = d.hubURL + "/alerts?alert=" + url.QueryEscape(a.Id)
	}
	return n
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// accepts applies a channel's filters to one alert.
func accepts(ch *asterismv1.NotificationChannel, kind string, a *asterismv1.Alert) bool {
	if kind == "resolved" && !ch.SendResolved {
		return false
	}
	minSev := ch.MinSeverity
	if minSev == asterismv1.AlertSeverity_ALERT_SEVERITY_UNSPECIFIED {
		minSev = sevWarning
	}
	if a.Severity < minSev {
		return false
	}
	if len(ch.Kinds) > 0 && !slices.Contains(ch.Kinds, a.RuleId) && !matchKind(ch.Kinds, a.Labels["event_kind"]) {
		return false
	}
	if len(ch.Projects) > 0 {
		name := strings.TrimPrefix(a.ProjectId, "proj:")
		if a.ProjectId == "" || (!slices.Contains(ch.Projects, a.ProjectId) && !slices.Contains(ch.Projects, name)) {
			return false
		}
	}
	return true
}

func (d *Dispatcher) route(ctx context.Context, g *group, now time.Time) {
	ids := make([]string, 0, len(g.alerts))
	for id := range g.alerts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := g.alerts[ids[i]], g.alerts[ids[j]]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		return a.Id < b.Id
	})
	for _, r := range d.routes() {
		ch := r.Channel
		if !ch.Enabled {
			continue
		}
		msg := notify.Message{Kind: g.kind, GroupKey: g.key, Project: g.project, HubURL: d.hubURL, SentAt: now, DeliveryID: newDeliveryID()}
		for _, id := range ids {
			if a := g.alerts[id]; accepts(ch, g.kind, a) {
				msg.Alerts = append(msg.Alerts, d.notice(a))
			}
		}
		if len(msg.Alerts) == 0 {
			continue
		}
		if !d.allow(ch, now) {
			d.record(ctx, ch.Id, msg, "rate_limited", 0, notify.Result{}, fmt.Errorf("rate limit of %d per hour reached; notification dropped", ch.RateLimitPerHour), 0, nil)
			continue
		}
		select {
		case d.queue <- job{route: r, msg: msg}:
		default:
			d.record(ctx, ch.Id, msg, "dropped", 0, notify.Result{}, fmt.Errorf("delivery queue full (%d); notification dropped", cap(d.queue)), 0, nil)
		}
	}
}

// allow takes one token from the channel's hourly bucket.
func (d *Dispatcher) allow(ch *asterismv1.NotificationChannel, now time.Time) bool {
	if ch.RateLimitPerHour == 0 {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	b := d.buckets[ch.Id]
	if b == nil || b.rate != ch.RateLimitPerHour {
		b = &bucket{rate: ch.RateLimitPerHour, tokens: float64(ch.RateLimitPerHour), last: now}
		d.buckets[ch.Id] = b
	}
	b.tokens = min(float64(b.rate), b.tokens+now.Sub(b.last).Hours()*float64(b.rate))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// backoff returns a full-jitter exponential delay for attempt (1-based),
// at least retryAfter.
func (d *Dispatcher) backoff(attempt int, retryAfter time.Duration) time.Duration {
	ceil := d.opts.BackoffBase << (attempt - 1)
	if ceil > d.opts.BackoffMax || ceil <= 0 {
		ceil = d.opts.BackoffMax
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(ceil)))
	delay := ceil / 2
	if err == nil {
		delay = time.Duration(n.Int64())
	}
	return max(delay, retryAfter, 100*time.Millisecond)
}

// deliver runs one job with retries.
func (d *Dispatcher) deliver(ctx context.Context, j job) {
	for attempt := 1; attempt <= d.opts.MaxAttempts; attempt++ {
		start := d.clock.Now()
		actx, cancel := context.WithTimeout(ctx, d.opts.AttemptTimeout)
		res := j.route.Sender.Send(actx, j.msg)
		cancel()
		dur := time.Since(start)
		if res.OK() {
			d.record(ctx, j.route.Channel.Id, j.msg, "sent", attempt, res, nil, dur, j.route.Secrets)
			return
		}
		last := attempt == d.opts.MaxAttempts || !res.Retryable || ctx.Err() != nil
		status := "retrying"
		if last {
			status = "failed"
		}
		d.record(ctx, j.route.Channel.Id, j.msg, status, attempt, res, res.Err, dur, j.route.Secrets)
		if last {
			d.failed(j.route.Channel, res.Err, j.route.Secrets)
			return
		}
		if !d.opts.Sleep(ctx, d.backoff(attempt, res.RetryAfter)) {
			return
		}
	}
}

// SendNow delivers one message synchronously with a single attempt (the
// channel test button) and returns the logged attempt.
func (d *Dispatcher) SendNow(ctx context.Context, r Route, msg notify.Message) *asterismv1.DeliveryAttempt {
	start := d.clock.Now()
	actx, cancel := context.WithTimeout(ctx, d.opts.AttemptTimeout)
	res := r.Sender.Send(actx, msg)
	cancel()
	status := "sent"
	if !res.OK() {
		status = "failed"
	}
	return d.record(ctx, r.Channel.Id, msg, status, 1, res, res.Err, time.Since(start), r.Secrets)
}

func (d *Dispatcher) record(ctx context.Context, channelID string, msg notify.Message, status string, attempt int, res notify.Result, err error, dur time.Duration, secrets []string) *asterismv1.DeliveryAttempt {
	a := &asterismv1.DeliveryAttempt{
		ChannelId:    channelID,
		TsMs:         d.clock.Now().UnixMilli(),
		Status:       status,
		Notification: msg.Kind,
		Subject:      msg.Subject(),
		AlertIds:     msg.AlertIDs(),
		Attempt:      uint32(attempt),
		HttpStatus:   uint32(max(res.HTTPStatus, 0)),
		DurationMs:   uint64(max(dur.Milliseconds(), 0)),
	}
	if err != nil {
		a.Error = notify.Redact(err.Error(), secrets...)
	}
	if serr := d.store.AddDelivery(context.WithoutCancel(ctx), a); serr != nil {
		d.log.Warn("alerts: delivery log", "err", serr)
	}
	if status != "sent" {
		d.log.Warn("notification delivery", "channel", channelID, "status", status, "attempt", attempt, "http_status", a.HttpStatus, "error", a.Error)
	}
	return a
}

// failed raises a notification.failed event, at most once per channel per
// FailureEventEvery.
func (d *Dispatcher) failed(ch *asterismv1.NotificationChannel, err error, secrets []string) {
	now := d.clock.Now()
	d.mu.Lock()
	last, seen := d.lastFailEvt[ch.Id]
	if seen && now.Sub(last) < d.opts.FailureEventEvery {
		d.mu.Unlock()
		return
	}
	d.lastFailEvt[ch.Id] = now
	d.mu.Unlock()
	msg := "unknown error"
	if err != nil {
		msg = notify.Redact(err.Error(), secrets...)
	}
	d.onEvent(&asterismv1.Event{
		Id:         fmt.Sprintf("notify:%d-%d", now.UnixMilli(), eventSeq.Add(1)),
		TsMs:       now.UnixMilli(),
		EntityId:   "channel:" + ch.Id,
		EntityName: ch.Name,
		Kind:       "notification.failed",
		Severity:   "warning",
		Message:    fmt.Sprintf("Notification channel %s (%s) failed: %s", ch.Name, notify.TypeName(ch.Type), msg),
		Attrs:      map[string]string{"channel_id": ch.Id, "channel_type": notify.TypeName(ch.Type)},
	})
}
