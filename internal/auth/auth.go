package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookieName = "asterism_session"
	argonTime         = 1
	argonMemory       = 64 * 1024
	argonThreads      = 4
	argonKeyLen       = 32
	saltLen           = 16
)

var (
	ErrMissingCredentials = errors.New("admin email and password (or password_hash) must be configured")
	ErrInvalidCredentials = errors.New("invalid email or password")
)

type Session struct {
	Token     string
	Email     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type Manager struct {
	adminEmail   string
	passwordHash string
	sessionTTL   time.Duration
	behindTLS    bool

	sessionScopes []Scope
	tokens        []apiToken
	agents        []agentToken

	mu       sync.RWMutex
	sessions map[string]Session
}

type Options struct {
	AdminEmail    string
	AdminPassword string
	PasswordHash  string
	SessionTTL    time.Duration
	BehindTLS     bool
	// SessionScopes are granted to every admin browser session. Defaults to [read].
	SessionScopes []Scope
	// Tokens are statically configured bearer tokens for CLI/MCP/API clients.
	Tokens []APIToken
	// Agents are enrolled asterism-agent tokens ([[agents]]). They carry only
	// the ingest scope and are accepted only by RequireIngest.
	Agents []AgentToken
}

// NewManager initializes the admin credential verifier and in-memory session store.
func NewManager(opts Options) (*Manager, error) {
	email := strings.ToLower(strings.TrimSpace(opts.AdminEmail))
	if email == "" {
		return nil, ErrMissingCredentials
	}

	hash := strings.TrimSpace(opts.PasswordHash)
	if hash == "" {
		if opts.AdminPassword == "" {
			return nil, ErrMissingCredentials
		}
		var err error
		hash, err = HashPassword(opts.AdminPassword)
		if err != nil {
			return nil, fmt.Errorf("hash admin password: %w", err)
		}
	} else if !strings.HasPrefix(hash, "$argon2id$") {
		return nil, fmt.Errorf("unsupported password hash format: expected $argon2id$")
	}

	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	tokens, err := compileTokens(opts.Tokens)
	if err != nil {
		return nil, err
	}
	agents, err := compileAgentTokens(opts.Agents, tokens)
	if err != nil {
		return nil, err
	}
	scopes := opts.SessionScopes
	if len(scopes) == 0 {
		scopes = []Scope{ScopeRead}
	}

	return &Manager{
		adminEmail:    email,
		passwordHash:  hash,
		sessionTTL:    ttl,
		behindTLS:     opts.BehindTLS,
		sessionScopes: slices.Clone(scopes),
		tokens:        tokens,
		agents:        agents,
		sessions:      make(map[string]Session),
	}, nil
}

// HashPassword hashes a plaintext password with Argon2id using a random salt.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonTime,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword verifies a plaintext password against a PHC-formatted Argon2id hash.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}

	var mem, t uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &threads); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	actual := argon2.IDKey([]byte(password), salt, t, mem, threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// Authenticate checks the supplied email and password and issues a new session.
func (m *Manager) Authenticate(email, password string) (Session, error) {
	normEmail := strings.ToLower(strings.TrimSpace(email))
	emailMatch := subtle.ConstantTimeCompare([]byte(normEmail), []byte(m.adminEmail)) == 1
	passwordMatch := VerifyPassword(password, m.passwordHash)
	if !emailMatch || !passwordMatch {
		return Session{}, ErrInvalidCredentials
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	now := time.Now().UTC()
	sess := Session{
		Token:     base64.RawURLEncoding.EncodeToString(tokenBytes),
		Email:     m.adminEmail,
		CreatedAt: now,
		ExpiresAt: now.Add(m.sessionTTL),
	}

	m.mu.Lock()
	m.pruneLocked(now)
	m.sessions[sess.Token] = sess
	m.mu.Unlock()

	return sess, nil
}

// ValidateRequest extracts and verifies the session cookie from an HTTP request.
func (m *Manager) ValidateRequest(r *http.Request) (Session, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return Session{}, false
	}
	return m.ValidateToken(cookie.Value)
}

// ValidateToken verifies a session token and checks expiry.
func (m *Manager) ValidateToken(token string) (Session, bool) {
	now := time.Now().UTC()
	m.mu.RLock()
	sess, ok := m.sessions[token]
	m.mu.RUnlock()
	if !ok {
		return Session{}, false
	}
	if now.After(sess.ExpiresAt) {
		m.Revoke(token)
		return Session{}, false
	}
	return sess, true
}

// Revoke deletes a session token.
func (m *Manager) Revoke(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *Manager) pruneLocked(now time.Time) {
	for tok, sess := range m.sessions {
		if now.After(sess.ExpiresAt) {
			delete(m.sessions, tok)
		}
	}
}

// RequireSession wraps an HTTP handler so unauthenticated callers receive 401.
// Authenticated requests carry their Principal in the request context.
func (m *Manager) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := m.Principal(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		if p.Kind == PrincipalKindAgent || (!p.Has(ScopeRead) && !p.Has(ScopeActions)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "token has no usable scope"})
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type sessionResponse struct {
	Authenticated bool     `json:"authenticated"`
	Email         string   `json:"email,omitempty"`
	ExpiresAt     string   `json:"expiresAt,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
}

func (m *Manager) sessionScopeNames() []string {
	return Principal{Scopes: m.sessionScopes}.ScopeNames()
}

// RegisterRoutes registers /api/auth/login, /api/auth/logout, and /api/auth/me.
func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", m.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", m.handleLogout)
	mux.HandleFunc("GET /api/auth/me", m.handleMe)
}

func (m *Manager) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	sess, err := m.Authenticate(req.Email, req.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid email or password"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		MaxAge:   int(m.sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   m.behindTLS || r.TLS != nil,
	})

	writeJSON(w, http.StatusOK, sessionResponse{
		Authenticated: true,
		Email:         sess.Email,
		ExpiresAt:     sess.ExpiresAt.Format(time.RFC3339),
		Scopes:        m.sessionScopeNames(),
	})
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		m.Revoke(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   m.behindTLS || r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
}

func (m *Manager) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := m.ValidateRequest(r)
	if !ok {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		Authenticated: true,
		Email:         sess.Email,
		ExpiresAt:     sess.ExpiresAt.Format(time.RFC3339),
		Scopes:        m.sessionScopeNames(),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
