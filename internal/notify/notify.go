// Package notify formats and delivers alert notifications to channels:
// Slack, generic webhooks, SMTP email, Web Push, Discord, and Telegram.
// It holds no state; the alerting engine queues, retries, and logs.
package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Notification kinds.
const (
	KindFiring   = "firing"
	KindResolved = "resolved"
	KindTest     = "test"
)

// Notice is one alert inside a notification.
type Notice struct {
	ID         string
	RuleID     string
	RuleName   string
	Severity   asterismv1.AlertSeverity
	State      asterismv1.AlertState
	EntityID   string
	EntityName string
	ProjectID  string
	Summary    string
	Value      float64
	StartsAt   time.Time
	FiringAt   time.Time
	EndsAt     time.Time
	Labels     map[string]string
	// URL links to the alert in the hub ("" when server.public_url is unset).
	URL string
}

// Message is one notification: a group of alerts (same project, same kind)
// or a test.
type Message struct {
	Kind       string
	DeliveryID string
	GroupKey   string
	// Project name for the group ("" for host-level alerts).
	Project string
	Alerts  []Notice
	// HubURL is server.public_url ("" when unset).
	HubURL string
	SentAt time.Time
}

// Result is the outcome of one delivery attempt.
type Result struct {
	// HTTPStatus is the last HTTP status code (0 for SMTP or network errors).
	HTTPStatus int
	// Err is nil on success. Its text never contains channel secrets.
	Err error
	// Retryable reports whether another attempt may succeed (network
	// errors, 408, 429, 5xx).
	Retryable bool
	// RetryAfter is the server's requested delay (429/503), if any.
	RetryAfter time.Duration
}

// OK reports a successful delivery.
func (r Result) OK() bool { return r.Err == nil }

// Sender delivers messages to one channel.
type Sender interface {
	Send(ctx context.Context, m Message) Result
}

// SeverityName returns "info", "warning", or "critical".
func SeverityName(s asterismv1.AlertSeverity) string {
	switch s {
	case asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL:
		return "critical"
	case asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING:
		return "warning"
	default:
		return "info"
	}
}

// StateName returns "pending", "firing", or "resolved".
func StateName(s asterismv1.AlertState) string {
	switch s {
	case asterismv1.AlertState_ALERT_STATE_PENDING:
		return "pending"
	case asterismv1.AlertState_ALERT_STATE_FIRING:
		return "firing"
	case asterismv1.AlertState_ALERT_STATE_RESOLVED:
		return "resolved"
	default:
		return "inactive"
	}
}

// MaxSeverity is the highest severity in the message.
func (m Message) MaxSeverity() asterismv1.AlertSeverity {
	best := asterismv1.AlertSeverity_ALERT_SEVERITY_INFO
	for _, a := range m.Alerts {
		if a.Severity > best {
			best = a.Severity
		}
	}
	return best
}

// Status is "firing" when any alert fires, "resolved" otherwise, or "test".
func (m Message) Status() string {
	if m.Kind == KindTest {
		return KindTest
	}
	for _, a := range m.Alerts {
		if a.State == asterismv1.AlertState_ALERT_STATE_FIRING {
			return KindFiring
		}
	}
	return KindResolved
}

// Title is a one-line headline, e.g. "FIRING (2) shop: Container exited".
func (m Message) Title() string {
	if m.Kind == KindTest {
		return "Test notification from Asterism"
	}
	var b strings.Builder
	b.WriteString(strings.ToUpper(m.Status()))
	if len(m.Alerts) > 1 {
		fmt.Fprintf(&b, " (%d)", len(m.Alerts))
	}
	if m.Project != "" {
		b.WriteString(" " + m.Project)
	}
	b.WriteString(": ")
	if len(m.Alerts) > 0 {
		first := m.Alerts[0]
		b.WriteString(first.RuleName)
		if first.EntityName != "" {
			b.WriteString(" · " + first.EntityName)
		}
		if len(m.Alerts) > 1 {
			fmt.Fprintf(&b, " and %d more", len(m.Alerts)-1)
		}
	}
	return b.String()
}

// Subject is the short line stored in the delivery log.
func (m Message) Subject() string { return truncate(m.Title(), 200) }

// AlertIDs returns the fingerprints in the message.
func (m Message) AlertIDs() []string {
	out := make([]string, len(m.Alerts))
	for i, a := range m.Alerts {
		out[i] = a.ID
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// TestMessage returns the message sent by "Send test notification".
func TestMessage(hubURL, deliveryID string, now time.Time) Message {
	return Message{
		Kind:       KindTest,
		DeliveryID: deliveryID,
		GroupKey:   "test",
		HubURL:     hubURL,
		SentAt:     now,
		Alerts: []Notice{{
			ID:         "test",
			RuleID:     "test",
			RuleName:   "Test notification",
			Severity:   asterismv1.AlertSeverity_ALERT_SEVERITY_INFO,
			State:      asterismv1.AlertState_ALERT_STATE_FIRING,
			EntityID:   "host",
			EntityName: "host",
			Summary:    "If you can read this, the channel works.",
			StartsAt:   now,
			FiringAt:   now,
		}},
	}
}

// since renders a compact age ("5m", "2h", "3d").
func since(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
