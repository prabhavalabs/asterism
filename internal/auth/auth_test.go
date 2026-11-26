package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword("correct-horse-battery-staple", hash) {
		t.Fatal("expected matching password to verify")
	}
	if VerifyPassword("wrong-password", hash) {
		t.Fatal("expected wrong password to fail verification")
	}
}

func TestSessionLifecycleHTTP(t *testing.T) {
	mgr, err := NewManager(Options{
		AdminEmail:    "operator@example.com",
		AdminPassword: "test-secret-password",
		SessionTTL:    time.Hour,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	mux := http.NewServeMux()
	mgr.RegisterRoutes(mux)
	mux.Handle("/protected", mgr.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	// Unauthenticated access to protected route is rejected.
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 before login, got %d", rec.Code)
	}

	// Wrong password fails.
	body, _ := json.Marshal(map[string]string{
		"email":    "operator@example.com",
		"password": "nope",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad password, got %d", rec.Code)
	}

	// Correct credentials issue HttpOnly SameSite=Strict cookie.
	body, _ = json.Marshal(map[string]string{
		"email":    "OPERATOR@EXAMPLE.COM",
		"password": "test-secret-password",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on login, got %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}

	// Authenticated access succeeds.
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", rec.Code)
	}

	// Logout invalidates the session.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on logout, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d", rec.Code)
	}
}
