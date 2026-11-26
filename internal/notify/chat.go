package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// ---------------------------------------------------------------------------
// Discord
// ---------------------------------------------------------------------------

// DiscordHosts are the accepted webhook hosts.
var DiscordHosts = []string{"discord.com", "discordapp.com", "canary.discord.com", "ptb.discord.com"}

// Discord posts embeds to a channel webhook.
type Discord struct {
	WebhookURL string
	Client     *http.Client
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	URL         string         `json:"url,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
	Timestamp   string         `json:"timestamp,omitempty"`
	Footer      *struct {
		Text string `json:"text"`
	} `json:"footer,omitempty"`
}

type discordPayload struct {
	Username        string         `json:"username"`
	Content         string         `json:"content"`
	Embeds          []discordEmbed `json:"embeds"`
	AllowedMentions struct {
		Parse []string `json:"parse"`
	} `json:"allowed_mentions"`
}

// DiscordPayload renders the webhook JSON for m (at most 10 embeds).
func DiscordPayload(m Message) ([]byte, error) {
	p := discordPayload{Username: "Asterism", Content: truncate(m.Title(), 1900)}
	p.AllowedMentions.Parse = []string{}
	for i, a := range m.Alerts {
		if i == 10 {
			break
		}
		hexColor := severityHex(a.Severity, StateName(a.State))
		color, _ := strconv.ParseInt(strings.TrimPrefix(hexColor, "#"), 16, 32)
		project := a.ProjectID
		if project == "" {
			project = "—"
		}
		e := discordEmbed{
			Title:       truncate(strings.ToUpper(StateName(a.State))+" · "+a.RuleName, 250),
			Description: truncate(a.Summary, 2000),
			URL:         a.URL,
			Color:       int(color),
			Fields: []discordField{
				{Name: "Severity", Value: SeverityName(a.Severity), Inline: true},
				{Name: "Entity", Value: truncate(a.EntityID, 1000), Inline: true},
				{Name: "Project", Value: truncate(project, 1000), Inline: true},
			},
			Timestamp: rfc3339(a.FiringAt),
		}
		e.Footer = &struct {
			Text string `json:"text"`
		}{Text: "Asterism"}
		p.Embeds = append(p.Embeds, e)
	}
	return json.Marshal(p)
}

// Send posts m.
func (d *Discord) Send(ctx context.Context, m Message) Result {
	body, err := DiscordPayload(m)
	if err != nil {
		return Result{Err: err}
	}
	return postJSON(ctx, d.Client, d.WebhookURL, body, nil, d.WebhookURL)
}

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------

// TelegramAPI is the Bot API base URL.
const TelegramAPI = "https://api.telegram.org"

// Telegram sends HTML messages through the Bot API.
type Telegram struct {
	BotToken string
	ChatID   string
	Client   *http.Client
	// APIBase overrides TelegramAPI (tests).
	APIBase string
}

// TelegramText renders the HTML message body for m.
func TelegramText(m Message) string {
	var b strings.Builder
	b.WriteString("<b>" + html.EscapeString(m.Title()) + "</b>\n")
	for i, a := range m.Alerts {
		if i == 10 {
			fmt.Fprintf(&b, "\n…and %d more", len(m.Alerts)-10)
			break
		}
		line := html.EscapeString(a.Summary)
		if a.URL != "" {
			line = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(a.URL), line)
		}
		fmt.Fprintf(&b, "\n• [%s] %s\n  <code>%s</code>", SeverityName(a.Severity), line, html.EscapeString(a.EntityID))
	}
	return truncate(b.String(), 4000)
}

// Send posts m.
func (t *Telegram) Send(ctx context.Context, m Message) Result {
	base := t.APIBase
	if base == "" {
		base = TelegramAPI
	}
	body, err := json.Marshal(map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     TelegramText(m),
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return Result{Err: err}
	}
	return postJSON(ctx, t.Client, base+"/bot"+t.BotToken+"/sendMessage", body, nil, t.BotToken)
}

// ---------------------------------------------------------------------------
// Web Push payload (the service worker renders it)
// ---------------------------------------------------------------------------

// PushPayload is the JSON the service worker (web/public/sw.js) receives.
type PushPayload struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	URL      string `json:"url"`
	Tag      string `json:"tag"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	TS       int64  `json:"ts"`
}

// BuildPushPayload renders a compact payload (well under the 4 KB limit).
func BuildPushPayload(m Message) ([]byte, error) {
	var lines []string
	for i, a := range m.Alerts {
		if i == 4 {
			lines = append(lines, fmt.Sprintf("…and %d more", len(m.Alerts)-4))
			break
		}
		lines = append(lines, truncate(a.Summary, 160))
	}
	target := "/alerts"
	if m.Kind == KindTest {
		target = "/settings/notifications"
	}
	ts := m.SentAt
	if ts.IsZero() {
		ts = time.Now()
	}
	sev := SeverityName(m.MaxSeverity())
	if m.Status() == KindResolved {
		sev = "resolved"
	}
	return json.Marshal(PushPayload{
		Title:    truncate(m.Title(), 120),
		Body:     strings.Join(lines, "\n"),
		URL:      target,
		Tag:      "asterism:" + m.GroupKey,
		Severity: sev,
		Status:   m.Status(),
		TS:       ts.UnixMilli(),
	})
}

// pushUrgency maps severity onto the Web Push Urgency header.
func pushUrgency(s asterismv1.AlertSeverity) string {
	switch s {
	case asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL:
		return "high"
	case asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING:
		return "normal"
	default:
		return "low"
	}
}
