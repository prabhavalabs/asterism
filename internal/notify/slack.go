package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Slack posts Block Kit messages to an incoming webhook.
type Slack struct {
	WebhookURL string
	Client     *http.Client
}

// SlackHosts are the accepted incoming-webhook hosts.
var SlackHosts = []string{"hooks.slack.com", "hooks.slack-gov.com"}

// severityHex is the attachment colour per severity (Slack and Discord
// payloads, not UI styling).
func severityHex(s asterismv1.AlertSeverity, state string) string {
	if state == KindResolved {
		return "#2E9E6B"
	}
	switch s {
	case asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL:
		return "#E5484D"
	case asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING:
		return "#F5A524"
	default:
		return "#3B82F6"
	}
}

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

type slackAttachment struct {
	Color  string       `json:"color"`
	Blocks []slackBlock `json:"blocks"`
}

type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

// maxSlackAlerts caps the alerts rendered in one message (Slack allows 50 blocks).
const maxSlackAlerts = 10

// SlackPayload renders the Block Kit JSON for m.
func SlackPayload(m Message) ([]byte, error) {
	status := m.Status()
	blocks := []slackBlock{{
		Type: "header",
		Text: &slackText{Type: "plain_text", Text: truncate(m.Title(), 150)},
	}}
	for i, a := range m.Alerts {
		if i == maxSlackAlerts {
			blocks = append(blocks, slackBlock{Type: "context", Elements: []slackText{{Type: "mrkdwn", Text: fmt.Sprintf("…and %d more", len(m.Alerts)-maxSlackAlerts)}}})
			break
		}
		summary := slackEscape(a.Summary)
		if a.URL != "" {
			summary = fmt.Sprintf("<%s|%s>", a.URL, summary)
		}
		project := a.ProjectID
		if project == "" {
			project = "—"
		}
		stateLine := StateName(a.State)
		when := a.FiringAt
		if a.State == asterismv1.AlertState_ALERT_STATE_RESOLVED {
			when = a.EndsAt
		}
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: "*" + summary + "*"},
			Fields: []slackText{
				{Type: "mrkdwn", Text: "*Severity*\n" + SeverityName(a.Severity)},
				{Type: "mrkdwn", Text: "*State*\n" + stateLine},
				{Type: "mrkdwn", Text: "*Entity*\n`" + slackEscape(a.EntityID) + "`"},
				{Type: "mrkdwn", Text: "*Project*\n" + slackEscape(project)},
				{Type: "mrkdwn", Text: "*Rule*\n" + slackEscape(a.RuleName)},
				{Type: "mrkdwn", Text: "*Since*\n" + when.UTC().Format(time.RFC3339)},
			},
		})
	}
	footer := "Asterism"
	if m.HubURL != "" {
		footer += fmt.Sprintf(" · <%s/alerts|Open alerts>", m.HubURL)
	}
	blocks = append(blocks, slackBlock{Type: "context", Elements: []slackText{{Type: "mrkdwn", Text: footer}}})
	return json.Marshal(slackPayload{
		Text:        m.Title(),
		Attachments: []slackAttachment{{Color: severityHex(m.MaxSeverity(), status), Blocks: blocks}},
	})
}

// Send posts m.
func (s *Slack) Send(ctx context.Context, m Message) Result {
	body, err := SlackPayload(m)
	if err != nil {
		return Result{Err: err}
	}
	return postJSON(ctx, s.Client, s.WebhookURL, body, nil, s.WebhookURL)
}
