package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// VAPIDKeys is the hub's Web Push application server key pair.
type VAPIDKeys struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

// LoadOrCreateVAPID reads <data_dir>/vapid.json, generating a P-256 key
// pair (mode 0600) on first use. Keep the file: browsers subscribed with
// the old public key stop receiving notifications if it changes.
func LoadOrCreateVAPID(path string) (VAPIDKeys, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var k VAPIDKeys
		if err := json.Unmarshal(raw, &k); err != nil || k.PublicKey == "" || k.PrivateKey == "" {
			return VAPIDKeys{}, fmt.Errorf("%s is not a valid VAPID key file", path)
		}
		return k, nil
	case errors.Is(err, fs.ErrNotExist):
	default:
		return VAPIDKeys{}, fmt.Errorf("read %s: %w", path, err)
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("generate VAPID keys: %w", err)
	}
	k := VAPIDKeys{PublicKey: pub, PrivateKey: priv}
	data, _ := json.MarshalIndent(k, "", "  ")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return VAPIDKeys{}, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return VAPIDKeys{}, fmt.Errorf("write %s: %w", path, err)
	}
	return k, f.Close()
}

// PushTarget is one browser subscription.
type PushTarget struct {
	ID       string
	Endpoint string
	P256dh   string
	Auth     string
}

// WebPush sends to every subscribed browser.
type WebPush struct {
	Keys       VAPIDKeys
	Subscriber string
	Client     *http.Client
	// Targets returns the current subscriptions.
	Targets func() []PushTarget
	// OnGone is called for subscriptions the push service reports as
	// expired (404/410); OnSuccess after a successful push.
	OnGone    func(id string)
	OnSuccess func(id string)
	// AllowPrivate relaxes endpoint validation (tests).
	AllowPrivate bool
}

// ErrNoSubscriptions is returned when no browser is subscribed.
var ErrNoSubscriptions = errors.New("no browser is subscribed; enable notifications on a device first")

// Send pushes m to every target. It succeeds when at least one push was
// accepted; failures are summarised without endpoint URLs.
func (w *WebPush) Send(ctx context.Context, m Message) Result {
	targets := w.Targets()
	if len(targets) == 0 {
		return Result{Err: ErrNoSubscriptions}
	}
	payload, err := BuildPushPayload(m)
	if err != nil {
		return Result{Err: err}
	}
	var sent, gone int
	var last Result
	var failures []string
	retryable := false
	for _, t := range targets {
		if err := ValidateTargetURL(t.Endpoint, w.AllowPrivate); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
			Endpoint: t.Endpoint,
			Keys:     webpush.Keys{P256dh: t.P256dh, Auth: t.Auth},
		}, &webpush.Options{
			HTTPClient:      w.Client,
			Subscriber:      w.Subscriber,
			VAPIDPublicKey:  w.Keys.PublicKey,
			VAPIDPrivateKey: w.Keys.PrivateKey,
			TTL:             3600,
			Urgency:         webpush.Urgency(pushUrgency(m.MaxSeverity())),
			Topic:           pushTopic(m.GroupKey),
		})
		if err != nil {
			failures = append(failures, scrubErr(err, t.Endpoint, t.Auth).Error())
			retryable = true
			continue
		}
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		_ = resp.Body.Close()
		r := classify(resp, snippet, t.Endpoint)
		last = r
		switch {
		case r.OK():
			sent++
			if w.OnSuccess != nil {
				w.OnSuccess(t.ID)
			}
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			gone++
			if w.OnGone != nil {
				w.OnGone(t.ID)
			}
		default:
			failures = append(failures, r.Err.Error())
			retryable = retryable || r.Retryable
		}
	}
	if sent > 0 {
		return Result{HTTPStatus: last.HTTPStatus}
	}
	msg := fmt.Sprintf("0 of %d browsers accepted the push", len(targets))
	if gone > 0 {
		msg += fmt.Sprintf("; %d expired subscriptions removed", gone)
	}
	if len(failures) > 0 {
		msg += ": " + strings.Join(failures[:min(3, len(failures))], "; ")
	}
	return Result{HTTPStatus: last.HTTPStatus, Err: errors.New(truncate(msg, 400)), Retryable: retryable, RetryAfter: last.RetryAfter}
}

// pushTopic derives a Topic header (max 32 URL-safe characters) so a newer
// notification for the same group replaces an undelivered one.
func pushTopic(group string) string {
	var b strings.Builder
	for _, r := range group {
		if b.Len() == 32 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
