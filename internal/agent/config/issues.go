package config

import (
	"fmt"
	"sort"
	"strings"
)

// Severity classifies an Issue.
type Severity int

// Issue severities.
const (
	SeverityError Severity = iota
	SeverityWarning
)

// Issue is one actionable configuration problem.
type Issue struct {
	Severity Severity
	// File is the config file name ("" for environment or defaults).
	File string
	// Line is the 1-based YAML line (0 when unknown).
	Line int
	// Path is the dotted key path, e.g. "hub.tls.pin_sha256[0]".
	Path    string
	Message string
}

// String formats the issue as "file:line: path: message".
func (i Issue) String() string {
	var b strings.Builder
	if i.File != "" {
		b.WriteString(i.File)
		if i.Line > 0 {
			fmt.Fprintf(&b, ":%d", i.Line)
		}
		b.WriteString(": ")
	}
	if i.Severity == SeverityWarning {
		b.WriteString("warning: ")
	}
	if i.Path != "" {
		b.WriteString(i.Path)
		b.WriteString(": ")
	}
	b.WriteString(i.Message)
	return b.String()
}

// Issues is a list of problems. As an error it reports only the errors.
type Issues []Issue

// Errors returns the error-severity issues.
func (is Issues) Errors() Issues {
	var out Issues
	for _, i := range is {
		if i.Severity == SeverityError {
			out = append(out, i)
		}
	}
	return out
}

// Warnings returns the warning-severity issues.
func (is Issues) Warnings() Issues {
	var out Issues
	for _, i := range is {
		if i.Severity == SeverityWarning {
			out = append(out, i)
		}
	}
	return out
}

// HasErrors reports whether any issue is an error.
func (is Issues) HasErrors() bool { return len(is.Errors()) > 0 }

// Error implements error.
func (is Issues) Error() string {
	errs := is.Errors()
	lines := make([]string, len(errs))
	for i, e := range errs {
		lines[i] = e.String()
	}
	if len(lines) == 1 {
		return lines[0]
	}
	return fmt.Sprintf("%d configuration errors:\n  %s", len(lines), strings.Join(lines, "\n  "))
}

// sorted orders issues by line, then path, for stable output.
func (is Issues) sorted() Issues {
	out := append(Issues(nil), is...)
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Severity != out[b].Severity {
			return out[a].Severity < out[b].Severity
		}
		if out[a].Line != out[b].Line {
			return out[a].Line < out[b].Line
		}
		return out[a].Path < out[b].Path
	})
	return out
}
