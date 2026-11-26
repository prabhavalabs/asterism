// Package dockercli builds read-only Docker CLI command scripts and parses their
// output into Asterism protobuf messages. It never returns environment values,
// secret-looking labels, or secret-looking command arguments.
package dockercli

import (
	"regexp"
	"sort"
	"strings"
)

const redacted = "[REDACTED]"

// secretKeyRe matches names that look like they carry credentials.
var secretKeyRe = regexp.MustCompile(`(?i)(passw(or)?d|passphrase|pass($|[^a-z])|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|auth($|[^o])|session[_-]?key|signing[_-]?key|(^|[^a-z])dsn($|[^a-z])|cookie)`)

// IsSecretKey reports whether a label, flag, or variable name looks secret.
func IsSecretKey(key string) bool {
	return secretKeyRe.MatchString(key)
}

// See redact_rules.go for RedactText, RedactArgs and RedactShellArgs.

// FilterLabels drops labels whose key looks secret and redacts URL credentials
// in the remaining values. It returns the filtered map and the number of dropped labels.
func FilterLabels(labels map[string]string) (map[string]string, uint32) {
	if len(labels) == 0 {
		return nil, 0
	}
	out := make(map[string]string, len(labels))
	var dropped uint32
	for k, v := range labels {
		if IsSecretKey(k) {
			dropped++
			continue
		}
		out[k] = RedactText(v)
	}
	return out, dropped
}

// SortedKeys returns map keys in lexical order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Truncate shortens s to at most n runes, appending an ellipsis when cut.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var (
	containerIDRe   = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
)

// ValidContainerID reports whether s is a 12-64 character lowercase hex Docker id.
func ValidContainerID(s string) bool {
	return containerIDRe.MatchString(s)
}

// ValidContainerName reports whether s is a legal Docker container name.
func ValidContainerName(s string) bool {
	return containerNameRe.MatchString(s)
}

// ShellQuote single-quotes s for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
