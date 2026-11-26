package mcp_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhavalabs/asterism/internal/notify"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/secretbox"
	"github.com/prabhavalabs/asterism/internal/store/sqlite"
)

// configureTestAlerts attaches an alert engine and runs one evaluation so
// the showcase's exited and OOM-killed containers produce firing alerts.
func configureTestAlerts(t *testing.T, svc *service.Observatory) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(context.Background(), filepath.Join(dir, sqlite.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, _ := secretbox.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	box, _ := secretbox.New(key)
	vapid, _ := notify.LoadOrCreateVAPID(filepath.Join(dir, "vapid.json"))
	if err := svc.ConfigureAlerts(context.Background(), service.AlertsConfig{DB: db, Box: box, VAPID: &vapid}); err != nil {
		t.Fatal(err)
	}
	svc.AlertEngine().Tick(context.Background())
}

func TestListAlertsNeverReturnsChannelSecrets(t *testing.T) {
	h := newHub(t)
	cs := memSession(t, h.handler)
	r := callTool(t, cs, "list_alerts", map[string]any{"state": "all", "page_size": 100})
	if r.isError || r.structured == nil {
		t.Fatalf("list_alerts: %+v", r)
	}
	for _, bad := range []string{"webhook_url", "signing_secret", "password", "bot_token"} {
		if strings.Contains(r.text, bad) {
			t.Fatalf("list_alerts mentions %q", bad)
		}
	}
}
