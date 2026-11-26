package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// UserAgent is sent by every HTTP channel.
var UserAgent = "Asterism-Notifier/1"

// ErrBlockedAddress is returned when a target resolves to an address the
// SSRF guard refuses.
var ErrBlockedAddress = errors.New("target address is private, loopback, or link-local (set [alerts] allow_private_targets = true to allow it)")

var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // carrier-grade NAT / shared address space
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2001:db8::/32"),  // documentation
}

// BlockedIP reports whether the SSRF guard refuses an address.
func BlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range extraBlocked {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// NewHTTPClient returns the client used by HTTP channels. Unless
// allowPrivate is set, every connection is checked at dial time (after DNS
// resolution, so rebinding cannot bypass it) and refused when the peer is
// private, loopback, or link-local. Redirects are never followed and no
// proxy from the environment is used.
func NewHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("refusing %q: %w", address, ErrBlockedAddress)
			}
			if BlockedIP(ap.Addr()) {
				return ErrBlockedAddress
			}
			return nil
		}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ValidateTargetURL checks an HTTP channel target. https is required unless
// allowPrivate is set; literal private addresses are refused unless
// allowPrivate (hostnames are checked again at dial time). allowedHosts, when
// non-empty, restricts the host (ignored when allowPrivate, so tests and LAN
// receivers can stand in).
func ValidateTargetURL(raw string, allowPrivate bool, allowedHosts ...string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("a URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return errors.New("not an absolute URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowPrivate {
			return errors.New("https is required (plain http only with [alerts] allow_private_targets = true)")
		}
	default:
		return fmt.Errorf("unsupported scheme %q; use https", u.Scheme)
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil && !allowPrivate && BlockedIP(ip) {
		return ErrBlockedAddress
	}
	if !allowPrivate && (strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost")) {
		return ErrBlockedAddress
	}
	if len(allowedHosts) > 0 && !allowPrivate {
		ok := false
		for _, h := range allowedHosts {
			if strings.EqualFold(host, h) {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("host must be %s", strings.Join(allowedHosts, " or "))
		}
	}
	return nil
}

// URLHint masks a secret URL down to its host: "hooks.slack.com/…".
func URLHint(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "••••"
	}
	return u.Hostname() + "/…"
}

// secretForms lists the substrings of a secret that must never appear in
// error text: the value, its escaped forms, and for URLs the path, query,
// and every path segment or query value of 8+ characters (receivers often
// echo only the path, which is where webhook tokens live).
func secretForms(sec string) []string {
	forms := []string{sec, url.PathEscape(sec), url.QueryEscape(sec)}
	u, err := url.Parse(sec)
	if err != nil || u.Host == "" {
		return forms
	}
	if len(u.Path) >= 8 {
		forms = append(forms, u.Path, u.EscapedPath())
	}
	if len(u.RawQuery) >= 8 {
		forms = append(forms, u.RawQuery)
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if len(seg) >= 8 {
			forms = append(forms, seg)
		}
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			if len(v) >= 8 {
				forms = append(forms, v)
			}
		}
	}
	if pw, ok := u.User.Password(); ok && len(pw) >= 4 {
		forms = append(forms, pw)
	}
	return forms
}

// Redact replaces each secret (and its URL-path/escaped forms) in s.
func Redact(s string, secrets ...string) string {
	var forms []string
	for _, sec := range secrets {
		if len(sec) < 4 {
			continue
		}
		forms = append(forms, secretForms(sec)...)
	}
	// Longest first so a path is replaced before its segments.
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	for _, f := range forms {
		if f != "" {
			s = strings.ReplaceAll(s, f, "[REDACTED]")
		}
	}
	return s
}

// scrubErr turns a transport error into text without URLs or secrets.
func scrubErr(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		host := ""
		if u, perr := url.Parse(ue.URL); perr == nil {
			host = u.Hostname()
		}
		inner := ue.Err
		if errors.Is(inner, ErrBlockedAddress) {
			return fmt.Errorf("%s %s: %w", strings.ToLower(ue.Op), host, ErrBlockedAddress)
		}
		return fmt.Errorf("%s %s: %s", strings.ToLower(ue.Op), host, Redact(inner.Error(), secrets...))
	}
	return errors.New(Redact(err.Error(), secrets...))
}

// postJSON sends body and classifies the response. secrets are scrubbed
// from any error text.
func postJSON(ctx context.Context, client *http.Client, target string, body []byte, headers map[string]string, secrets ...string) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Result{Err: scrubErr(err, secrets...)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Err: scrubErr(err, secrets...), Retryable: !errors.Is(err, ErrBlockedAddress) && ctx.Err() == nil}
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return classify(resp, snippet, secrets...)
}

// classify maps an HTTP response to a Result.
func classify(resp *http.Response, snippet []byte, secrets ...string) Result {
	r := Result{HTTPStatus: resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return r
	}
	text := strings.TrimSpace(string(snippet))
	text = strings.Map(func(c rune) rune {
		if c < 0x20 {
			return ' '
		}
		return c
	}, text)
	msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if text != "" {
		msg += ": " + truncate(Redact(text, secrets...), 200)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		msg += " (redirects are not followed)"
	}
	r.Err = errors.New(msg)
	switch {
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		r.Retryable = true
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			r.RetryAfter = min(time.Duration(s)*time.Second, 5*time.Minute)
		}
	}
	return r
}
