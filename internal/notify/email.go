package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/netip"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Email delivers multipart text+HTML messages over SMTP.
type Email struct {
	Host     string
	Port     int
	Security asterismv1.SmtpSecurity
	Username string
	Password string
	From     string
	To       []string
	// TLSConfig overrides the TLS settings (tests).
	TLSConfig *tls.Config
}

// ValidateEmailConfig checks non-secret SMTP settings.
func ValidateEmailConfig(c *asterismv1.EmailConfig, allowPrivate bool) error {
	host := strings.TrimSpace(c.GetHost())
	if host == "" || strings.ContainsAny(host, " /\r\n") {
		return errors.New("SMTP host is required")
	}
	if c.GetPort() == 0 || c.GetPort() > 65535 {
		return errors.New("SMTP port must be 1-65535")
	}
	switch c.GetSecurity() {
	case asterismv1.SmtpSecurity_SMTP_SECURITY_STARTTLS, asterismv1.SmtpSecurity_SMTP_SECURITY_TLS:
	case asterismv1.SmtpSecurity_SMTP_SECURITY_NONE:
		if !allowPrivate && !isLoopbackHost(host) {
			return errors.New("plain SMTP is only allowed to a loopback relay; use STARTTLS or TLS")
		}
	default:
		return errors.New("choose STARTTLS, TLS, or none")
	}
	if _, err := mail.ParseAddress(c.GetFrom()); err != nil {
		return fmt.Errorf("invalid from address: %w", err)
	}
	if len(c.GetTo()) == 0 || len(c.GetTo()) > 20 {
		return errors.New("between 1 and 20 recipients are required")
	}
	for _, to := range c.GetTo() {
		if _, err := mail.ParseAddress(to); err != nil {
			return fmt.Errorf("invalid recipient %q: %w", to, err)
		}
	}
	if strings.ContainsAny(c.GetUsername(), "\r\n") {
		return errors.New("invalid username")
	}
	return nil
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.IsLoopback()
}

// EmailSubject is the Subject header text.
func EmailSubject(m Message) string { return "[Asterism] " + truncate(m.Title(), 180) }

// EmailBodies renders the plain-text and HTML parts.
func EmailBodies(m Message) (text, htmlBody string) {
	now := m.SentAt
	if now.IsZero() {
		now = time.Now()
	}
	var t strings.Builder
	t.WriteString(m.Title() + "\n\n")
	var h strings.Builder
	h.WriteString(`<!doctype html><html><body style="font-family:system-ui,-apple-system,Segoe UI,sans-serif;font-size:14px;line-height:1.5">`)
	h.WriteString("<h2 style=\"font-size:16px;margin:0 0 12px\">" + html.EscapeString(m.Title()) + "</h2>")
	h.WriteString(`<table cellpadding="6" cellspacing="0" style="border-collapse:collapse">`)
	h.WriteString("<tr><th align=left>Severity</th><th align=left>State</th><th align=left>Alert</th><th align=left>Entity</th><th align=left>Project</th><th align=left>Since</th></tr>")
	for _, a := range m.Alerts {
		project := a.ProjectID
		if project == "" {
			project = "-"
		}
		fmt.Fprintf(&t, "[%s] %s: %s\n  entity: %s  project: %s  since: %s\n", strings.ToUpper(SeverityName(a.Severity)), StateName(a.State), a.Summary, a.EntityID, project, rfc3339(a.FiringAt))
		if a.URL != "" {
			fmt.Fprintf(&t, "  %s\n", a.URL)
		}
		summary := html.EscapeString(a.Summary)
		if a.URL != "" {
			summary = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(a.URL), summary)
		}
		fmt.Fprintf(&h, "<tr><td>%s</td><td>%s</td><td>%s</td><td><code>%s</code></td><td>%s</td><td>%s</td></tr>",
			SeverityName(a.Severity), StateName(a.State), summary, html.EscapeString(a.EntityID), html.EscapeString(project), since(a.FiringAt, now))
	}
	h.WriteString("</table>")
	if m.HubURL != "" {
		fmt.Fprintf(&t, "\nOpen alerts: %s/alerts\n", m.HubURL)
		fmt.Fprintf(&h, `<p><a href="%s/alerts">Open alerts in Asterism</a></p>`, html.EscapeString(m.HubURL))
	}
	t.WriteString("\n-- \nSent by Asterism\n")
	h.WriteString(`<p style="color:#888;font-size:12px">Sent by Asterism</p></body></html>`)
	return t.String(), h.String()
}

func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

// BuildEmail renders the full RFC 5322 message with a fixed boundary when
// boundary is non-empty (golden tests).
func BuildEmail(m Message, from string, to []string, boundary string) ([]byte, error) {
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	var toList []string
	for _, r := range to {
		a, err := mail.ParseAddress(r)
		if err != nil {
			return nil, fmt.Errorf("to: %w", err)
		}
		toList = append(toList, a.String())
	}
	textBody, htmlBody := EmailBodies(m)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if boundary != "" {
		if err := mw.SetBoundary(boundary); err != nil {
			return nil, err
		}
	}
	date := m.SentAt
	if date.IsZero() {
		date = time.Now()
	}
	msgID := m.DeliveryID
	if msgID == "" {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		msgID = hex.EncodeToString(b)
	}
	domain := "asterism.local"
	if _, d, ok := strings.Cut(fromAddr.Address, "@"); ok {
		domain = d
	}
	var head bytes.Buffer
	fmt.Fprintf(&head, "From: %s\r\n", headerSafe(fromAddr.String()))
	fmt.Fprintf(&head, "To: %s\r\n", headerSafe(strings.Join(toList, ", ")))
	fmt.Fprintf(&head, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", headerSafe(EmailSubject(m))))
	fmt.Fprintf(&head, "Date: %s\r\n", date.UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&head, "Message-ID: <%s@%s>\r\n", headerSafe(msgID), headerSafe(domain))
	head.WriteString("MIME-Version: 1.0\r\n")
	head.WriteString("Auto-Submitted: auto-generated\r\n")
	fmt.Fprintf(&head, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mw.Boundary())

	for _, part := range []struct{ ctype, body string }{{"text/plain; charset=utf-8", textBody}, {"text/html; charset=utf-8", htmlBody}} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(head.Bytes(), buf.Bytes()...), nil
}

// Send delivers m. SMTP errors are permanent for 5xx replies and retryable
// otherwise.
func (e *Email) Send(ctx context.Context, m Message) Result {
	raw, err := BuildEmail(m, e.From, e.To, "")
	if err != nil {
		return Result{Err: err}
	}
	err = e.deliver(ctx, raw)
	if err == nil {
		return Result{}
	}
	var tpErr *textproto.Error
	retry := true
	if errors.As(err, &tpErr) && tpErr.Code >= 500 {
		retry = false
	}
	return Result{Err: errors.New(Redact(err.Error(), e.Password)), Retryable: retry && ctx.Err() == nil}
}

func (e *Email) deliver(ctx context.Context, raw []byte) error {
	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	tlsCfg := e.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
	}
	var conn net.Conn
	var err error
	if e.Security == asterismv1.SmtpSecurity_SMTP_SECURITY_TLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, e.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello("asterism"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if e.Security == asterismv1.SmtpSecurity_SMTP_SECURITY_STARTTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if e.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.Username, e.Password, e.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	from, _ := mail.ParseAddress(e.From)
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, r := range e.To {
		a, _ := mail.ParseAddress(r)
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("rcpt %s: %w", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	return c.Quit()
}
