package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBearerTokensAndScopes(t *testing.T) {
	readTok, readDigest, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	actTok, actDigest, _ := GenerateToken()
	mgr, err := NewManager(Options{
		AdminEmail:    "op@example.com",
		AdminPassword: "pw-for-tests",
		SessionTTL:    time.Hour,
		SessionScopes: []Scope{ScopeRead},
		Tokens: []APIToken{
			{Name: "grafana", SHA256: readDigest, Scopes: []Scope{ScopeRead}},
			{Name: "ops-bot", SHA256: actDigest, Scopes: []Scope{ScopeRead, ScopeActions}},
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	var seen Principal
	h := mgr.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		authz      string
		wantStatus int
		wantActor  string
		wantAction bool
	}{
		{"no credentials", "", http.StatusUnauthorized, "", false},
		{"wrong token", "Bearer ast_nope", http.StatusUnauthorized, "", false},
		{"read token", "Bearer " + readTok, http.StatusNoContent, "token:grafana", false},
		{"actions token", "bearer " + actTok, http.StatusNoContent, "token:ops-bot", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seen = Principal{}
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if seen.Actor() != tc.wantActor && tc.wantActor != "" {
				t.Fatalf("actor = %q, want %q", seen.Actor(), tc.wantActor)
			}
			if seen.Has(ScopeActions) != tc.wantAction {
				t.Fatalf("actions scope = %v", seen.Has(ScopeActions))
			}
		})
	}

	sess, err := mgr.Authenticate("op@example.com", "pw-for-tests")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.Token})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen.Kind != "session" || seen.Has(ScopeActions) || !seen.Has(ScopeRead) {
		t.Fatalf("session principal = %+v", seen)
	}
}

func TestTokenConfigValidation(t *testing.T) {
	_, digest, _ := GenerateToken()
	tests := []struct {
		name   string
		tokens []APIToken
	}{
		{"missing name", []APIToken{{SHA256: digest, Scopes: []Scope{ScopeRead}}}},
		{"bad digest", []APIToken{{Name: "a", SHA256: "abc", Scopes: []Scope{ScopeRead}}}},
		{"no scopes", []APIToken{{Name: "a", SHA256: digest}}},
		{"duplicate", []APIToken{{Name: "a", SHA256: digest, Scopes: []Scope{ScopeRead}}, {Name: "a", SHA256: digest, Scopes: []Scope{ScopeRead}}}},
	}
	for _, tc := range tests {
		if _, err := NewManager(Options{AdminEmail: "a@b.c", AdminPassword: "x", Tokens: tc.tokens}); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
	if _, err := ParseScope("admin"); err == nil {
		t.Fatal("expected unknown scope error")
	}
	if TokenDigest("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("TokenDigest must be hex sha256")
	}
}
