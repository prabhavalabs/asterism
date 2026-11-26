package notify

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Secrets holds a channel's write-only fields. It is sealed with
// secretbox before storage and never returned by an API.
type Secrets struct {
	WebhookURL    string            `json:"webhook_url,omitempty"`
	URL           string            `json:"url,omitempty"`
	SigningSecret string            `json:"signing_secret,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Password      string            `json:"password,omitempty"`
	BotToken      string            `json:"bot_token,omitempty"`
}

// Values lists every secret string (for redaction).
func (s Secrets) Values() []string {
	out := []string{s.WebhookURL, s.URL, s.SigningSecret, s.Password, s.BotToken}
	for _, v := range s.Headers {
		out = append(out, v)
	}
	return out
}

// TypeName is the lowercase channel type ("slack").
func TypeName(t asterismv1.ChannelType) string {
	return strings.ToLower(strings.TrimPrefix(t.String(), "CHANNEL_TYPE_"))
}

// ConfigType returns the channel type a config describes.
func ConfigType(c *asterismv1.ChannelConfig) asterismv1.ChannelType {
	switch c.GetKind().(type) {
	case *asterismv1.ChannelConfig_Slack:
		return asterismv1.ChannelType_CHANNEL_TYPE_SLACK
	case *asterismv1.ChannelConfig_Webhook:
		return asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK
	case *asterismv1.ChannelConfig_Email:
		return asterismv1.ChannelType_CHANNEL_TYPE_EMAIL
	case *asterismv1.ChannelConfig_Webpush:
		return asterismv1.ChannelType_CHANNEL_TYPE_WEBPUSH
	case *asterismv1.ChannelConfig_Discord:
		return asterismv1.ChannelType_CHANNEL_TYPE_DISCORD
	case *asterismv1.ChannelConfig_Telegram:
		return asterismv1.ChannelType_CHANNEL_TYPE_TELEGRAM
	}
	return asterismv1.ChannelType_CHANNEL_TYPE_UNSPECIFIED
}

// SplitSecrets moves the secret fields out of cfg (clearing them in place)
// and returns them.
func SplitSecrets(cfg *asterismv1.ChannelConfig) Secrets {
	var s Secrets
	switch k := cfg.GetKind().(type) {
	case *asterismv1.ChannelConfig_Slack:
		s.WebhookURL, k.Slack.WebhookUrl = strings.TrimSpace(k.Slack.GetWebhookUrl()), ""
	case *asterismv1.ChannelConfig_Discord:
		s.WebhookURL, k.Discord.WebhookUrl = strings.TrimSpace(k.Discord.GetWebhookUrl()), ""
	case *asterismv1.ChannelConfig_Webhook:
		s.URL, k.Webhook.Url = strings.TrimSpace(k.Webhook.GetUrl()), ""
		s.SigningSecret, k.Webhook.SigningSecret = k.Webhook.GetSigningSecret(), ""
		if len(k.Webhook.GetHeaders()) > 0 {
			s.Headers = map[string]string{}
			for name, v := range k.Webhook.GetHeaders() {
				s.Headers[strings.TrimSpace(name)] = v
			}
		}
		k.Webhook.Headers = nil
		k.Webhook.HeaderNames = nil
	case *asterismv1.ChannelConfig_Email:
		s.Password, k.Email.Password = k.Email.GetPassword(), ""
	case *asterismv1.ChannelConfig_Telegram:
		s.BotToken, k.Telegram.BotToken = strings.TrimSpace(k.Telegram.GetBotToken()), ""
	}
	return s
}

// secretFields are the clearable optional secrets per type.
var optionalSecrets = map[string]bool{"signing_secret": true, "password": true}

// MergeSecrets applies an update: non-empty new values replace old ones,
// empty values keep them, and clear removes optional fields
// ("signing_secret", "password", or "headers.<Name>"). Webhook headers in
// next replace the stored header set when next names any header; a header
// given with an empty value keeps its stored value.
func MergeSecrets(old, next Secrets, clear []string) (Secrets, error) {
	out := old
	keep := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	keep(&out.WebhookURL, next.WebhookURL)
	keep(&out.URL, next.URL)
	keep(&out.SigningSecret, next.SigningSecret)
	keep(&out.Password, next.Password)
	keep(&out.BotToken, next.BotToken)
	if next.Headers != nil {
		merged := map[string]string{}
		for name, v := range next.Headers {
			if v == "" {
				v = old.Headers[name]
			}
			merged[name] = v
		}
		out.Headers = merged
	}
	for _, f := range clear {
		switch {
		case f == "signing_secret":
			out.SigningSecret = ""
		case f == "password":
			out.Password = ""
		case strings.HasPrefix(f, "headers."):
			delete(out.Headers, strings.TrimPrefix(f, "headers."))
		default:
			if !optionalSecrets[f] {
				return Secrets{}, fmt.Errorf("secret %q cannot be cleared (it is required or unknown)", f)
			}
		}
	}
	return out, nil
}

func hint(v string) string {
	if v == "" {
		return ""
	}
	if len(v) >= 16 {
		return "••••" + v[len(v)-4:]
	}
	return "••••"
}

// SecretStatuses describes the stored secrets of a channel type.
func SecretStatuses(t asterismv1.ChannelType, s Secrets) []*asterismv1.SecretStatus {
	st := func(field, v, h string) *asterismv1.SecretStatus {
		if v == "" {
			h = ""
		}
		return &asterismv1.SecretStatus{Field: field, HasValue: v != "", Hint: h}
	}
	switch t {
	case asterismv1.ChannelType_CHANNEL_TYPE_SLACK, asterismv1.ChannelType_CHANNEL_TYPE_DISCORD:
		return []*asterismv1.SecretStatus{st("webhook_url", s.WebhookURL, URLHint(s.WebhookURL))}
	case asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK:
		out := []*asterismv1.SecretStatus{st("url", s.URL, URLHint(s.URL)), st("signing_secret", s.SigningSecret, "••••")}
		names := make([]string, 0, len(s.Headers))
		for n := range s.Headers {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			out = append(out, st("headers."+n, s.Headers[n], "••••"))
		}
		return out
	case asterismv1.ChannelType_CHANNEL_TYPE_EMAIL:
		return []*asterismv1.SecretStatus{st("password", s.Password, "••••")}
	case asterismv1.ChannelType_CHANNEL_TYPE_TELEGRAM:
		return []*asterismv1.SecretStatus{st("bot_token", s.BotToken, hint(s.BotToken))}
	}
	return nil
}

// HeaderNames returns the sorted custom header names.
func (s Secrets) HeaderNames() []string {
	names := make([]string, 0, len(s.Headers))
	for n := range s.Headers {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

var (
	headerName  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	telegramTok = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{20,100}$`)
	telegramID  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{4,64})$`)
)

// Validate checks a channel's settings and secrets for its type.
func Validate(t asterismv1.ChannelType, cfg *asterismv1.ChannelConfig, s Secrets, allowPrivate bool) error {
	if ConfigType(cfg) != t {
		return fmt.Errorf("config does not match channel type %s", TypeName(t))
	}
	switch t {
	case asterismv1.ChannelType_CHANNEL_TYPE_SLACK:
		if err := ValidateTargetURL(s.WebhookURL, allowPrivate, SlackHosts...); err != nil {
			return fmt.Errorf("webhook_url: %w", err)
		}
	case asterismv1.ChannelType_CHANNEL_TYPE_DISCORD:
		if err := ValidateTargetURL(s.WebhookURL, allowPrivate, DiscordHosts...); err != nil {
			return fmt.Errorf("webhook_url: %w", err)
		}
	case asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK:
		if err := ValidateTargetURL(s.URL, allowPrivate); err != nil {
			return fmt.Errorf("url: %w", err)
		}
		if len(s.Headers) > 10 {
			return errors.New("at most 10 custom headers")
		}
		for n, v := range s.Headers {
			if !headerName.MatchString(n) || ReservedHeader(n) {
				return fmt.Errorf("header %q is not allowed", n)
			}
			if v == "" || len(v) > 2048 || strings.ContainsAny(v, "\r\n") {
				return fmt.Errorf("header %q needs a single-line value", n)
			}
		}
		if len(s.SigningSecret) > 0 && len(s.SigningSecret) < 16 {
			return errors.New("signing_secret must be at least 16 characters")
		}
	case asterismv1.ChannelType_CHANNEL_TYPE_EMAIL:
		if err := ValidateEmailConfig(cfg.GetEmail(), allowPrivate); err != nil {
			return err
		}
		if cfg.GetEmail().GetUsername() != "" && s.Password == "" {
			return errors.New("password is required when a username is set")
		}
	case asterismv1.ChannelType_CHANNEL_TYPE_TELEGRAM:
		if !telegramTok.MatchString(s.BotToken) {
			return errors.New("bot_token must look like 123456:ABC…")
		}
		if !telegramID.MatchString(strings.TrimSpace(cfg.GetTelegram().GetChatId())) {
			return errors.New("chat_id must be a numeric id or @channel")
		}
	case asterismv1.ChannelType_CHANNEL_TYPE_WEBPUSH:
	default:
		return errors.New("unknown channel type")
	}
	return nil
}

// Deps are shared dependencies for senders.
type Deps struct {
	Client       *http.Client
	AllowPrivate bool
	// WebPush is the template for Web Push senders (nil disables them).
	WebPush *WebPush
	// TelegramAPI overrides the Bot API base (tests).
	TelegramAPI string
}

// Build returns the sender for a stored channel.
func Build(ch *asterismv1.NotificationChannel, s Secrets, d Deps) (Sender, error) {
	cfg := ch.GetConfig()
	switch ch.GetType() {
	case asterismv1.ChannelType_CHANNEL_TYPE_SLACK:
		return &Slack{WebhookURL: s.WebhookURL, Client: d.Client}, nil
	case asterismv1.ChannelType_CHANNEL_TYPE_DISCORD:
		return &Discord{WebhookURL: s.WebhookURL, Client: d.Client}, nil
	case asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK:
		return &Webhook{URL: s.URL, SigningSecret: s.SigningSecret, Headers: s.Headers, Client: d.Client}, nil
	case asterismv1.ChannelType_CHANNEL_TYPE_TELEGRAM:
		return &Telegram{BotToken: s.BotToken, ChatID: strings.TrimSpace(cfg.GetTelegram().GetChatId()), Client: d.Client, APIBase: d.TelegramAPI}, nil
	case asterismv1.ChannelType_CHANNEL_TYPE_EMAIL:
		e := cfg.GetEmail()
		return &Email{Host: strings.TrimSpace(e.GetHost()), Port: int(e.GetPort()), Security: e.GetSecurity(), Username: e.GetUsername(), Password: s.Password, From: e.GetFrom(), To: e.GetTo()}, nil
	case asterismv1.ChannelType_CHANNEL_TYPE_WEBPUSH:
		if d.WebPush == nil {
			return nil, errors.New("web push is not available")
		}
		return d.WebPush, nil
	}
	return nil, fmt.Errorf("unsupported channel type %s", TypeName(ch.GetType()))
}
