package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookSchema identifies the JSON payload version (docs/BLUEPRINT.md §8.9).
const WebhookSchema = "asterism.notification.v1"

// Webhook header names.
const (
	HeaderSignature = "X-Asterism-Signature"
	HeaderEvent     = "X-Asterism-Event"
	HeaderDelivery  = "X-Asterism-Delivery"
	HeaderSchema    = "X-Asterism-Schema"
)

// Webhook posts the versioned JSON payload, optionally HMAC-signed.
type Webhook struct {
	URL           string
	SigningSecret string
	Headers       map[string]string
	Client        *http.Client
	// Now is the clock for the signature timestamp (time.Now when nil).
	Now func() time.Time
}

// WebhookAlert is one alert in the payload.
type WebhookAlert struct {
	ID         string            `json:"id"`
	Rule       string            `json:"rule"`
	RuleName   string            `json:"rule_name"`
	Severity   string            `json:"severity"`
	Status     string            `json:"status"`
	EntityID   string            `json:"entity_id"`
	EntityName string            `json:"entity_name"`
	ProjectID  string            `json:"project_id,omitempty"`
	Summary    string            `json:"summary"`
	Value      float64           `json:"value,omitempty"`
	StartsAt   string            `json:"starts_at"`
	FiringAt   string            `json:"firing_at,omitempty"`
	EndsAt     string            `json:"ends_at,omitempty"`
	URL        string            `json:"url,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// WebhookPayload is the documented v1 body.
type WebhookPayload struct {
	Schema     string         `json:"schema"`
	Type       string         `json:"type"`
	Status     string         `json:"status"`
	DeliveryID string         `json:"delivery_id"`
	GroupKey   string         `json:"group_key"`
	Project    string         `json:"project,omitempty"`
	Title      string         `json:"title"`
	HubURL     string         `json:"hub_url,omitempty"`
	SentAt     string         `json:"sent_at"`
	Alerts     []WebhookAlert `json:"alerts"`
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// BuildWebhookPayload renders the v1 JSON body for m.
func BuildWebhookPayload(m Message) ([]byte, error) {
	typ := "alert"
	if m.Kind == KindTest {
		typ = "test"
	}
	p := WebhookPayload{
		Schema:     WebhookSchema,
		Type:       typ,
		Status:     m.Status(),
		DeliveryID: m.DeliveryID,
		GroupKey:   m.GroupKey,
		Project:    m.Project,
		Title:      m.Title(),
		HubURL:     m.HubURL,
		SentAt:     rfc3339(m.SentAt),
		Alerts:     make([]WebhookAlert, 0, len(m.Alerts)),
	}
	for _, a := range m.Alerts {
		p.Alerts = append(p.Alerts, WebhookAlert{
			ID: a.ID, Rule: a.RuleID, RuleName: a.RuleName, Severity: SeverityName(a.Severity),
			Status: StateName(a.State), EntityID: a.EntityID, EntityName: a.EntityName,
			ProjectID: a.ProjectID, Summary: a.Summary, Value: a.Value,
			StartsAt: rfc3339(a.StartsAt), FiringAt: rfc3339(a.FiringAt), EndsAt: rfc3339(a.EndsAt),
			URL: a.URL, Labels: a.Labels,
		})
	}
	return json.Marshal(p)
}

// Sign returns the X-Asterism-Signature value: t=<unix>,v1=<hex
// HMAC-SHA256(secret, "<unix>." + body)>.
func Sign(secret string, ts time.Time, body []byte) string {
	t := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks a signature header against body, rejecting
// timestamps older than tolerance (receivers should do the same).
func VerifySignature(secret, header string, body []byte, now time.Time, tolerance time.Duration) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return false
	}
	at := time.Unix(unix, 0)
	if d := now.Sub(at); d > tolerance || d < -tolerance {
		return false
	}
	want := Sign(secret, at, body)
	_, wantSig, _ := strings.Cut(want, ",v1=")
	got, err1 := hex.DecodeString(sig)
	exp, err2 := hex.DecodeString(wantSig)
	return err1 == nil && err2 == nil && hmac.Equal(got, exp)
}

// reservedHeaders cannot be overridden by custom headers.
var reservedHeaders = map[string]bool{
	"content-type": true, "content-length": true, "host": true, "user-agent": true,
	"x-asterism-signature": true, "x-asterism-event": true, "x-asterism-delivery": true, "x-asterism-schema": true,
}

// ReservedHeader reports whether a custom header name is refused.
func ReservedHeader(name string) bool {
	return reservedHeaders[strings.ToLower(strings.TrimSpace(name))]
}

// Send posts m.
func (w *Webhook) Send(ctx context.Context, m Message) Result {
	body, err := BuildWebhookPayload(m)
	if err != nil {
		return Result{Err: err}
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	typ := "alert"
	if m.Kind == KindTest {
		typ = "test"
	}
	headers := map[string]string{}
	secrets := []string{w.URL, w.SigningSecret}
	for k, v := range w.Headers {
		if !ReservedHeader(k) {
			headers[k] = v
			secrets = append(secrets, v)
		}
	}
	headers[HeaderEvent] = typ
	headers[HeaderDelivery] = m.DeliveryID
	headers[HeaderSchema] = WebhookSchema
	if w.SigningSecret != "" {
		headers[HeaderSignature] = Sign(w.SigningSecret, now(), body)
	}
	return postJSON(ctx, w.Client, w.URL, body, headers, secrets...)
}
