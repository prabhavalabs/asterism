package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenMigratesWALAnd0600(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil || v != len(migrations) {
		t.Fatalf("schema version = %d, %v; want %d", v, err, len(migrations))
	}
	mode, err := db.JournalMode(ctx)
	if err != nil || mode != "wal" {
		t.Fatalf("journal mode = %q, %v", mode, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("db mode = %v, %v", st.Mode().Perm(), err)
	}
	if err := db.SaveRule(ctx, &asterismv1.AlertRule{Id: "disk_usage", Enabled: true, WarningThreshold: 80}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Reopening is idempotent and keeps data.
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	rules, err := db2.LoadRules(ctx)
	if err != nil || rules["disk_usage"].GetWarningThreshold() != 80 {
		t.Fatalf("rules after reopen = %v, %v", rules, err)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (999, 'future', 0)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("want error for a newer schema")
	}
}

func TestAlertsSilencesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	a := &asterismv1.Alert{Id: "disk_usage|host|/", RuleId: "disk_usage", State: asterismv1.AlertState_ALERT_STATE_FIRING, Value: 91}
	b := &asterismv1.Alert{Id: "oom_kill|ctr:x", RuleId: "oom_kill", State: asterismv1.AlertState_ALERT_STATE_PENDING}
	if err := db.SaveAlerts(ctx, []*asterismv1.Alert{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	a.State = asterismv1.AlertState_ALERT_STATE_RESOLVED
	if err := db.SaveAlerts(ctx, []*asterismv1.Alert{a}, []string{b.Id}); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadAlerts(ctx)
	if err != nil || len(got) != 1 || got[0].State != asterismv1.AlertState_ALERT_STATE_RESOLVED || got[0].Value != 91 {
		t.Fatalf("alerts = %v, %v", got, err)
	}
	if err := db.SaveSilence(ctx, &asterismv1.Silence{Id: "s1", RuleId: "disk_usage", EndsAtMs: 100}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSilence(ctx, &asterismv1.Silence{Id: "s2", EntityId: "host", EndsAtMs: 500}); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneSilences(ctx, 200); err != nil {
		t.Fatal(err)
	}
	ss, err := db.ListSilences(ctx)
	if err != nil || len(ss) != 1 || ss[0].Id != "s2" {
		t.Fatalf("silences = %v, %v", ss, err)
	}
}

func TestChannelsAndDeliveryCap(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	ch := &asterismv1.NotificationChannel{Id: "c1", Name: "ops", Type: asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK, CreatedAtMs: 1}
	if err := db.SaveChannel(ctx, ChannelRecord{Channel: ch, Secret: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	rec, err := db.GetChannel(ctx, "c1")
	if err != nil || rec.Channel.Name != "ops" || len(rec.Secret) != 3 {
		t.Fatalf("get = %v, %v", rec, err)
	}
	if _, err := db.GetChannel(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing channel: %v", err)
	}
	for i := range MaxDeliveriesPerChannel + 15 {
		if err := db.AddDelivery(ctx, &asterismv1.DeliveryAttempt{ChannelId: "c1", TsMs: int64(i), Status: "sent", Subject: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AddDelivery(ctx, &asterismv1.DeliveryAttempt{ChannelId: "c2", TsMs: 1, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	page, total, err := db.ListDeliveries(ctx, "c1", 0, 10)
	if err != nil || total != MaxDeliveriesPerChannel || len(page) != 10 {
		t.Fatalf("deliveries total=%d len=%d err=%v", total, len(page), err)
	}
	if page[0].Subject != fmt.Sprint(MaxDeliveriesPerChannel+14) || page[0].Id == 0 {
		t.Fatalf("newest first: %v", page[0])
	}
	last, err := db.LastDeliveries(ctx)
	if err != nil || last["c2"].GetStatus() != "failed" || last["c1"].GetStatus() != "sent" {
		t.Fatalf("last = %v, %v", last, err)
	}
	if err := db.DeleteChannel(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := db.ListDeliveries(ctx, "c1", 0, 10); total != 0 {
		t.Fatalf("deliveries survived channel delete: %d", total)
	}
	if err := db.DeleteChannel(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}

func TestPushSubscriptionsUpsertByEndpoint(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	s1, err := db.SavePushSubscription(ctx, PushRecord{Sub: &asterismv1.WebPushSubscription{Id: "p1", Label: "Firefox"}, EndpointHash: "h1", Secret: []byte{1}})
	if err != nil || s1.Id != "p1" {
		t.Fatal(s1, err)
	}
	s2, err := db.SavePushSubscription(ctx, PushRecord{Sub: &asterismv1.WebPushSubscription{Id: "p2", Label: "Firefox again"}, EndpointHash: "h1", Secret: []byte{2}})
	if err != nil || s2.Id != "p1" {
		t.Fatalf("same endpoint must keep id: %v %v", s2, err)
	}
	all, _ := db.ListPushSubscriptions(ctx)
	if len(all) != 1 || all[0].Sub.Label != "Firefox again" {
		t.Fatalf("subs = %v", all)
	}
	if ok, err := db.DeletePushSubscription(ctx, "", "h1"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := db.DeletePushSubscription(ctx, "p1", ""); ok {
		t.Fatal("deleted twice")
	}
}
