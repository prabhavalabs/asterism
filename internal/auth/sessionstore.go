package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/crypto/argon2"
)

// storeTimeout bounds each session store call made from a request path.
const storeTimeout = 3 * time.Second

// StoredSession is the persisted form of a browser session. The raw session
// token is never stored: only its SHA-256 digest, so a copy of the database
// cannot be replayed as a cookie.
type StoredSession struct {
	TokenSHA256 string
	Email       string
	// Credential fingerprints the admin email and password the session was
	// issued under. Sessions whose fingerprint no longer matches (the admin
	// email or password changed) are discarded on load.
	Credential string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// SessionStore persists browser sessions so they survive hub restarts.
type SessionStore interface {
	LoadSessions(ctx context.Context) ([]StoredSession, error)
	SaveSession(ctx context.Context, s StoredSession) error
	DeleteSession(ctx context.Context, tokenSHA256 string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error
}

// hashToken returns the hex SHA-256 digest used as the session key.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// credentialFingerprint derives a stable, slow-to-invert fingerprint of the
// admin credentials. The salt depends only on the email so the value is the
// same across restarts even when the password is configured in plaintext
// (whose Argon2id hash gets a fresh random salt on every start).
func credentialFingerprint(email, material string) string {
	salt := sha256.Sum256([]byte("asterism session credential\x00" + email))
	key := argon2.IDKey([]byte(material), salt[:saltLen], argonTime, argonMemory, argonThreads, argonKeyLen)
	return base64.RawStdEncoding.EncodeToString(key)
}

// AttachStore makes sessions persistent: expired and stale rows are pruned,
// the remaining sessions are loaded, and later logins, renewals and logouts
// are written through to store.
func (m *Manager) AttachStore(ctx context.Context, store SessionStore) (int, error) {
	now := time.Now().UTC()
	if err := store.DeleteExpiredSessions(ctx, now); err != nil {
		return 0, fmt.Errorf("prune sessions: %w", err)
	}
	rows, err := store.LoadSessions(ctx)
	if err != nil {
		return 0, fmt.Errorf("load sessions: %w", err)
	}
	loaded := make(map[string]Session, len(rows))
	for _, row := range rows {
		if row.Email != m.adminEmail || row.Credential != m.credential || !now.Before(row.ExpiresAt) {
			if err := store.DeleteSession(ctx, row.TokenSHA256); err != nil {
				return 0, fmt.Errorf("drop stale session: %w", err)
			}
			continue
		}
		loaded[row.TokenSHA256] = Session{Email: row.Email, CreatedAt: row.CreatedAt.UTC(), ExpiresAt: row.ExpiresAt.UTC()}
	}

	m.mu.Lock()
	for k, s := range loaded {
		m.sessions[k] = s
	}
	m.store = store
	m.mu.Unlock()
	return len(loaded), nil
}

func (m *Manager) persist(key string, s Session) {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	err := store.SaveSession(ctx, StoredSession{
		TokenSHA256: key,
		Email:       s.Email,
		Credential:  m.credential,
		CreatedAt:   s.CreatedAt,
		ExpiresAt:   s.ExpiresAt,
	})
	if err != nil {
		m.logger.Warn("persist session failed; it will not survive a restart", "err", err)
	}
}

func (m *Manager) forget(key string) {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := store.DeleteSession(ctx, key); err != nil {
		m.logger.Warn("delete persisted session failed", "err", err)
	}
}

// renew slides the expiry of an active session forward once less than half
// of the TTL remains, so a session in regular use does not lapse. It reports
// the session and whether it was extended.
func (m *Manager) renew(token string) (Session, bool) {
	key := hashToken(token)
	now := time.Now().UTC()
	m.mu.Lock()
	sess, ok := m.sessions[key]
	if !ok || !now.Before(sess.ExpiresAt) || sess.ExpiresAt.Sub(now) >= m.sessionTTL/2 {
		m.mu.Unlock()
		return sess, false
	}
	sess.ExpiresAt = now.Add(m.sessionTTL)
	m.sessions[key] = sess
	m.mu.Unlock()
	m.persist(key, sess)
	sess.Token = token
	return sess, true
}

func (m *Manager) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   m.behindTLS || r.TLS != nil,
	})
}
