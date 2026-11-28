package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory SessionStore standing in for the SQLite store.
type memStore struct {
	mu   sync.Mutex
	rows map[string]StoredSession
}

func newMemStore() *memStore { return &memStore{rows: map[string]StoredSession{}} }

func (s *memStore) LoadSessions(context.Context) ([]StoredSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StoredSession, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, nil
}

func (s *memStore) SaveSession(_ context.Context, r StoredSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[r.TokenSHA256] = r
	return nil
}

func (s *memStore) DeleteSession(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, key)
	return nil
}

func (s *memStore) DeleteExpiredSessions(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.rows {
		if !now.Before(r.ExpiresAt) {
			delete(s.rows, k)
		}
	}
	return nil
}

func (s *memStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func newTestManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	if opts.AdminEmail == "" {
		opts.AdminEmail = "op@example.com"
	}
	if opts.AdminPassword == "" && opts.PasswordHash == "" {
		opts.AdminPassword = "pw-for-tests"
	}
	m, err := NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDefaultSessionTTLIsThirtyDays(t *testing.T) {
	m := newTestManager(t, Options{})
	sess, err := m.Authenticate("op@example.com", "pw-for-tests")
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.ExpiresAt.Sub(sess.CreatedAt); got != 30*24*time.Hour {
		t.Fatalf("default TTL = %s, want 720h", got)
	}
}

func TestSessionsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()

	first := newTestManager(t, Options{})
	if _, err := first.AttachStore(ctx, store); err != nil {
		t.Fatal(err)
	}
	sess, err := first.Authenticate("op@example.com", "pw-for-tests")
	if err != nil {
		t.Fatal(err)
	}
	for k := range store.rows {
		if k == sess.Token {
			t.Fatal("raw session token was persisted")
		}
	}

	// A new manager (hub restart) with the same plaintext password, whose
	// Argon2id hash gets a new salt, still accepts the session.
	second := newTestManager(t, Options{})
	n, err := second.AttachStore(ctx, store)
	if err != nil || n != 1 {
		t.Fatalf("AttachStore = %d, %v; want 1 session", n, err)
	}
	if _, ok := second.ValidateToken(sess.Token); !ok {
		t.Fatal("session lost across restart")
	}

	// Logout removes the persisted copy too.
	second.Revoke(sess.Token)
	if store.len() != 0 {
		t.Fatalf("store has %d sessions after logout", store.len())
	}
	third := newTestManager(t, Options{})
	if _, err := third.AttachStore(ctx, store); err != nil {
		t.Fatal(err)
	}
	if _, ok := third.ValidateToken(sess.Token); ok {
		t.Fatal("revoked session came back after restart")
	}
}

func TestCredentialChangeDiscardsSessions(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		opts Options
	}{
		{"password changed", Options{AdminPassword: "a-new-password"}},
		{"email changed", Options{AdminEmail: "someone@example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore()
			first := newTestManager(t, Options{})
			if _, err := first.AttachStore(ctx, store); err != nil {
				t.Fatal(err)
			}
			sess, err := first.Authenticate("op@example.com", "pw-for-tests")
			if err != nil {
				t.Fatal(err)
			}
			second := newTestManager(t, tt.opts)
			n, err := second.AttachStore(ctx, store)
			if err != nil || n != 0 {
				t.Fatalf("AttachStore = %d, %v; want 0", n, err)
			}
			if _, ok := second.ValidateToken(sess.Token); ok {
				t.Fatal("session survived a credential change")
			}
			if store.len() != 0 {
				t.Fatalf("stale session left in store: %d", store.len())
			}
		})
	}
}

func TestExpiredSessionsAreNotRestored(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	m := newTestManager(t, Options{})
	now := time.Now().UTC()
	_ = store.SaveSession(ctx, StoredSession{
		TokenSHA256: hashToken("old"), Email: "op@example.com", Credential: m.credential,
		CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	})
	n, err := m.AttachStore(ctx, store)
	if err != nil || n != 0 || store.len() != 0 {
		t.Fatalf("AttachStore = %d, %v, store %d; want expired row pruned", n, err, store.len())
	}
}

func TestMeRenewsSessionPastHalfTTL(t *testing.T) {
	store := newMemStore()
	m := newTestManager(t, Options{SessionTTL: 10 * time.Hour})
	if _, err := m.AttachStore(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	sess, err := m.Authenticate("op@example.com", "pw-for-tests")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterRoutes(mux)
	me := func() *http.Response {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.Token})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Result()
	}

	// Fresh session: no renewal, no cookie.
	if res := me(); len(res.Cookies()) != 0 {
		t.Fatalf("fresh session re-issued a cookie: %v", res.Cookies())
	}

	// Age the session past half its TTL.
	key := hashToken(sess.Token)
	m.mu.Lock()
	aged := m.sessions[key]
	aged.ExpiresAt = time.Now().UTC().Add(2 * time.Hour)
	m.sessions[key] = aged
	m.mu.Unlock()

	res := me()
	cookies := res.Cookies()
	if len(cookies) != 1 || cookies[0].Value != sess.Token || cookies[0].MaxAge < int((9*time.Hour).Seconds()) {
		t.Fatalf("renewal cookie = %+v", cookies)
	}
	renewed, ok := m.ValidateToken(sess.Token)
	if !ok || time.Until(renewed.ExpiresAt) < 9*time.Hour {
		t.Fatalf("session not extended: %v %v", renewed.ExpiresAt, ok)
	}
	if got := store.rows[key].ExpiresAt; !got.Equal(renewed.ExpiresAt) {
		t.Fatalf("renewal not persisted: store %v, memory %v", got, renewed.ExpiresAt)
	}
}
