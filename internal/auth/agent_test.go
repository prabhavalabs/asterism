package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAgentTokenScopeIsolation(t *testing.T) {
	agentTok, agentDigest, err := GenerateAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(agentTok, AgentTokenPrefix) || len(agentDigest) != 64 {
		t.Fatalf("token %q digest %q", agentTok, agentDigest)
	}
	readTok, readDigest, _ := GenerateToken()
	mgr, err := NewManager(Options{
		AdminEmail: "op@example.com", AdminPassword: "pw-for-tests", SessionTTL: time.Hour,
		SessionScopes: []Scope{ScopeRead, ScopeActions},
		Tokens:        []APIToken{{Name: "grafana", SHA256: readDigest, Scopes: []Scope{ScopeRead, ScopeActions}}},
		Agents:        []AgentToken{{Name: "prod-1", SHA256: agentDigest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := mgr.Authenticate("op@example.com", "pw-for-tests")
	if err != nil {
		t.Fatal(err)
	}

	var seen Principal
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	session, ingest := mgr.RequireSession(ok), mgr.RequireIngest(ok)

	do := func(h http.Handler, authz string, cookie bool) int {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		if cookie {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.Token})
		}
		rec := httptest.NewRecorder()
		seen = Principal{}
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// The agent token reaches only the ingest endpoint, bound to its host.
	if c := do(ingest, "Bearer "+agentTok, false); c != http.StatusNoContent {
		t.Fatalf("agent token on ingest: %d", c)
	}
	if seen.Kind != PrincipalKindAgent || seen.Subject != "prod-1" || !seen.Has(ScopeIngest) || seen.Has(ScopeRead) || seen.Has(ScopeActions) || seen.Actor() != "agent:prod-1" {
		t.Fatalf("agent principal %+v", seen)
	}
	// ...and can read nothing.
	if c := do(session, "Bearer "+agentTok, false); c != http.StatusUnauthorized {
		t.Fatalf("agent token on read API: %d", c)
	}
	// Session cookies and API tokens, even with every scope, cannot ingest.
	if c := do(ingest, "", true); c != http.StatusForbidden {
		t.Fatalf("session on ingest: %d", c)
	}
	if c := do(ingest, "Bearer "+readTok, false); c != http.StatusForbidden {
		t.Fatalf("api token on ingest: %d", c)
	}
	if c := do(ingest, "Bearer asta_wrong", false); c != http.StatusUnauthorized {
		t.Fatalf("unknown agent token: %d", c)
	}
	if c := do(ingest, "", false); c != http.StatusUnauthorized {
		t.Fatalf("anonymous ingest: %d", c)
	}
	// The ordinary paths still work.
	if c := do(session, "Bearer "+readTok, false); c != http.StatusNoContent {
		t.Fatalf("api token on read API: %d", c)
	}
	if c := do(session, "", true); c != http.StatusNoContent {
		t.Fatalf("session on read API: %d", c)
	}
}

func TestAgentTokenConfigValidation(t *testing.T) {
	_, d, _ := GenerateAgentToken()
	_, apiDigest, _ := GenerateToken()
	base := Options{AdminEmail: "a@example.com", AdminPassword: "pw"}
	tests := []struct {
		name   string
		agents []AgentToken
		tokens []APIToken
		ok     bool
	}{
		{"valid", []AgentToken{{Name: "prod-1", SHA256: d}}, nil, true},
		{"bad name", []AgentToken{{Name: "@prod", SHA256: d}}, nil, false},
		{"duplicate", []AgentToken{{Name: "a", SHA256: d}, {Name: "a", SHA256: d}}, nil, false},
		{"bad digest", []AgentToken{{Name: "a", SHA256: "abc"}}, nil, false},
		{"shared digest", []AgentToken{{Name: "a", SHA256: apiDigest}}, []APIToken{{Name: "t", SHA256: apiDigest, Scopes: []Scope{ScopeRead}}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			o.Agents, o.Tokens = tc.agents, tc.tokens
			_, err := NewManager(o)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := ParseScope("ingest"); err == nil {
		t.Fatal("API tokens must not be able to hold the ingest scope")
	}
}
