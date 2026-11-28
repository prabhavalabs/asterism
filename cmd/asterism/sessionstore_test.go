package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prabhavalabs/asterism/internal/config"
)

// TestSessionSurvivesHubRestart logs in, builds a second hub on the same
// data directory, and checks that the cookie is still accepted.
func TestSessionSurvivesHubRestart(t *testing.T) {
	cfg := config.Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.Collect.Mode = "showcase"
	cfg.Auth.AdminEmail, cfg.Auth.AdminPassword = "admin@example.com", "pw"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	newHandler := func() http.Handler {
		mgr, err := newAuthManager(cfg)
		if err != nil {
			t.Fatal(err)
		}
		h, err := newHub(cfg, logger, mgr)
		if err != nil {
			t.Fatal(err)
		}
		return h.handler
	}

	body, _ := json.Marshal(map[string]string{"email": "admin@example.com", "password": "pw"})
	rec := httptest.NewRecorder()
	newHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body)))
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("login = %d %v", rec.Code, rec.Result().Cookies())
	}
	cookie := rec.Result().Cookies()[0]
	if cookie.MaxAge < 29*24*3600 {
		t.Fatalf("cookie MaxAge = %d, want about 30 days", cookie.MaxAge)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	newHandler().ServeHTTP(rec, req)
	var me struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&me); err != nil || !me.Authenticated {
		t.Fatalf("after restart /me = %+v, %v", me, err)
	}
}
