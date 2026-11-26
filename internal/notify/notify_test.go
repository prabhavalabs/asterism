package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

var update = flag.Bool("update", false, "rewrite golden files")

var fixedNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func sampleMessage() Message {
	return Message{
		Kind: KindFiring, DeliveryID: "d-123", GroupKey: "proj:shop", Project: "shop",
		HubURL: "https://monitor.example.com", SentAt: fixedNow,
		Alerts: []Notice{
			{ID: "container_exited|ctr:shop-api-1", RuleID: "container_exited", RuleName: "Container exited unexpectedly",
				Severity: asterismv1.AlertSeverity_ALERT_SEVERITY_CRITICAL, State: asterismv1.AlertState_ALERT_STATE_FIRING,
				EntityID: "ctr:shop-api-1", EntityName: "shop-api-1", ProjectID: "proj:shop", Summary: "Container shop-api-1 exited with code 1 <&>",
				StartsAt: fixedNow.Add(-2 * time.Minute), FiringAt: fixedNow.Add(-time.Minute), Labels: map[string]string{"exit_code": "1"},
				URL: "https://monitor.example.com/alerts?alert=container_exited%7Cctr%3Ashop-api-1"},
			{ID: "container_unhealthy|ctr:shop-worker-1", RuleID: "container_unhealthy", RuleName: "Health check failing",
				Severity: asterismv1.AlertSeverity_ALERT_SEVERITY_WARNING, State: asterismv1.AlertState_ALERT_STATE_FIRING,
				EntityID: "ctr:shop-worker-1", EntityName: "shop-worker-1", ProjectID: "proj:shop", Summary: "Health check failing",
				StartsAt: fixedNow.Add(-3 * time.Minute), FiringAt: fixedNow.Add(-time.Minute)},
		},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/notify -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func indent(t *testing.T, raw []byte) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	return append(buf.Bytes(), '\n')
}

func TestGoldenPayloads(t *testing.T) {
	m := sampleMessage()
	slack, err := SlackPayload(m)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "slack.golden.json", indent(t, slack))
	wh, err := BuildWebhookPayload(m)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "webhook.golden.json", indent(t, wh))
	dc, err := DiscordPayload(m)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "discord.golden.json", indent(t, dc))
	golden(t, "telegram.golden.txt", []byte(TelegramText(m)+"\n"))
	push, err := BuildPushPayload(m)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "webpush.golden.json", indent(t, push))
	mail, err := BuildEmail(m, "Asterism <alerts@example.com>", []string{"ops@example.com"}, "BOUNDARY")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "email.golden.eml", mail)

	resolved := m
	resolved.Kind = KindResolved
	resolved.Alerts = []Notice{m.Alerts[0]}
	resolved.Alerts[0].State = asterismv1.AlertState_ALERT_STATE_RESOLVED
	resolved.Alerts[0].EndsAt = fixedNow
	if resolved.Title() != "RESOLVED shop: Container exited unexpectedly · shop-api-1" {
		t.Fatalf("resolved title = %q", resolved.Title())
	}
	if m.Title() != "FIRING (2) shop: Container exited unexpectedly · shop-api-1 and 1 more" {
		t.Fatalf("title = %q", m.Title())
	}
}

func TestSignatureRoundTrip(t *testing.T) {
	body := []byte(`{"a":1}`)
	sig := Sign("s3cret-signing-key", fixedNow, body)
	want := "t=1795694400,v1="
	if !strings.HasPrefix(sig, want) {
		t.Fatalf("sig = %s", sig)
	}
	if !VerifySignature("s3cret-signing-key", sig, body, fixedNow.Add(time.Minute), 5*time.Minute) {
		t.Fatal("valid signature rejected")
	}
	for name, ok := range map[string]bool{
		"tampered body": VerifySignature("s3cret-signing-key", sig, []byte(`{"a":2}`), fixedNow, 5*time.Minute),
		"wrong secret":  VerifySignature("other", sig, body, fixedNow, 5*time.Minute),
		"too old":       VerifySignature("s3cret-signing-key", sig, body, fixedNow.Add(10*time.Minute), 5*time.Minute),
		"garbage":       VerifySignature("s3cret-signing-key", "v1=zz", body, fixedNow, 5*time.Minute),
	} {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestWebhookDeliveryHeadersAndSignature(t *testing.T) {
	type got struct {
		header http.Header
		body   []byte
	}
	ch := make(chan got, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{r.Header.Clone(), b}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	wh := &Webhook{URL: srv.URL + "/hook?token=abc", SigningSecret: "s3cret-signing-key", Headers: map[string]string{"Authorization": "Bearer xyz", "X-Asterism-Event": "spoof"},
		Client: NewHTTPClient(true), Now: func() time.Time { return fixedNow }}
	res := wh.Send(context.Background(), sampleMessage())
	if !res.OK() || res.HTTPStatus != 204 {
		t.Fatalf("send = %+v", res)
	}
	g := <-ch
	if g.header.Get(HeaderEvent) != "alert" || g.header.Get(HeaderDelivery) != "d-123" || g.header.Get(HeaderSchema) != WebhookSchema {
		t.Fatalf("headers = %v", g.header)
	}
	if g.header.Get("Authorization") != "Bearer xyz" {
		t.Fatal("custom header missing")
	}
	if !VerifySignature("s3cret-signing-key", g.header.Get(HeaderSignature), g.body, fixedNow, time.Minute) {
		t.Fatalf("bad signature %q", g.header.Get(HeaderSignature))
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("bad token supersecrettoken123 in " + r.URL.Path))
	}))
	defer srv.Close()
	sec := "supersecrettoken123"
	s := &Slack{WebhookURL: srv.URL + "/services/" + sec, Client: NewHTTPClient(true)}
	res := s.Send(context.Background(), sampleMessage())
	if res.OK() || !res.Retryable || strings.Contains(res.Err.Error(), sec) {
		t.Fatalf("result = %+v", res)
	}
	// Network error: the url.Error must not echo the webhook path.
	tg := &Telegram{BotToken: "123456:" + sec + "AAAAAAAA", ChatID: "1", Client: NewHTTPClient(true), APIBase: "http://127.0.0.1:1"}
	res = tg.Send(context.Background(), sampleMessage())
	if res.OK() || strings.Contains(res.Err.Error(), sec) {
		t.Fatalf("telegram error leaks token: %v", res.Err)
	}
}

func TestSSRFGuard(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "::1", "fe80::1", "fd00::1", "100.64.0.1", "0.0.0.0", "::ffff:127.0.0.1"} {
		if !BlockedIP(netip.MustParseAddr(ip)) {
			t.Errorf("%s not blocked", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if BlockedIP(netip.MustParseAddr(ip)) {
			t.Errorf("%s blocked", ip)
		}
	}
	cases := map[string]bool{ // url -> valid without allowPrivate
		"https://example.com/hook":        true,
		"http://example.com/hook":         false,
		"https://127.0.0.1/hook":          false,
		"https://[::1]/hook":              false,
		"https://169.254.169.254/latest/": false,
		"https://localhost/x":             false,
		"ftp://example.com":               false,
		"not a url":                       false,
	}
	for u, want := range cases {
		if got := ValidateTargetURL(u, false) == nil; got != want {
			t.Errorf("ValidateTargetURL(%q) ok=%v, want %v", u, got, want)
		}
	}
	if ValidateTargetURL("https://evil.example.com/services/x", false, SlackHosts...) == nil {
		t.Error("non-Slack host accepted for Slack")
	}
	if ValidateTargetURL("http://127.0.0.1:9/x", true) != nil {
		t.Error("allowPrivate should accept loopback http")
	}
	// Dial-time guard: a hostname resolving to loopback is refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	res := postJSON(context.Background(), NewHTTPClient(false), "http://localhost:"+port+"/", []byte("{}"), nil)
	if res.OK() || !errors.Is(res.Err, ErrBlockedAddress) || res.Retryable {
		t.Fatalf("guarded client reached loopback: %+v", res)
	}
	// Redirects are not followed.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	defer redir.Close()
	res = postJSON(context.Background(), NewHTTPClient(true), redir.URL, []byte("{}"), nil)
	if res.OK() || res.HTTPStatus != 302 {
		t.Fatalf("redirect followed: %+v", res)
	}
}

// fakeSMTP is a minimal SMTP server (EHLO, AUTH PLAIN, MAIL, RCPT, DATA).
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	auth string
	rcpt []string
	data string
	code int // reply to RCPT (250 default)
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, code: 250}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := func(s string) { _, _ = fmt.Fprintf(c, "%s\r\n", s) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			w("250-fake")
			w("250 AUTH PLAIN")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			f.mu.Lock()
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			f.auth = string(raw)
			f.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(up, "MAIL FROM"):
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, cmd)
			code := f.code
			f.mu.Unlock()
			w(fmt.Sprintf("%d rcpt", code))
		case up == "DATA":
			w("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			w("250 queued")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestEmailOverFakeSMTP(t *testing.T) {
	f := newFakeSMTP(t)
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	var p int
	_, _ = fmt.Sscan(port, &p)
	e := &Email{Host: "127.0.0.1", Port: p, Security: asterismv1.SmtpSecurity_SMTP_SECURITY_NONE, Username: "bot", Password: "smtp-pass-123",
		From: "Asterism <alerts@example.com>", To: []string{"ops@example.com", "oncall@example.com"}}
	if res := e.Send(context.Background(), sampleMessage()); !res.OK() {
		t.Fatal(res.Err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "\x00bot\x00smtp-pass-123" || len(f.rcpt) != 2 {
		t.Fatalf("auth=%q rcpt=%v", f.auth, f.rcpt)
	}
	for _, want := range []string{"Subject: =?utf-8?q?", "multipart/alternative", "text/plain", "text/html", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(f.data, want) {
			t.Errorf("message lacks %q", want)
		}
	}
	f.code = 550
	f.rcpt = nil
	f.mu.Unlock()
	res := e.Send(context.Background(), sampleMessage())
	f.mu.Lock()
	if res.OK() || res.Retryable || strings.Contains(res.Err.Error(), "smtp-pass-123") {
		t.Fatalf("5xx should be permanent: %+v", res)
	}
	// STARTTLS required but not offered.
	f.code = 250
	e.Security = asterismv1.SmtpSecurity_SMTP_SECURITY_STARTTLS
	f.mu.Unlock()
	res = e.Send(context.Background(), sampleMessage())
	f.mu.Lock()
	if res.OK() || !strings.Contains(res.Err.Error(), "STARTTLS") {
		t.Fatalf("starttls downgrade accepted: %+v", res)
	}
}

func TestValidateChannels(t *testing.T) {
	slack := &asterismv1.ChannelConfig{Kind: &asterismv1.ChannelConfig_Slack{Slack: &asterismv1.SlackConfig{}}}
	if err := Validate(asterismv1.ChannelType_CHANNEL_TYPE_SLACK, slack, Secrets{WebhookURL: "https://hooks.slack.com/services/T/B/x"}, false); err != nil {
		t.Fatal(err)
	}
	if Validate(asterismv1.ChannelType_CHANNEL_TYPE_SLACK, slack, Secrets{WebhookURL: "https://example.com/x"}, false) == nil {
		t.Fatal("non-slack host accepted")
	}
	if Validate(asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK, slack, Secrets{URL: "https://example.com"}, false) == nil {
		t.Fatal("config/type mismatch accepted")
	}
	wh := &asterismv1.ChannelConfig{Kind: &asterismv1.ChannelConfig_Webhook{Webhook: &asterismv1.WebhookConfig{}}}
	for _, bad := range []Secrets{
		{URL: "https://example.com", Headers: map[string]string{"X-Asterism-Signature": "x"}},
		{URL: "https://example.com", Headers: map[string]string{"X-A": "a\r\nInjected: 1"}},
		{URL: "https://example.com", SigningSecret: "short"},
	} {
		if Validate(asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK, wh, bad, false) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	email := &asterismv1.ChannelConfig{Kind: &asterismv1.ChannelConfig_Email{Email: &asterismv1.EmailConfig{Host: "smtp.example.com", Port: 25, Security: asterismv1.SmtpSecurity_SMTP_SECURITY_NONE, From: "a@example.com", To: []string{"b@example.com"}}}}
	if Validate(asterismv1.ChannelType_CHANNEL_TYPE_EMAIL, email, Secrets{}, false) == nil {
		t.Fatal("plain SMTP to a remote host accepted")
	}
	email.GetEmail().Security = asterismv1.SmtpSecurity_SMTP_SECURITY_STARTTLS
	if err := Validate(asterismv1.ChannelType_CHANNEL_TYPE_EMAIL, email, Secrets{}, false); err != nil {
		t.Fatal(err)
	}
}

func TestSplitMergeSecrets(t *testing.T) {
	cfg := &asterismv1.ChannelConfig{Kind: &asterismv1.ChannelConfig_Webhook{Webhook: &asterismv1.WebhookConfig{
		Url: "https://example.com/x", SigningSecret: "0123456789abcdef", Headers: map[string]string{"Authorization": "Bearer t"}}}}
	s := SplitSecrets(cfg)
	w := cfg.GetWebhook()
	if w.Url != "" || w.SigningSecret != "" || len(w.Headers) != 0 || s.URL == "" || s.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("split left secrets: %v / %+v", cfg, s)
	}
	merged, err := MergeSecrets(s, Secrets{}, nil)
	if err != nil || merged.URL != s.URL || merged.SigningSecret != s.SigningSecret {
		t.Fatalf("empty update must keep secrets: %+v", merged)
	}
	merged, _ = MergeSecrets(s, Secrets{Headers: map[string]string{"Authorization": "", "X-New": "v"}}, []string{"signing_secret"})
	if merged.SigningSecret != "" || merged.Headers["Authorization"] != "Bearer t" || merged.Headers["X-New"] != "v" {
		t.Fatalf("merge = %+v", merged)
	}
	if _, err := MergeSecrets(s, Secrets{}, []string{"url"}); err == nil {
		t.Fatal("required secret cleared")
	}
	st := SecretStatuses(asterismv1.ChannelType_CHANNEL_TYPE_WEBHOOK, s)
	for _, x := range st {
		if strings.Contains(x.Hint, "Bearer") || strings.Contains(x.Hint, "/x") || strings.Contains(x.Hint, "0123") {
			t.Fatalf("hint leaks: %v", x)
		}
	}
}

// TestWebPushEncryptsForBrowser decrypts the RFC 8291 aes128gcm body the
// way a user agent does and checks the VAPID header.
func TestWebPushEncryptsForBrowser(t *testing.T) {
	keys, err := LoadOrCreateVAPID(filepath.Join(t.TempDir(), "vapid.json"))
	if err != nil {
		t.Fatal(err)
	}
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	_, _ = rand.Read(authSecret)
	type got struct {
		h    http.Header
		body []byte
	}
	ch := make(chan got, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{r.Header.Clone(), b}
		w.WriteHeader(201)
	}))
	defer srv.Close()
	gone := ""
	wp := &WebPush{Keys: keys, Subscriber: "https://monitor.example.com", Client: NewHTTPClient(true), AllowPrivate: true,
		Targets: func() []PushTarget {
			return []PushTarget{{ID: "p1", Endpoint: srv.URL + "/push/abc", P256dh: base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(authSecret)}}
		},
		OnGone: func(id string) { gone = id }}
	if res := wp.Send(context.Background(), sampleMessage()); !res.OK() {
		t.Fatal(res.Err)
	}
	g := <-ch
	if g.h.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(g.h.Get("Authorization"), "vapid t=") || g.h.Get("Urgency") != "high" {
		t.Fatalf("headers = %v", g.h)
	}
	plain := decryptAES128GCM(t, g.body, uaPriv, authSecret)
	var p PushPayload
	if err := json.Unmarshal(plain, &p); err != nil || p.URL != "/alerts" || p.Severity != "critical" {
		t.Fatalf("payload = %s (%v)", plain, err)
	}
	// 410 Gone removes the subscription.
	gs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(410) }))
	defer gs.Close()
	wp.Targets = func() []PushTarget {
		return []PushTarget{{ID: "p2", Endpoint: gs.URL, P256dh: base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(authSecret)}}
	}
	if res := wp.Send(context.Background(), sampleMessage()); res.OK() || gone != "p2" {
		t.Fatalf("gone handling: %+v gone=%q", res, gone)
	}
	keys2, _ := LoadOrCreateVAPID(filepath.Join(filepath.Dir(filepath.Join(t.TempDir(), "x")), "v.json"))
	if keys2.PublicKey == keys.PublicKey {
		t.Fatal("keys not random")
	}
}
