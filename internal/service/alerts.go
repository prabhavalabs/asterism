package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/notify"
	"github.com/prabhavalabs/asterism/internal/service/alerting"
	"github.com/prabhavalabs/asterism/internal/store/secretbox"
	"github.com/prabhavalabs/asterism/internal/store/sqlite"
)

// FilesystemProvider is optionally implemented by host providers that know
// per-mount usage (agent mode). Other modes alert on the root disk.
type FilesystemProvider interface {
	Filesystems() []*asterismv1.FilesystemUsage
}

// AlertsConfig wires alerting and notifications (docs/BLUEPRINT.md §6.9, §8.9).
type AlertsConfig struct {
	DB  *sqlite.DB
	Box *secretbox.Box
	// VAPID keys for Web Push; nil disables Web Push with VAPIDError as reason.
	VAPID      *notify.VAPIDKeys
	VAPIDError string
	// AllowPrivate is [alerts] allow_private_targets.
	AllowPrivate bool
	// HubURL is server.public_url ("" when unset).
	HubURL         string
	EvalInterval   time.Duration
	GroupWait      time.Duration
	RepeatInterval time.Duration
	// Clock and Dispatch override engine internals (tests).
	Clock    alerting.Clock
	Dispatch alerting.DispatchOptions
	// TelegramAPI overrides the Bot API base (tests).
	TelegramAPI string
}

type alertsState struct {
	engine       *alerting.Engine
	db           *sqlite.DB
	box          *secretbox.Box
	client       *http.Client
	allowPrivate bool
	hubURL       string
	vapid        *notify.VAPIDKeys
	vapidErr     string
	telegramAPI  string

	mu     sync.Mutex
	routes []alerting.Route
	stale  bool
}

// ErrAlertsDisabled is returned by writes when alerting is off.
const alertsDisabledMsg = "alerting is disabled: set enabled = true under [alerts] in asterism.toml (or ASTERISM_ALERTS_ENABLED=true) and restart Asterism"

// ConfigureAlerts creates the alert engine over the store and loads its
// state. Call RunAlerts to start evaluation.
func (o *Observatory) ConfigureAlerts(ctx context.Context, cfg AlertsConfig) error {
	if cfg.DB == nil || cfg.Box == nil {
		return errors.New("alerts: a database and a secret box are required")
	}
	st := &alertsState{
		db: cfg.DB, box: cfg.Box, client: notify.NewHTTPClient(cfg.AllowPrivate), allowPrivate: cfg.AllowPrivate,
		hubURL: cfg.HubURL, vapid: cfg.VAPID, vapidErr: cfg.VAPIDError, telegramAPI: cfg.TelegramAPI, stale: true,
	}
	st.engine = alerting.New(alerting.Options{
		Clock:          cfg.Clock,
		Store:          cfg.DB,
		Logger:         o.logger.With("component", "alerts"),
		Snapshot:       o.alertSnapshot,
		Routes:         func() []alerting.Route { return o.alertRoutes(context.Background()) },
		OnEvent:        o.appendEvent,
		HubURL:         cfg.HubURL,
		EvalInterval:   cfg.EvalInterval,
		GroupWait:      cfg.GroupWait,
		RepeatInterval: cfg.RepeatInterval,
		Dispatch:       cfg.Dispatch,
	})
	if err := st.engine.Load(ctx); err != nil {
		return fmt.Errorf("alerts: load state: %w", err)
	}
	o.alerts = st
	return nil
}

// RunAlerts evaluates rules until ctx ends (no-op when alerting is off).
func (o *Observatory) RunAlerts(ctx context.Context) error {
	if o.alerts == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return o.alerts.engine.Run(ctx)
}

// AlertEngine exposes the engine (tests).
func (o *Observatory) AlertEngine() *alerting.Engine {
	if o.alerts == nil {
		return nil
	}
	return o.alerts.engine
}

// appendEvent stores and publishes a service event.
func (o *Observatory) appendEvent(e *asterismv1.Event) {
	o.events.Append(e)
	o.bus.Publish(bus.Message{Events: []*asterismv1.Event{e}})
}

// alertSnapshot builds the evaluation input from the same providers that
// serve the UI, so local, ssh, agent, and showcase modes all work.
func (o *Observatory) alertSnapshot() alerting.Snapshot {
	var s alerting.Snapshot
	if o.docker != nil {
		snap := o.snapshot()
		s.DockerAvailable = snap.Engine.GetAvailable() || len(snap.Containers) > 0
		s.Containers = snap.Containers
	}
	if o.host != nil {
		if h := o.host.HostSnapshot(); h != nil && (h.MemTotalBytes > 0 || h.DiskTotalBytes > 0) {
			s.Host = h
		}
		if fp, ok := o.host.(FilesystemProvider); ok {
			s.Filesystems = fp.Filesystems()
		}
	}
	s.Workloads = o.ListWorkloads("", asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED)
	if o.mode == "agent" {
		s.AgentMode = true
		s.Agents = o.ListAgents().GetAgents()
	}
	s.Events = o.allEvents("")
	return s
}

// ---------------------------------------------------------------------------
// Authorisation and audit
// ---------------------------------------------------------------------------

// AdminRequest identifies the caller of a settings write.
type AdminRequest struct {
	Principal  auth.Principal
	RemoteAddr string
}

var adminAuditSeq uint64
var adminAuditMu sync.Mutex

// adminWrite authorises a settings write (admin browser session only; API
// tokens are refused whatever their scopes), runs fn, and records exactly
// one audit event for the attempt. area is "alerts" or "notifications".
func (o *Observatory) adminWrite(req AdminRequest, area, action, targetID, targetName string, fn func() (string, error)) error {
	start := time.Now()
	audit := &asterismv1.ActionAudit{
		Actor: req.Principal.Actor(), ActorKind: req.Principal.Kind, Action: action,
		TargetId: targetID, TargetName: targetName, RemoteAddr: req.RemoteAddr,
	}
	var err error
	switch {
	case req.Principal.Kind != "session":
		err = newErr(ErrPermissionDenied, "%s settings can only be changed from an admin browser session; API tokens (caller %s) are read-only here", area, req.Principal.Actor())
		audit.Result = "denied"
	case o.alerts == nil:
		err = newErr(ErrFailedPrecondition, alertsDisabledMsg)
		audit.Result = "denied"
	default:
		var name string
		name, err = fn()
		if name != "" {
			audit.TargetName = name
		}
		switch {
		case err == nil:
			audit.Result = "success"
		case errors.Is(err, ErrNotFound):
			audit.Result = "not_found"
		case errors.Is(err, ErrInvalidArgument):
			audit.Result = "invalid"
		default:
			audit.Result = "failure"
		}
	}
	if err != nil {
		audit.Error = err.Error()
	}
	audit.DurationMs = uint64(time.Since(start).Milliseconds())
	o.recordAdminAudit(area, audit)
	return err
}

func (o *Observatory) recordAdminAudit(area string, a *asterismv1.ActionAudit) {
	now := time.Now()
	adminAuditMu.Lock()
	adminAuditSeq++
	seq := adminAuditSeq
	adminAuditMu.Unlock()
	target := a.TargetName
	if target == "" {
		target = a.TargetId
	}
	sev, msg := "info", fmt.Sprintf("%s: %s %s", a.Actor, a.Action, target)
	if a.Result != "success" {
		sev = "warning"
		msg = fmt.Sprintf("%s could not %s %s: %s", a.Actor, a.Action, target, a.Error)
	}
	ev := &asterismv1.Event{
		Id: fmt.Sprintf("audit:%s:%d-%d", area, now.UnixMilli(), seq), TsMs: now.UnixMilli(),
		EntityId: "host", EntityName: target, Kind: "audit." + area + "." + a.Action, Severity: sev, Message: msg,
		Attrs: map[string]string{"actor": a.Actor, "action": a.Action, "result": a.Result, "target_id": a.TargetId},
		Audit: a,
	}
	o.appendEvent(ev)
	o.logger.Info("audit", "area", area, "action", a.Action, "result", a.Result, "actor", a.Actor,
		"actor_kind", a.ActorKind, "target", a.TargetId, "remote", a.RemoteAddr, "error", a.Error)
}

// mapAlertingErr converts engine errors to service error kinds.
func mapAlertingErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, alerting.ErrInvalid):
		return newErr(ErrInvalidArgument, "%s", strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, alerting.ErrNotFound):
		return newErr(ErrNotFound, "%s", strings.TrimPrefix(err.Error(), "not found: "))
	}
	return newErr(ErrInternal, "%s", err.Error())
}

// ---------------------------------------------------------------------------
// AlertsService
// ---------------------------------------------------------------------------

func alertStateRank(s asterismv1.AlertState) int {
	switch s {
	case asterismv1.AlertState_ALERT_STATE_FIRING:
		return 0
	case asterismv1.AlertState_ALERT_STATE_PENDING:
		return 1
	default:
		return 2
	}
}

var alertSorter = newSorter("alert", "state, severity desc, since desc",
	func(a *asterismv1.Alert) string { return a.Id },
	by("state", func(a *asterismv1.Alert) int { return alertStateRank(a.State) }),
	by("severity", func(a *asterismv1.Alert) asterismv1.AlertSeverity { return a.Severity }),
	by("since", func(a *asterismv1.Alert) int64 { return a.StartedAtMs }),
	by("rule", func(a *asterismv1.Alert) string { return a.RuleId }),
	byFold("entity", func(a *asterismv1.Alert) string { return a.EntityName }),
)

// ListAlerts serves AlertsService.ListAlerts.
func (o *Observatory) ListAlerts(req *asterismv1.ListAlertsRequest) (*asterismv1.ListAlertsResponse, error) {
	var all []*asterismv1.Alert
	if o.alerts != nil {
		all = o.alerts.engine.Alerts()
	}
	states := map[asterismv1.AlertState]bool{}
	for _, s := range req.GetStates() {
		if s != asterismv1.AlertState_ALERT_STATE_UNSPECIFIED {
			states[s] = true
		}
	}
	sevs := map[asterismv1.AlertSeverity]bool{}
	for _, s := range req.GetSeverities() {
		if s != asterismv1.AlertSeverity_ALERT_SEVERITY_UNSPECIFIED {
			sevs[s] = true
		}
	}
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.Alert]{
		{facet: "state", match: func(a *asterismv1.Alert) bool { return len(states) == 0 || states[a.State] }},
		{match: func(a *asterismv1.Alert) bool { return len(sevs) == 0 || sevs[a.Severity] }},
		{match: func(a *asterismv1.Alert) bool {
			return containsAny(q, a.RuleId, a.RuleName, a.EntityId, a.EntityName, a.ProjectId, a.Summary)
		}},
	}
	items, info, err := listPage(matching(all, filters, ""), alertSorter, req.GetOrderBy(), req.GetPage())
	if err != nil {
		return nil, err
	}
	counts := &asterismv1.AlertStateCounts{}
	for _, a := range matching(all, filters, "state") {
		switch a.State {
		case asterismv1.AlertState_ALERT_STATE_FIRING:
			counts.Firing++
			if a.SilencedBy != "" {
				counts.Silenced++
			}
		case asterismv1.AlertState_ALERT_STATE_PENDING:
			counts.Pending++
		case asterismv1.AlertState_ALERT_STATE_RESOLVED:
			counts.Resolved++
		}
	}
	return &asterismv1.ListAlertsResponse{Alerts: items, PageInfo: info, StateCounts: counts}, nil
}

// FiringAlertCount is the number of firing, unsilenced alerts.
func (o *Observatory) FiringAlertCount() (uint32, bool) {
	if o.alerts == nil {
		return 0, false
	}
	return uint32(o.alerts.engine.FiringCount()), true
}

// ListRules serves AlertsService.ListRules.
func (o *Observatory) ListRules() *asterismv1.ListRulesResponse {
	if o.alerts == nil {
		return &asterismv1.ListRulesResponse{Rules: alerting.DefaultRules()}
	}
	return &asterismv1.ListRulesResponse{Rules: o.alerts.engine.Rules()}
}

// UpdateRule serves AlertsService.UpdateRule.
func (o *Observatory) UpdateRule(ctx context.Context, req AdminRequest, in *asterismv1.UpdateRuleRequest) (*asterismv1.AlertRule, error) {
	id := strings.TrimSpace(in.GetRule().GetId())
	action := "rule.update"
	if in.GetReset_() {
		action = "rule.reset"
	}
	var out *asterismv1.AlertRule
	err := o.adminWrite(req, "alerts", action, "rule:"+id, id, func() (string, error) {
		var err error
		if in.GetReset_() {
			out, err = o.alerts.engine.ResetRule(ctx, id)
		} else {
			if in.GetRule() == nil {
				return "", newErr(ErrInvalidArgument, "rule is required")
			}
			out, err = o.alerts.engine.UpdateRule(ctx, in.GetRule())
		}
		if err != nil {
			return "", mapAlertingErr(err)
		}
		return out.Name, nil
	})
	return out, err
}

// ListSilences serves AlertsService.ListSilences.
func (o *Observatory) ListSilences(req *asterismv1.ListSilencesRequest) *asterismv1.ListSilencesResponse {
	if o.alerts == nil {
		return &asterismv1.ListSilencesResponse{}
	}
	return &asterismv1.ListSilencesResponse{Silences: o.alerts.engine.Silences(req.GetIncludeExpired())}
}

// CreateSilence serves AlertsService.CreateSilence.
func (o *Observatory) CreateSilence(ctx context.Context, req AdminRequest, in *asterismv1.CreateSilenceRequest) (*asterismv1.Silence, error) {
	target := strings.Trim(strings.Join([]string{in.GetRuleId(), in.GetEntityId(), in.GetProjectId()}, " "), " ")
	var out *asterismv1.Silence
	err := o.adminWrite(req, "alerts", "silence.create", "silence", target, func() (string, error) {
		var err error
		out, err = o.alerts.engine.CreateSilence(ctx, in, req.Principal.Actor())
		if err != nil {
			return "", mapAlertingErr(err)
		}
		return out.Id + " (" + target + ")", nil
	})
	return out, err
}

// DeleteSilence serves AlertsService.DeleteSilence (expires it now).
func (o *Observatory) DeleteSilence(ctx context.Context, req AdminRequest, id string) error {
	return o.adminWrite(req, "alerts", "silence.expire", id, id, func() (string, error) {
		_, err := o.alerts.engine.ExpireSilence(ctx, strings.TrimSpace(id))
		return "", mapAlertingErr(err)
	})
}

// ---------------------------------------------------------------------------
// NotificationsService: channels
// ---------------------------------------------------------------------------

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (st *alertsState) openSecrets(rec sqlite.ChannelRecord) (notify.Secrets, error) {
	var s notify.Secrets
	if len(rec.Secret) == 0 {
		return s, nil
	}
	pt, err := st.box.Open(rec.Secret, []byte("channel:"+rec.Channel.Id))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(pt, &s); err != nil {
		return s, fmt.Errorf("decode secrets: %w", err)
	}
	return s, nil
}

func (st *alertsState) sealSecrets(id string, s notify.Secrets) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return st.box.Seal(raw, []byte("channel:"+id))
}

// publicChannel returns the API view of a stored channel: secret fields
// empty, header names and secret statuses filled.
func (st *alertsState) publicChannel(rec sqlite.ChannelRecord, last map[string]*asterismv1.DeliveryAttempt) *asterismv1.NotificationChannel {
	ch := proto.Clone(rec.Channel).(*asterismv1.NotificationChannel)
	if ch.Config == nil {
		ch.Config = &asterismv1.ChannelConfig{}
	}
	_ = notify.SplitSecrets(ch.Config) // defensive: never echo a secret
	sec, err := st.openSecrets(rec)
	if err == nil {
		ch.Secrets = notify.SecretStatuses(ch.Type, sec)
		if w := ch.Config.GetWebhook(); w != nil {
			w.HeaderNames = sec.HeaderNames()
		}
	} else {
		ch.Secrets = []*asterismv1.SecretStatus{{Field: "secrets", HasValue: false, Hint: "cannot be decrypted with secret.key; re-enter them"}}
	}
	ch.LastDelivery = last[ch.Id]
	return ch
}

// ListChannels serves NotificationsService.ListChannels.
func (o *Observatory) ListChannels(ctx context.Context) (*asterismv1.ListChannelsResponse, error) {
	if o.alerts == nil {
		return &asterismv1.ListChannelsResponse{}, nil
	}
	st := o.alerts
	recs, err := st.db.ListChannels(ctx)
	if err != nil {
		return nil, newErr(ErrInternal, "%s", err.Error())
	}
	last, err := st.db.LastDeliveries(ctx)
	if err != nil {
		return nil, newErr(ErrInternal, "%s", err.Error())
	}
	out := &asterismv1.ListChannelsResponse{Channels: make([]*asterismv1.NotificationChannel, 0, len(recs))}
	for _, r := range recs {
		out.Channels = append(out.Channels, st.publicChannel(r, last))
	}
	return out, nil
}

func cleanStrings(in []string, maxItems int) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		if len(v) > 120 || strings.ContainsAny(v, "\r\n") {
			return nil, newErr(ErrInvalidArgument, "invalid filter value %q", v)
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) > maxItems {
		return nil, newErr(ErrInvalidArgument, "at most %d filter values", maxItems)
	}
	return out, nil
}

// normaliseChannel validates the common fields of a channel in place.
func normaliseChannel(ch *asterismv1.NotificationChannel) error {
	ch.Name = strings.TrimSpace(ch.Name)
	if ch.Name == "" || len(ch.Name) > 80 || strings.ContainsAny(ch.Name, "\r\n") {
		return newErr(ErrInvalidArgument, "name is required (up to 80 characters)")
	}
	if ch.MinSeverity == asterismv1.AlertSeverity_ALERT_SEVERITY_UNSPECIFIED {
		ch.MinSeverity = asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING
	}
	if ch.MinSeverity > asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL {
		return newErr(ErrInvalidArgument, "unknown min_severity")
	}
	if ch.RateLimitPerHour > 10000 {
		return newErr(ErrInvalidArgument, "rate_limit_per_hour must be at most 10000")
	}
	var err error
	if ch.Kinds, err = cleanStrings(ch.Kinds, 50); err != nil {
		return err
	}
	if ch.Projects, err = cleanStrings(ch.Projects, 50); err != nil {
		return err
	}
	ch.Secrets, ch.LastDelivery = nil, nil
	return nil
}

func (o *Observatory) invalidateRoutes() {
	o.alerts.mu.Lock()
	o.alerts.stale = true
	o.alerts.mu.Unlock()
}

// CreateChannel serves NotificationsService.CreateChannel.
func (o *Observatory) CreateChannel(ctx context.Context, req AdminRequest, in *asterismv1.NotificationChannel) (*asterismv1.NotificationChannel, error) {
	in = proto.Clone(in).(*asterismv1.NotificationChannel)
	var out *asterismv1.NotificationChannel
	err := o.adminWrite(req, "notifications", "channel.create", "channel", in.GetName(), func() (string, error) {
		st := o.alerts
		if err := normaliseChannel(in); err != nil {
			return "", err
		}
		if in.Config == nil {
			return "", newErr(ErrInvalidArgument, "config is required")
		}
		if in.Type == asterismv1.ChannelType_CHANNEL_TYPE_UNSPECIFIED {
			in.Type = notify.ConfigType(in.Config)
		}
		if in.Type == asterismv1.ChannelType_CHANNEL_TYPE_WEBPUSH {
			recs, err := st.db.ListChannels(ctx)
			if err != nil {
				return "", newErr(ErrInternal, "%s", err.Error())
			}
			for _, r := range recs {
				if r.Channel.Type == asterismv1.ChannelType_CHANNEL_TYPE_WEBPUSH {
					return "", newErr(ErrInvalidArgument, "a Web Push channel already exists (%s); it delivers to every subscribed browser", r.Channel.Name)
				}
			}
		}
		sec := notify.SplitSecrets(in.Config)
		if err := notify.Validate(in.Type, in.Config, sec, st.allowPrivate); err != nil {
			return "", newErr(ErrInvalidArgument, "%s", err.Error())
		}
		now := time.Now().UnixMilli()
		in.Id = newID("ch_")
		in.CreatedAtMs, in.UpdatedAtMs = now, now
		blob, err := st.sealSecrets(in.Id, sec)
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		rec := sqlite.ChannelRecord{Channel: in, Secret: blob}
		if err := st.db.SaveChannel(ctx, rec); err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		o.invalidateRoutes()
		out = st.publicChannel(rec, nil)
		return in.Name + " (" + in.Id + ")", nil
	})
	return out, err
}

// UpdateChannel serves NotificationsService.UpdateChannel. Secret fields
// left empty keep their stored values.
func (o *Observatory) UpdateChannel(ctx context.Context, req AdminRequest, in *asterismv1.UpdateChannelRequest) (*asterismv1.NotificationChannel, error) {
	ch := proto.Clone(in.GetChannel()).(*asterismv1.NotificationChannel)
	if ch == nil {
		ch = &asterismv1.NotificationChannel{}
	}
	var out *asterismv1.NotificationChannel
	err := o.adminWrite(req, "notifications", "channel.update", ch.GetId(), ch.GetName(), func() (string, error) {
		st := o.alerts
		old, err := st.db.GetChannel(ctx, ch.Id)
		if errors.Is(err, sqlite.ErrNotFound) {
			return "", newErr(ErrNotFound, "unknown channel %q", ch.Id)
		}
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		if ch.Type == asterismv1.ChannelType_CHANNEL_TYPE_UNSPECIFIED {
			ch.Type = old.Channel.Type
		}
		if ch.Type != old.Channel.Type {
			return "", newErr(ErrInvalidArgument, "a channel's type cannot change; create a new channel")
		}
		if ch.Config == nil {
			ch.Config = proto.Clone(old.Channel.Config).(*asterismv1.ChannelConfig)
		}
		if err := normaliseChannel(ch); err != nil {
			return "", err
		}
		oldSec, err := st.openSecrets(old)
		if err != nil {
			oldSec = notify.Secrets{} // undecryptable: the update must supply them again
		}
		next := notify.SplitSecrets(ch.Config)
		sec, err := notify.MergeSecrets(oldSec, next, in.GetClearSecrets())
		if err != nil {
			return "", newErr(ErrInvalidArgument, "%s", err.Error())
		}
		if err := notify.Validate(ch.Type, ch.Config, sec, st.allowPrivate); err != nil {
			return "", newErr(ErrInvalidArgument, "%s", err.Error())
		}
		ch.CreatedAtMs = old.Channel.CreatedAtMs
		ch.UpdatedAtMs = time.Now().UnixMilli()
		blob, err := st.sealSecrets(ch.Id, sec)
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		rec := sqlite.ChannelRecord{Channel: ch, Secret: blob}
		if err := st.db.SaveChannel(ctx, rec); err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		o.invalidateRoutes()
		last, _ := st.db.LastDeliveries(ctx)
		out = st.publicChannel(rec, last)
		return ch.Name + " (" + ch.Id + ")", nil
	})
	return out, err
}

// DeleteChannel serves NotificationsService.DeleteChannel.
func (o *Observatory) DeleteChannel(ctx context.Context, req AdminRequest, id string) error {
	return o.adminWrite(req, "notifications", "channel.delete", id, id, func() (string, error) {
		st := o.alerts
		name := id
		if rec, err := st.db.GetChannel(ctx, id); err == nil {
			name = rec.Channel.Name + " (" + id + ")"
		}
		err := st.db.DeleteChannel(ctx, id)
		if errors.Is(err, sqlite.ErrNotFound) {
			return "", newErr(ErrNotFound, "unknown channel %q", id)
		}
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		o.invalidateRoutes()
		return name, nil
	})
}

// buildRoute opens a channel's secrets and builds its sender.
func (o *Observatory) buildRoute(rec sqlite.ChannelRecord) (alerting.Route, error) {
	st := o.alerts
	sec, err := st.openSecrets(rec)
	if err != nil {
		return alerting.Route{}, fmt.Errorf("channel %s: secrets: %w", rec.Channel.Id, err)
	}
	sender, err := notify.Build(rec.Channel, sec, notify.Deps{
		Client: st.client, AllowPrivate: st.allowPrivate, WebPush: o.webPushSender(), TelegramAPI: st.telegramAPI,
	})
	if err != nil {
		return alerting.Route{}, err
	}
	return alerting.Route{Channel: rec.Channel, Sender: sender, Secrets: sec.Values()}, nil
}

// alertRoutes returns the cached routes, rebuilding them after a change.
func (o *Observatory) alertRoutes(ctx context.Context) []alerting.Route {
	st := o.alerts
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.stale {
		return st.routes
	}
	recs, err := st.db.ListChannels(ctx)
	if err != nil {
		o.logger.Warn("alerts: list channels", "err", err)
		return st.routes
	}
	routes := make([]alerting.Route, 0, len(recs))
	for _, rec := range recs {
		r, err := o.buildRoute(rec)
		if err != nil {
			o.logger.Warn("alerts: channel unavailable", "channel", rec.Channel.Id, "err", err)
			continue
		}
		routes = append(routes, r)
	}
	st.routes, st.stale = routes, false
	return routes
}

// TestChannel serves NotificationsService.TestChannel: one synchronous
// attempt, bypassing filters, rate limits, and the enabled flag.
func (o *Observatory) TestChannel(ctx context.Context, req AdminRequest, id string) (*asterismv1.DeliveryAttempt, error) {
	var out *asterismv1.DeliveryAttempt
	err := o.adminWrite(req, "notifications", "channel.test", id, id, func() (string, error) {
		st := o.alerts
		rec, err := st.db.GetChannel(ctx, id)
		if errors.Is(err, sqlite.ErrNotFound) {
			return "", newErr(ErrNotFound, "unknown channel %q", id)
		}
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		route, err := o.buildRoute(rec)
		if err != nil {
			return "", newErr(ErrFailedPrecondition, "%s", err.Error())
		}
		msg := notify.TestMessage(st.hubURL, newID("test_"), time.Now())
		out = st.engine.Dispatcher().SendNow(context.WithoutCancel(ctx), route, msg)
		name := rec.Channel.Name + " (" + id + ")"
		if out.Status != "sent" {
			return name, newErr(ErrUnavailable, "test notification failed: %s", out.Error)
		}
		return name, nil
	})
	if out != nil && err != nil && errors.Is(err, ErrUnavailable) {
		// The attempt itself is the answer; the UI shows its error.
		return out, nil
	}
	return out, err
}

// ListDeliveries serves NotificationsService.ListDeliveries.
func (o *Observatory) ListDeliveries(ctx context.Context, req *asterismv1.ListDeliveriesRequest) (*asterismv1.ListDeliveriesResponse, error) {
	page, size := pageBounds(req.GetPage())
	if o.alerts == nil {
		_, info := paginate([]int{}, req.GetPage())
		return &asterismv1.ListDeliveriesResponse{PageInfo: info}, nil
	}
	items, total, err := o.alerts.db.ListDeliveries(ctx, strings.TrimSpace(req.GetChannelId()), (page-1)*size, size)
	if err != nil {
		return nil, newErr(ErrInternal, "%s", err.Error())
	}
	pages := max(1, (total+size-1)/size)
	return &asterismv1.ListDeliveriesResponse{
		Deliveries: items,
		PageInfo:   &asterismv1.PageInfo{Page: int32(page), PageSize: int32(size), TotalSize: int32(total), TotalPages: int32(pages)},
	}, nil
}

// ---------------------------------------------------------------------------
// Web Push
// ---------------------------------------------------------------------------

type pushSecret struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

func endpointHash(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	return base64.RawURLEncoding.DecodeString(strings.NewReplacer("+", "-", "/", "_").Replace(s))
}

// webPushSender builds the shared Web Push sender (nil without VAPID keys).
func (o *Observatory) webPushSender() *notify.WebPush {
	st := o.alerts
	if st.vapid == nil {
		return nil
	}
	subscriber := "https://github.com/prabhavalabs/asterism"
	if strings.HasPrefix(st.hubURL, "https://") {
		subscriber = st.hubURL
	}
	return &notify.WebPush{
		Keys: *st.vapid, Subscriber: subscriber, Client: st.client, AllowPrivate: st.allowPrivate,
		Targets: func() []notify.PushTarget { return o.pushTargets(context.Background()) },
		OnGone: func(id string) {
			if _, err := st.db.DeletePushSubscription(context.Background(), id, ""); err == nil {
				o.logger.Info("web push subscription expired and was removed", "id", id)
			}
		},
		OnSuccess: func(id string) { o.touchPushSubscription(id) },
	}
}

func (o *Observatory) pushTargets(ctx context.Context) []notify.PushTarget {
	st := o.alerts
	recs, err := st.db.ListPushSubscriptions(ctx)
	if err != nil {
		o.logger.Warn("alerts: list push subscriptions", "err", err)
		return nil
	}
	out := make([]notify.PushTarget, 0, len(recs))
	for _, r := range recs {
		pt, err := st.box.Open(r.Secret, []byte("push:"+r.EndpointHash))
		if err != nil {
			continue
		}
		var ps pushSecret
		if json.Unmarshal(pt, &ps) != nil {
			continue
		}
		out = append(out, notify.PushTarget{ID: r.Sub.Id, Endpoint: ps.Endpoint, P256dh: ps.P256dh, Auth: ps.Auth})
	}
	return out
}

func (o *Observatory) touchPushSubscription(id string) {
	st := o.alerts
	recs, err := st.db.ListPushSubscriptions(context.Background())
	if err != nil {
		return
	}
	for _, r := range recs {
		if r.Sub.Id == id {
			r.Sub.LastSuccessMs = time.Now().UnixMilli()
			_, _ = st.db.SavePushSubscription(context.Background(), r)
		}
	}
}

// GetWebPushConfig serves NotificationsService.GetWebPushConfig.
func (o *Observatory) GetWebPushConfig(ctx context.Context) *asterismv1.GetWebPushConfigResponse {
	if o.alerts == nil {
		return &asterismv1.GetWebPushConfigResponse{UnavailableReason: alertsDisabledMsg}
	}
	st := o.alerts
	if st.vapid == nil {
		return &asterismv1.GetWebPushConfigResponse{UnavailableReason: "Web Push keys are unavailable: " + st.vapidErr}
	}
	recs, _ := st.db.ListPushSubscriptions(ctx)
	return &asterismv1.GetWebPushConfigResponse{PublicKey: st.vapid.PublicKey, Available: true, SubscriptionCount: uint32(len(recs))}
}

// ListWebPushSubscriptions serves NotificationsService.ListWebPushSubscriptions.
func (o *Observatory) ListWebPushSubscriptions(ctx context.Context) (*asterismv1.ListWebPushSubscriptionsResponse, error) {
	if o.alerts == nil {
		return &asterismv1.ListWebPushSubscriptionsResponse{}, nil
	}
	recs, err := o.alerts.db.ListPushSubscriptions(ctx)
	if err != nil {
		return nil, newErr(ErrInternal, "%s", err.Error())
	}
	out := &asterismv1.ListWebPushSubscriptionsResponse{}
	for _, r := range recs {
		out.Subscriptions = append(out.Subscriptions, r.Sub)
	}
	return out, nil
}

func sanitizeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		s = "Browser"
	}
	return s
}

// SubscribeWebPush serves NotificationsService.SubscribeWebPush. It also
// creates the Web Push channel on first use.
func (o *Observatory) SubscribeWebPush(ctx context.Context, req AdminRequest, in *asterismv1.SubscribeWebPushRequest) (*asterismv1.SubscribeWebPushResponse, error) {
	label := sanitizeLabel(in.GetLabel())
	var out *asterismv1.SubscribeWebPushResponse
	err := o.adminWrite(req, "notifications", "webpush.subscribe", "webpush", label, func() (string, error) {
		st := o.alerts
		if st.vapid == nil {
			return "", newErr(ErrFailedPrecondition, "Web Push keys are unavailable: %s", st.vapidErr)
		}
		endpoint := strings.TrimSpace(in.GetEndpoint())
		if len(endpoint) > 2048 {
			return "", newErr(ErrInvalidArgument, "endpoint is too long")
		}
		if err := notify.ValidateTargetURL(endpoint, st.allowPrivate); err != nil {
			return "", newErr(ErrInvalidArgument, "endpoint: %s", err.Error())
		}
		if k, err := decodeB64(in.GetP256Dh()); err != nil || len(k) != 65 || k[0] != 4 {
			return "", newErr(ErrInvalidArgument, "p256dh must be an uncompressed P-256 public key (base64url)")
		}
		if a, err := decodeB64(in.GetAuth()); err != nil || len(a) != 16 {
			return "", newErr(ErrInvalidArgument, "auth must be a 16-byte secret (base64url)")
		}
		hash := endpointHash(endpoint)
		raw, _ := json.Marshal(pushSecret{Endpoint: endpoint, P256dh: in.GetP256Dh(), Auth: in.GetAuth()})
		blob, err := st.box.Seal(raw, []byte("push:"+hash))
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		host := ""
		if u, err := url.Parse(endpoint); err == nil {
			host = u.Hostname()
		}
		sub, err := st.db.SavePushSubscription(ctx, sqlite.PushRecord{
			Sub:          &asterismv1.WebPushSubscription{Id: newID("push_"), EndpointHost: host, Label: label, CreatedAtMs: time.Now().UnixMilli()},
			EndpointHash: hash, Secret: blob,
		})
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		chID, err := o.ensureWebPushChannel(ctx)
		if err != nil {
			return "", err
		}
		out = &asterismv1.SubscribeWebPushResponse{Subscription: sub, ChannelId: chID}
		return label + " (" + host + ")", nil
	})
	return out, err
}

func (o *Observatory) ensureWebPushChannel(ctx context.Context) (string, error) {
	st := o.alerts
	recs, err := st.db.ListChannels(ctx)
	if err != nil {
		return "", newErr(ErrInternal, "%s", err.Error())
	}
	for _, r := range recs {
		if r.Channel.Type == asterismv1.ChannelType_CHANNEL_TYPE_WEBPUSH {
			return r.Channel.Id, nil
		}
	}
	now := time.Now().UnixMilli()
	ch := &asterismv1.NotificationChannel{
		Id: newID("ch_"), Name: "Browser notifications", Type: asterismv1.ChannelType_CHANNEL_TYPE_WEBPUSH, Enabled: true,
		MinSeverity: asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING, SendResolved: true, RateLimitPerHour: 60,
		Config:      &asterismv1.ChannelConfig{Kind: &asterismv1.ChannelConfig_Webpush{Webpush: &asterismv1.WebPushConfig{}}},
		CreatedAtMs: now, UpdatedAtMs: now,
	}
	blob, err := st.sealSecrets(ch.Id, notify.Secrets{})
	if err != nil {
		return "", newErr(ErrInternal, "%s", err.Error())
	}
	if err := st.db.SaveChannel(ctx, sqlite.ChannelRecord{Channel: ch, Secret: blob}); err != nil {
		return "", newErr(ErrInternal, "%s", err.Error())
	}
	o.invalidateRoutes()
	return ch.Id, nil
}

// UnsubscribeWebPush serves NotificationsService.UnsubscribeWebPush.
func (o *Observatory) UnsubscribeWebPush(ctx context.Context, req AdminRequest, in *asterismv1.UnsubscribeWebPushRequest) error {
	target := in.GetId()
	if target == "" {
		target = "endpoint"
	}
	return o.adminWrite(req, "notifications", "webpush.unsubscribe", target, target, func() (string, error) {
		hash := ""
		if ep := strings.TrimSpace(in.GetEndpoint()); ep != "" {
			hash = endpointHash(ep)
		}
		if hash == "" && in.GetId() == "" {
			return "", newErr(ErrInvalidArgument, "endpoint or id is required")
		}
		ok, err := o.alerts.db.DeletePushSubscription(ctx, in.GetId(), hash)
		if err != nil {
			return "", newErr(ErrInternal, "%s", err.Error())
		}
		if !ok {
			return "", newErr(ErrNotFound, "no such subscription")
		}
		return "", nil
	})
}
