package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/showcase"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"github.com/prabhavalabs/asterism/internal/notify"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/ring"
	"github.com/prabhavalabs/asterism/internal/store/secretbox"
	"github.com/prabhavalabs/asterism/internal/store/sqlite"
)

type alertsEnv struct {
	srv     *httptest.Server
	dbPath  string
	reader  string
	actions string
	session *http.Client
}

func newAlertsServer(t *testing.T) alertsEnv {
	t.Helper()
	readTok, readDigest, _ := auth.GenerateToken()
	actTok, actDigest, _ := auth.GenerateToken()
	mgr, err := auth.NewManager(auth.Options{
		AdminEmail: "a@example.com", AdminPassword: "pw", SessionScopes: []auth.Scope{auth.ScopeRead, auth.ScopeActions},
		Tokens: []auth.APIToken{
			{Name: "reader", SHA256: readDigest, Scopes: []auth.Scope{auth.ScopeRead}},
			{Name: "operator", SHA256: actDigest, Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeActions}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	db, err := sqlite.Open(context.Background(), filepath.Join(dir, sqlite.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, _ := secretbox.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	box, _ := secretbox.New(key)
	vapid, _ := notify.LoadOrCreateVAPID(filepath.Join(dir, "vapid.json"))
	b := bus.New(64)
	r := ring.New(30)
	sc := showcase.New(b, r)
	svc := service.New(b, r, nil, sc, "showcase")
	svc.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.ConfigureDocker(sc, sc, service.ActionPolicy{})
	if err := svc.ConfigureAlerts(context.Background(), service.AlertsConfig{DB: db, Box: box, VAPID: &vapid, AllowPrivate: true, HubURL: "https://monitor.example.com"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mgr.RegisterRoutes(mux)
	New(svc).Register(mux, mgr.RequireSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	sess := &http.Client{Jar: jar}
	resp, err := sess.Post(srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"email":"a@example.com","password":"pw"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	return alertsEnv{srv: srv, dbPath: filepath.Join(dir, sqlite.FileName), reader: readTok, actions: actTok, session: sess}
}

func (e alertsEnv) tokenClient(tok string) *http.Client {
	return &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: map[string]string{"Authorization": "Bearer " + tok}}}
}

func TestNotificationsAuthAuditAndSecrets(t *testing.T) {
	ctx := context.Background()
	env := newAlertsServer(t)
	type hit struct {
		h    http.Header
		body []byte
	}
	hits := make(chan hit, 4)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hits <- hit{r.Header.Clone(), body}
		w.WriteHeader(200)
	}))
	defer recv.Close()

	const hookToken, signing, bearer = "hooktoken-0123456789", "signing-secret-0123456789", "Bearer header-secret-value"
	input := &asterismv1.NotificationChannel{
		Name: "ops webhook", Enabled: true, MinSeverity: asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING, SendResolved: true,
		Config: &asterismv1.ChannelConfig{Kind: &asterismv1.ChannelConfig_Webhook{Webhook: &asterismv1.WebhookConfig{
			Url: recv.URL + "/hook/" + hookToken, SigningSecret: signing, Headers: map[string]string{"Authorization": bearer},
		}}},
	}
	secrets := []string{hookToken, signing, bearer, "header-secret-value"}
	assertNoSecrets := func(where string, m proto.Message) {
		t.Helper()
		raw, _ := protojson.Marshal(m)
		for _, s := range secrets {
			if strings.Contains(string(raw), s) {
				t.Fatalf("%s leaks secret %q: %s", where, s, raw)
			}
		}
	}

	// API tokens are refused for writes, whatever their scopes.
	for name, tok := range map[string]string{"read": env.reader, "actions": env.actions} {
		nc := asterismv1connect.NewNotificationsServiceClient(env.tokenClient(tok), env.srv.URL)
		_, err := nc.CreateChannel(ctx, connect.NewRequest(&asterismv1.CreateChannelRequest{Channel: input}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s token create: %v", name, err)
		}
		ac := asterismv1connect.NewAlertsServiceClient(env.tokenClient(tok), env.srv.URL)
		_, err = ac.UpdateRule(ctx, connect.NewRequest(&asterismv1.UpdateRuleRequest{Rule: &asterismv1.AlertRule{Id: "disk_usage", Enabled: false, WarningThreshold: 85}}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s token rule update: %v", name, err)
		}
		if _, err := nc.ListChannels(ctx, connect.NewRequest(&asterismv1.ListChannelsRequest{})); err != nil {
			t.Fatalf("%s token read: %v", name, err)
		}
	}

	// The admin session can create; the response carries no secrets.
	nc := asterismv1connect.NewNotificationsServiceClient(env.session, env.srv.URL)
	created, err := nc.CreateChannel(ctx, connect.NewRequest(&asterismv1.CreateChannelRequest{Channel: input}))
	if err != nil {
		t.Fatal(err)
	}
	ch := created.Msg
	assertNoSecrets("create response", ch)
	if ch.Id == "" || len(ch.Secrets) != 3 || !ch.Secrets[0].HasValue || ch.GetConfig().GetWebhook().GetHeaderNames()[0] != "Authorization" {
		t.Fatalf("created = %v", ch)
	}

	// Lists (with any read principal) never return secrets.
	rc := asterismv1connect.NewNotificationsServiceClient(env.tokenClient(env.reader), env.srv.URL)
	list, err := rc.ListChannels(ctx, connect.NewRequest(&asterismv1.ListChannelsRequest{}))
	if err != nil || len(list.Msg.Channels) != 1 {
		t.Fatal(list, err)
	}
	assertNoSecrets("list", list.Msg)

	// Update without secrets keeps them (the test send is still signed).
	upd := proto.Clone(ch).(*asterismv1.NotificationChannel)
	upd.Name = "ops webhook v2"
	upd.Config = &asterismv1.ChannelConfig{Kind: &asterismv1.ChannelConfig_Webhook{Webhook: &asterismv1.WebhookConfig{}}}
	updated, err := nc.UpdateChannel(ctx, connect.NewRequest(&asterismv1.UpdateChannelRequest{Channel: upd}))
	if err != nil || updated.Msg.Name != "ops webhook v2" {
		t.Fatal(updated, err)
	}
	assertNoSecrets("update response", updated.Msg)

	test, err := nc.TestChannel(ctx, connect.NewRequest(&asterismv1.TestChannelRequest{Id: ch.Id}))
	if err != nil || test.Msg.Delivery.Status != "sent" || test.Msg.Delivery.HttpStatus != 200 {
		t.Fatalf("test = %v, %v", test, err)
	}
	select {
	case h := <-hits:
		if h.h.Get(notify.HeaderEvent) != "test" || h.h.Get("Authorization") != bearer ||
			!notify.VerifySignature(signing, h.h.Get(notify.HeaderSignature), h.body, time.Now(), time.Minute) {
			t.Fatalf("test delivery headers = %v", h.h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test notification not received")
	}
	dl, err := rc.ListDeliveries(ctx, connect.NewRequest(&asterismv1.ListDeliveriesRequest{ChannelId: ch.Id}))
	if err != nil || len(dl.Msg.Deliveries) != 1 || dl.Msg.Deliveries[0].Notification != "test" {
		t.Fatal(dl, err)
	}
	assertNoSecrets("deliveries", dl.Msg)

	// Secrets are encrypted at rest.
	raw, _ := os.ReadFile(env.dbPath)
	wal, _ := os.ReadFile(env.dbPath + "-wal")
	for _, s := range secrets {
		if strings.Contains(string(raw)+string(wal), s) {
			t.Fatalf("database stores %q in plaintext", s)
		}
	}

	// CSRF guard: writes need the Connect-Protocol-Version header.
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/asterism.v1.NotificationsService/DeleteChannel", strings.NewReader(`{"id":"`+ch.Id+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := env.session.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("request without Connect-Protocol-Version accepted")
	}

	// Every attempt is audited: denied token writes and session successes.
	ec := asterismv1connect.NewEventsServiceClient(env.tokenClient(env.reader), env.srv.URL)
	evs, err := ec.ListEvents(ctx, connect.NewRequest(&asterismv1.ListEventsRequest{KindGroup: "audit", Page: &asterismv1.PageRequest{PageSize: 100}}))
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]int{}
	for _, e := range evs.Msg.Events {
		results[e.Kind+":"+e.Audit.GetResult()]++
		assertNoSecrets("audit event", e)
	}
	for _, want := range []string{
		"audit.notifications.channel.create:denied", "audit.alerts.rule.update:denied",
		"audit.notifications.channel.create:success", "audit.notifications.channel.update:success", "audit.notifications.channel.test:success",
	} {
		if results[want] == 0 {
			t.Errorf("missing audit %s (have %v)", want, results)
		}
	}
	if results["audit.notifications.channel.create:denied"] != 2 {
		t.Errorf("want 2 denied creates, got %v", results)
	}
}

func TestAlertsRulesAndSilencesViaSession(t *testing.T) {
	ctx := context.Background()
	env := newAlertsServer(t)
	ac := asterismv1connect.NewAlertsServiceClient(env.session, env.srv.URL)
	rules, err := ac.ListRules(ctx, connect.NewRequest(&asterismv1.ListRulesRequest{}))
	if err != nil || len(rules.Msg.Rules) < 10 {
		t.Fatal(rules, err)
	}
	r, err := ac.UpdateRule(ctx, connect.NewRequest(&asterismv1.UpdateRuleRequest{Rule: &asterismv1.AlertRule{Id: "disk_usage", Enabled: true, WarningThreshold: 80, CriticalThreshold: 90, ForSeconds: 60}}))
	if err != nil || r.Msg.WarningThreshold != 80 || r.Msg.IsDefault {
		t.Fatal(r, err)
	}
	_, err = ac.UpdateRule(ctx, connect.NewRequest(&asterismv1.UpdateRuleRequest{Rule: &asterismv1.AlertRule{Id: "disk_usage", WarningThreshold: 99, CriticalThreshold: 90}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid thresholds: %v", err)
	}
	sil, err := ac.CreateSilence(ctx, connect.NewRequest(&asterismv1.CreateSilenceRequest{ProjectId: "shop", DurationSeconds: 3600, Reason: "maintenance"}))
	if err != nil || !sil.Msg.Active || sil.Msg.CreatedBy != "a@example.com" {
		t.Fatal(sil, err)
	}
	if _, err := ac.DeleteSilence(ctx, connect.NewRequest(&asterismv1.DeleteSilenceRequest{Id: sil.Msg.Id})); err != nil {
		t.Fatal(err)
	}
	ls, _ := ac.ListSilences(ctx, connect.NewRequest(&asterismv1.ListSilencesRequest{IncludeExpired: true}))
	if len(ls.Msg.Silences) != 1 || ls.Msg.Silences[0].Active {
		t.Fatalf("silences = %v", ls.Msg)
	}
	alerts, err := ac.ListAlerts(ctx, connect.NewRequest(&asterismv1.ListAlertsRequest{OrderBy: "nope"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad order_by: %v %v", alerts, err)
	}
	nc := asterismv1connect.NewNotificationsServiceClient(env.session, env.srv.URL)
	_, err = nc.SubscribeWebPush(ctx, connect.NewRequest(&asterismv1.SubscribeWebPushRequest{Endpoint: "https://push.example.com/x", P256Dh: "bad", Auth: "bad"}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument {
		t.Fatalf("bad push keys: %v", err)
	}
	cfg, err := nc.GetWebPushConfig(ctx, connect.NewRequest(&asterismv1.GetWebPushConfigRequest{}))
	if err != nil || !cfg.Msg.Available || cfg.Msg.PublicKey == "" {
		t.Fatal(cfg, err)
	}
}
