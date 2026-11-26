package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ScopeIngest lets an asterism-agent push telemetry for exactly one host. It
// is held only by agent tokens, is accepted only by AgentService, and grants
// no read access: RequireSession never recognises an agent token, and
// RequireIngest never recognises a session cookie or an API token.
const ScopeIngest Scope = "ingest"

// PrincipalKindAgent is Principal.Kind for agent tokens. Subject is the host
// name the token is bound to.
const PrincipalKindAgent = "agent"

// AgentTokenPrefix starts every agent token, so a leaked token is easy to
// recognise and cannot be confused with an API token ("ast_").
const AgentTokenPrefix = "asta_"

// AgentToken is an enrolled agent: the host name its token is bound to and
// the token's SHA-256 digest. The token itself is never stored.
type AgentToken struct {
	Name   string
	SHA256 string
}

type agentToken struct {
	name   string
	digest [sha256.Size]byte
}

var hostNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

// ValidHostName reports whether s can name an agent host. Names become part
// of entity ids ("@<host>/ctr:..."), so they are restricted to a safe charset
// and may not start with '@'.
func ValidHostName(s string) bool { return hostNameRe.MatchString(s) }

func compileAgentTokens(tokens []AgentToken, api []apiToken) ([]agentToken, error) {
	out := make([]agentToken, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		name := strings.TrimSpace(t.Name)
		if !ValidHostName(name) {
			return nil, fmt.Errorf("agent %q: name must be 1-63 characters of letters, digits, '.', '_' or '-'", t.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate agent name %q", name)
		}
		seen[name] = true
		raw, err := hex.DecodeString(strings.TrimSpace(strings.ToLower(t.SHA256)))
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("agent %q: sha256 must be a 64-character hex digest (from `asterism agent token create`)", name)
		}
		var d [sha256.Size]byte
		copy(d[:], raw)
		for _, a := range api {
			if a.digest == d {
				return nil, fmt.Errorf("agent %q reuses the digest of API token %q; agent tokens must be distinct", name, a.name)
			}
		}
		out = append(out, agentToken{name: name, digest: d})
	}
	return out, nil
}

// GenerateAgentToken returns a new agent token ("asta_" + 32 random bytes,
// base64url) and its SHA-256 hex digest.
func GenerateAgentToken() (token, digest string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate agent token: %w", err)
	}
	token = AgentTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, TokenDigest(token), nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// agentPrincipal resolves an agent bearer token. It compares against every
// enrolled digest in constant time.
func (m *Manager) agentPrincipal(r *http.Request) (Principal, bool) {
	tok := bearer(r)
	if tok == "" || len(m.agents) == 0 {
		return Principal{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	var match *agentToken
	for i := range m.agents {
		if subtle.ConstantTimeCompare(sum[:], m.agents[i].digest[:]) == 1 {
			match = &m.agents[i]
		}
	}
	if match == nil {
		return Principal{}, false
	}
	return Principal{Kind: PrincipalKindAgent, Subject: match.name, Scopes: []Scope{ScopeIngest}}, true
}

// RequireIngest wraps the AgentService handler: only agent tokens pass, and
// the request context carries their Principal (Subject = bound host name).
// Session cookies and API tokens are refused even when valid, so a browser
// session or a read token can never inject telemetry.
func (m *Manager) RequireIngest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := m.agentPrincipal(r)
		if !ok {
			// The body is a Connect error so agents show the reason.
			if _, other := m.Principal(r); other {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"code":    "permission_denied",
					"message": "AgentService accepts only asterism-agent tokens (scope \"ingest\"); create one with `asterism agent token create --host <name>`",
				})
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"code":    "unauthenticated",
				"message": "unknown agent token: check hub.token_file on the agent and the [[agents]] sha256 on the hub",
			})
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}
