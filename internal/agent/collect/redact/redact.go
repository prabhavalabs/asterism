// Package redact applies the agent's redaction rules on top of the
// built-in dockercli ones: redaction.extra_secret_keys adds key regexes and
// redaction.drop_labels drops labels by glob. The built-ins cannot be
// disabled.
package redact

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Redactor is immutable after New and safe for concurrent use.
type Redactor struct {
	extra []*regexp.Regexp
	drop  []string
}

// New compiles the rules; invalid regexes are returned as an error and
// skipped (the remaining rules still apply).
func New(r config.Redaction) (*Redactor, error) {
	red := &Redactor{drop: append([]string(nil), r.DropLabels...)}
	var errs []string
	for _, p := range r.ExtraSecretKeys {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%q: %v", p, err))
			continue
		}
		red.extra = append(red.extra, re)
	}
	if len(errs) > 0 {
		return red, fmt.Errorf("redaction.extra_secret_keys: %s", strings.Join(errs, "; "))
	}
	return red, nil
}

// IsSecretKey reports whether a key is secret by the built-in or extra rules.
func (r *Redactor) IsSecretKey(k string) bool {
	if dockercli.IsSecretKey(k) {
		return true
	}
	if r == nil {
		return false
	}
	for _, re := range r.extra {
		if re.MatchString(k) {
			return true
		}
	}
	return false
}

func (r *Redactor) dropped(k string) bool {
	if r == nil {
		return false
	}
	for _, g := range r.drop {
		if ok, _ := path.Match(g, k); ok {
			return true
		}
	}
	return false
}

// Labels filters an already dockercli-filtered label map with the extra
// rules and returns the number of labels removed.
func (r *Redactor) Labels(in map[string]string) (map[string]string, uint32) {
	if r == nil || (len(r.extra) == 0 && len(r.drop) == 0) || len(in) == 0 {
		return in, 0
	}
	out := make(map[string]string, len(in))
	var n uint32
	for k, v := range in {
		if r.IsSecretKey(k) || r.dropped(k) {
			n++
			continue
		}
		out[k] = v
	}
	return out, n
}

// Args redacts command arguments (built-in shell-aware rules, then values
// following or attached to extra secret keys).
func (r *Redactor) Args(args []string) []string {
	out := dockercli.RedactShellArgs(args)
	if r == nil || len(r.extra) == 0 {
		return out
	}
	next := false
	for i, a := range out {
		if next {
			out[i] = "[REDACTED]"
			next = false
			continue
		}
		if eq := strings.IndexByte(a, '='); eq > 0 && r.IsSecretKey(strings.TrimLeft(a[:eq], "-")) {
			out[i] = a[:eq] + "=[REDACTED]"
			continue
		}
		if strings.HasPrefix(a, "-") && !strings.Contains(a, "=") && r.IsSecretKey(strings.TrimLeft(a, "-")) {
			next = true
		}
	}
	return out
}

// Text redacts free text.
func (r *Redactor) Text(s string) string { return dockercli.RedactText(s) }

// Container applies the extra rules to a converted container in place.
func (r *Redactor) Container(c *v1.Container) {
	if c == nil || r == nil {
		return
	}
	var n uint32
	c.Labels, n = r.Labels(c.Labels)
	c.RedactedLabelCount += n
	if len(r.extra) > 0 {
		c.Command = r.Args(c.Command)
		c.Entrypoint = r.Args(c.Entrypoint)
	}
}
