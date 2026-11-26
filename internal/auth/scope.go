package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Scope is a permission granted to a caller.
type Scope string

const (
	// ScopeRead allows every read-only RPC.
	ScopeRead Scope = "read"
	// ScopeActions allows mutating workload actions (start, stop, restart, ...).
	ScopeActions Scope = "actions"
)

// ParseScope validates a scope name.
func ParseScope(s string) (Scope, error) {
	switch Scope(strings.ToLower(strings.TrimSpace(s))) {
	case ScopeRead:
		return ScopeRead, nil
	case ScopeActions:
		return ScopeActions, nil
	case ScopeIngest:
		return "", fmt.Errorf("scope %q is reserved for agent tokens; create one with `asterism agent token create --host <name>`", s)
	default:
		return "", fmt.Errorf("unknown scope %q (want read or actions)", s)
	}
}

// Principal identifies an authenticated caller and the scopes it holds.
type Principal struct {
	// Kind is "session" for browser sessions, "token" for API tokens, or
	// "agent" for asterism-agent tokens.
	Kind string
	// Subject is the admin email for sessions or the token name for tokens.
	Subject string
	Scopes  []Scope
}

// Has reports whether the principal holds the scope.
func (p Principal) Has(s Scope) bool {
	return slices.Contains(p.Scopes, s)
}

// Actor returns a display identifier for audit records.
func (p Principal) Actor() string {
	switch p.Kind {
	case "token":
		return "token:" + p.Subject
	case PrincipalKindAgent:
		return "agent:" + p.Subject
	}
	return p.Subject
}

// ScopeNames returns the scope list as strings.
func (p Principal) ScopeNames() []string {
	out := make([]string, len(p.Scopes))
	for i, s := range p.Scopes {
		out[i] = string(s)
	}
	return out
}

type principalKey struct{}

// WithPrincipal stores the principal in the context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored by RequireSession.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// APIToken is a statically configured bearer token. Only the SHA-256 digest is kept.
type APIToken struct {
	Name   string
	SHA256 string // lowercase hex digest of the token
	Scopes []Scope
}

type apiToken struct {
	name   string
	digest [sha256.Size]byte
	scopes []Scope
}

func compileTokens(tokens []APIToken) ([]apiToken, error) {
	out := make([]apiToken, 0, len(tokens))
	seen := make(map[string]bool)
	for _, t := range tokens {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			return nil, fmt.Errorf("api token without a name")
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate api token name %q", name)
		}
		seen[name] = true
		raw, err := hex.DecodeString(strings.TrimSpace(strings.ToLower(t.SHA256)))
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("api token %q: sha256 must be a 64-character hex digest", name)
		}
		if len(t.Scopes) == 0 {
			return nil, fmt.Errorf("api token %q: at least one scope is required", name)
		}
		var d [sha256.Size]byte
		copy(d[:], raw)
		out = append(out, apiToken{name: name, digest: d, scopes: slices.Clone(t.Scopes)})
	}
	return out, nil
}

// GenerateToken returns a new random bearer token and its SHA-256 hex digest.
func GenerateToken() (token, digest string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = "ast_" + base64.RawURLEncoding.EncodeToString(b)
	return token, TokenDigest(token), nil
}

// TokenDigest returns the lowercase hex SHA-256 digest of a token.
func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validateBearer checks an Authorization: Bearer header against configured tokens.
func (m *Manager) validateBearer(r *http.Request) (Principal, bool) {
	tok := bearer(r)
	if tok == "" || len(m.tokens) == 0 {
		return Principal{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	var match *apiToken
	for i := range m.tokens {
		if subtle.ConstantTimeCompare(sum[:], m.tokens[i].digest[:]) == 1 {
			match = &m.tokens[i]
		}
	}
	if match == nil {
		return Principal{}, false
	}
	return Principal{Kind: "token", Subject: match.name, Scopes: slices.Clone(match.scopes)}, true
}

// Principal resolves the caller from a bearer token or session cookie.
func (m *Manager) Principal(r *http.Request) (Principal, bool) {
	if p, ok := m.validateBearer(r); ok {
		return p, true
	}
	sess, ok := m.ValidateRequest(r)
	if !ok {
		return Principal{}, false
	}
	return Principal{Kind: "session", Subject: sess.Email, Scopes: slices.Clone(m.sessionScopes)}, true
}
