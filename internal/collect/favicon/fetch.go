package favicon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"
)

// UserAgent is sent with every favicon request.
const UserAgent = "Asterism-favicon/1.0"

const (
	maxRedirects   = 3
	maxHTMLBytes   = 512 << 10
	maxLinkTries   = 3
	requestTimeout = 8 * time.Second
)

// ErrBlockedAddress is returned when a hostname resolves to an address the
// fetcher refuses (loopback, private, link-local, ...).
var ErrBlockedAddress = errors.New("refusing to connect to a private, loopback or link-local address")

// blockedPrefixes are non-public ranges beyond what netip's predicates cover.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private space
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 embeds IPv4
}

// PublicAddr reports whether ip is a globally routable unicast address.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || !ip.IsGlobalUnicast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// guardControl runs after DNS resolution, right before connect, so a
// hostname that resolves (or rebinds) to a private address is refused.
func guardControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
	}
	if !PublicAddr(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ap.Addr())
	}
	return nil
}

// ValidHostname reports whether h is a DNS name the fetcher may contact: a
// dotted name without wildcards, ports, IP literals or local-only suffixes.
func ValidHostname(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	if _, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa", ".test", ".invalid", ".example", ".onion"} {
		if strings.HasSuffix(h, suffix) {
			return false
		}
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// Fetcher downloads favicons over https with SSRF protections.
type Fetcher struct {
	client *http.Client
}

// Result is one fetched favicon.
type Result struct {
	Data  []byte
	Image Image
	// URL is the final https URL of the image.
	URL string
}

// NewFetcher returns a fetcher whose dialer refuses non-public addresses,
// which never uses an HTTP proxy, follows at most three https redirects and
// times every request out after a few seconds.
func NewFetcher() *Fetcher {
	dialer := &net.Dialer{Timeout: 3 * time.Second, Control: guardControl}
	return newFetcher(dialer.DialContext, nil)
}

func newFetcher(dial func(ctx context.Context, network, addr string) (net.Conn, error), tlsConf *tls.Config) *Fetcher {
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            dial,
		TLSHandshakeTimeout:    4 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxIdleConns:           4,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     false,
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 32 << 10,
	}
	if tlsConf != nil {
		tr.TLSClientConfig = tlsConf
	}
	return &Fetcher{client: &http.Client{
		Transport: tr,
		Timeout:   requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("more than %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-https URL %s", req.URL.Redacted())
			}
			return nil
		},
	}}
}

// Fetch finds the best favicon of https://host/: the <link rel=icon> family
// in the page head (SVG first, then the largest), falling back to
// /favicon.ico.
func (f *Fetcher) Fetch(ctx context.Context, host string) (Result, error) {
	if !ValidHostname(host) {
		return Result{}, fmt.Errorf("hostname %q is not eligible for favicon fetching", host)
	}
	base := &url.URL{Scheme: "https", Host: strings.ToLower(strings.TrimSuffix(host, ".")), Path: "/"}
	var errs []error
	links, final, err := f.pageLinks(ctx, base)
	if err != nil {
		errs = append(errs, err)
		if errors.Is(err, ErrBlockedAddress) {
			return Result{}, err
		}
	}
	tried := map[string]bool{}
	for _, l := range links {
		if len(tried) >= maxLinkTries {
			break
		}
		if tried[l.URL] {
			continue
		}
		tried[l.URL] = true
		res, err := f.image(ctx, l.URL)
		if err == nil {
			return res, nil
		}
		errs = append(errs, err)
	}
	fallbackBase := base
	if final != nil {
		fallbackBase = final
	}
	fav := fallbackBase.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()
	if !tried[fav] {
		res, err := f.image(ctx, fav)
		if err == nil {
			return res, nil
		}
		errs = append(errs, err)
	}
	return Result{}, fmt.Errorf("no favicon on %s: %w", host, errors.Join(errs...))
}

func (f *Fetcher) get(ctx context.Context, u, accept string, limit int) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", accept)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp, fmt.Errorf("GET %s: HTTP %d", req.URL.Redacted(), resp.StatusCode)
	}
	if resp.ContentLength > int64(limit) {
		return nil, resp, fmt.Errorf("GET %s: body is %d bytes (limit %d)", req.URL.Redacted(), resp.ContentLength, limit)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, resp, err
	}
	if len(data) > limit {
		return nil, resp, fmt.Errorf("GET %s: body exceeds %d bytes", req.URL.Redacted(), limit)
	}
	return data, resp, nil
}

func (f *Fetcher) pageLinks(ctx context.Context, base *url.URL) ([]Link, *url.URL, error) {
	data, resp, err := f.get(ctx, base.String(), "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1", maxHTMLBytes)
	if err != nil {
		if errors.Is(err, ErrBlockedAddress) {
			return nil, nil, ErrBlockedAddress
		}
		return nil, nil, err
	}
	final := resp.Request.URL
	return ParseIconLinks(strings.NewReader(string(data)), final), final, nil
}

func (f *Fetcher) image(ctx context.Context, u string) (Result, error) {
	data, resp, err := f.get(ctx, u, "image/svg+xml,image/png,image/x-icon,image/webp,image/jpeg;q=0.9,*/*;q=0.1", MaxFaviconBytes)
	if err != nil {
		return Result{}, err
	}
	img, err := Validate(data, "", MaxFaviconBytes)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", resp.Request.URL.Redacted(), err)
	}
	return Result{Data: data, Image: img, URL: resp.Request.URL.String()}, nil
}

// Link is one icon <link> from a page head.
type Link struct {
	URL   string
	Rel   string
	Type  string
	Sizes string
	score int
}

// ParseIconLinks returns the https icon links of an HTML document's head,
// best first: SVG, then the largest declared size, then apple-touch-icon,
// then plain icons. Relative hrefs resolve against base.
func ParseIconLinks(r io.Reader, base *url.URL) []Link {
	z := html.NewTokenizer(r)
	var out []Link
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return sortLinks(out)
		case html.EndTagToken:
			name, _ := z.TagName()
			if string(name) == "head" {
				return sortLinks(out)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "body":
				return sortLinks(out)
			case "link":
			default:
				continue
			}
			var rel, href, typ, sizes string
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				switch string(k) {
				case "rel":
					rel = strings.ToLower(strings.Join(strings.Fields(string(v)), " "))
				case "href":
					href = strings.TrimSpace(string(v))
				case "type":
					typ = strings.ToLower(strings.TrimSpace(string(v)))
				case "sizes":
					sizes = strings.ToLower(strings.TrimSpace(string(v)))
				}
			}
			if href == "" || !iconRel(rel) {
				continue
			}
			ref, err := url.Parse(href)
			if err != nil {
				continue
			}
			abs := base.ResolveReference(ref)
			if abs.Scheme != "https" || abs.Host == "" || abs.User != nil {
				continue
			}
			abs.Fragment = ""
			l := Link{URL: abs.String(), Rel: rel, Type: typ, Sizes: sizes}
			l.score = linkScore(l)
			out = append(out, l)
			if len(out) >= 32 {
				return sortLinks(out)
			}
		}
	}
}

func iconRel(rel string) bool {
	for _, r := range strings.Fields(rel) {
		switch r {
		case "icon", "apple-touch-icon", "apple-touch-icon-precomposed":
			return true
		}
	}
	return false
}

func linkScore(l Link) int {
	isSVG := l.Type == "image/svg+xml" || strings.HasSuffix(strings.ToLower(strings.SplitN(l.URL, "?", 2)[0]), ".svg") || l.Sizes == "any"
	if isSVG {
		return 1_000_000
	}
	best := 0
	for _, s := range strings.Fields(l.Sizes) {
		w, h, ok := strings.Cut(s, "x")
		if !ok {
			continue
		}
		wi, err1 := strconv.Atoi(w)
		hi, err2 := strconv.Atoi(h)
		if err1 == nil && err2 == nil && wi*hi > best && wi <= maxDimension && hi <= maxDimension {
			best = wi * hi
		}
	}
	if best == 0 && strings.Contains(l.Rel, "apple-touch-icon") {
		best = 180 * 180
	}
	if best == 0 {
		best = 16 * 16 // an undeclared icon is usually favicon-sized
	}
	return best
}

func sortLinks(ls []Link) []Link {
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].score > ls[j].score })
	return ls
}
