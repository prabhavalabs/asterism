// Package dockerapi is the agent's Docker Engine API client: net/http over
// the Docker unix socket (or tcp:// for a socket proxy), with a compiled-in
// endpoint allow-list.
//
// Only read endpoints (containers, events, images, volumes, networks, info,
// system df, logs, _ping) and the five lifecycle actions are reachable. Even
// with a raw root-equivalent socket, a bug or a hostile hub cannot reach
// exec, create, build, or any other endpoint: Client.do refuses every
// request the allow-list does not match before it touches the network.
package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// MaxResponseBytes caps every non-streaming response body (8 MiB).
const MaxResponseBytes = 8 << 20

// ErrNotAllowed is returned for requests outside the allow-list.
var ErrNotAllowed = errors.New("dockerapi: endpoint not in the agent allow-list")

// ErrTooLarge is returned when a response exceeds MaxResponseBytes.
var ErrTooLarge = errors.New("dockerapi: response exceeds 8 MiB cap")

// StatusError is a non-2xx Engine response.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("docker engine: HTTP %d: %s", e.Code, e.Message)
}

// IsStatus reports whether err is a StatusError with the given code.
func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
}

// endpoint is one allow-listed (method, path) pattern. Paths are matched
// after the optional "/v1.xx" version prefix is stripped.
type endpoint struct {
	method string
	re     *regexp.Regexp
}

const idPat = `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}` // full/short id or container name

var allowList = []endpoint{
	{http.MethodGet, regexp.MustCompile(`^/_ping$`)},
	{http.MethodHead, regexp.MustCompile(`^/_ping$`)},
	{http.MethodGet, regexp.MustCompile(`^/info$`)},
	{http.MethodGet, regexp.MustCompile(`^/events$`)},
	{http.MethodGet, regexp.MustCompile(`^/containers/json$`)},
	{http.MethodGet, regexp.MustCompile(`^/containers/` + idPat + `/json$`)},
	{http.MethodGet, regexp.MustCompile(`^/containers/` + idPat + `/logs$`)},
	{http.MethodGet, regexp.MustCompile(`^/images/json$`)},
	{http.MethodGet, regexp.MustCompile(`^/volumes$`)},
	{http.MethodGet, regexp.MustCompile(`^/networks$`)},
	{http.MethodGet, regexp.MustCompile(`^/system/df$`)},
	{http.MethodPost, regexp.MustCompile(`^/containers/` + idPat + `/(start|stop|restart|pause|unpause)$`)},
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+/`)

// Allowed reports whether method and path (with or without a version
// prefix) are in the allow-list.
func Allowed(method, path string) bool {
	if loc := versionPrefix.FindStringIndex(path); loc != nil {
		path = path[loc[1]-1:]
	}
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	for _, e := range allowList {
		if e.method == method && e.re.MatchString(path) {
			return true
		}
	}
	return false
}

// Client talks to one Docker-compatible Engine API endpoint.
type Client struct {
	endpoint string
	base     string // "http://docker" for unix sockets, "http://host:port" for tcp
	hc       *http.Client
	stream   *http.Client // no overall timeout, for events and log follow

	// PostProcess, when set, runs on every container this client converts
	// itself (action results), so agent-side redaction rules apply there
	// too. Set it before first use.
	PostProcess func(*v1.Container)

	mu      sync.Mutex
	version string // negotiated API version ("1.47"); "" = unversioned
	pinged  bool
}

// Options tunes a Client.
type Options struct {
	// Timeout bounds each non-streaming request (default 10 s).
	Timeout time.Duration
}

// New returns a client for endpoint ("unix:///var/run/docker.sock" or
// "tcp://socket-proxy:2375"). It does not connect.
func New(endpoint string, opts Options) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	c := &Client{endpoint: endpoint}
	var dial func(ctx context.Context, network, addr string) (net.Conn, error)
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		sock := strings.TrimPrefix(endpoint, "unix://")
		if sock == "" {
			return nil, fmt.Errorf("dockerapi: empty unix socket path in %q", endpoint)
		}
		d := &net.Dialer{Timeout: 5 * time.Second}
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "unix", sock) }
		c.base = "http://docker"
	case strings.HasPrefix(endpoint, "tcp://"), strings.HasPrefix(endpoint, "http://"):
		u, err := url.Parse(strings.Replace(endpoint, "tcp://", "http://", 1))
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("dockerapi: invalid endpoint %q", endpoint)
		}
		d := &net.Dialer{Timeout: 5 * time.Second}
		dial = d.DialContext
		c.base = "http://" + u.Host
	default:
		return nil, fmt.Errorf("dockerapi: unsupported endpoint %q (want unix:// or tcp://)", endpoint)
	}
	tr := &http.Transport{
		DialContext:           dial,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: opts.Timeout,
		DisableCompression:    true,
	}
	c.hc = &http.Client{Transport: tr, Timeout: opts.Timeout, CheckRedirect: noRedirect}
	c.stream = &http.Client{Transport: tr, CheckRedirect: noRedirect}
	return c, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Endpoint returns the configured endpoint.
func (c *Client) Endpoint() string { return c.endpoint }

// APIVersion returns the negotiated API version ("" before Ping).
func (c *Client) APIVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

func (c *Client) path(p string) string {
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	if v == "" || p == "/_ping" {
		return p
	}
	return "/v" + v + p
}

// do sends a request after checking the allow-list.
func (c *Client) do(ctx context.Context, hc *http.Client, method, p string, q url.Values) (*http.Response, error) {
	full := c.path(p)
	if !Allowed(method, full) {
		return nil, fmt.Errorf("%w: %s %s", ErrNotAllowed, method, p)
	}
	u := c.base + full
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "asterism-agent")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var m struct {
			Message string `json:"message"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &m) == nil && m.Message != "" {
			msg = m.Message
		}
		return nil, &StatusError{Code: resp.StatusCode, Message: msg}
	}
	return resp, nil
}

// get performs a capped GET and returns the body.
func (c *Client) get(ctx context.Context, p string, q url.Values) ([]byte, error) {
	resp, err := c.do(ctx, c.hc, http.MethodGet, p, q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readCapped(resp.Body)
}

func readCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxResponseBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// minAPIVersion is the oldest Engine API the decoders are tested against
// (Docker 20.10). Newer servers are used at their own version.
const minAPIVersion = "1.41"

// Ping checks the engine and negotiates the API version from the
// API-Version response header.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, c.hc, http.MethodGet, "/_ping", nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64))
	resp.Body.Close()
	v := resp.Header.Get("API-Version")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinged = true
	if validVersion(v) {
		if versionLess(v, minAPIVersion) {
			return fmt.Errorf("docker engine API %s is older than the supported %s", v, minAPIVersion)
		}
		c.version = v
	}
	return nil
}

func validVersion(v string) bool {
	maj, min, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	_, e1 := strconv.Atoi(maj)
	_, e2 := strconv.Atoi(min)
	return e1 == nil && e2 == nil
}

func versionLess(a, b string) bool {
	am, an, _ := strings.Cut(a, ".")
	bm, bn, _ := strings.Cut(b, ".")
	ai, _ := strconv.Atoi(am)
	bi, _ := strconv.Atoi(bm)
	if ai != bi {
		return ai < bi
	}
	aj, _ := strconv.Atoi(an)
	bj, _ := strconv.Atoi(bn)
	return aj < bj
}

// Info returns GET /info.
func (c *Client) Info(ctx context.Context) ([]byte, error) { return c.get(ctx, "/info", nil) }

// ListContainers returns GET /containers/json?all=1.
func (c *Client) ListContainers(ctx context.Context) ([]byte, error) {
	return c.get(ctx, "/containers/json", url.Values{"all": {"1"}})
}

// InspectContainer returns GET /containers/{id}/json.
func (c *Client) InspectContainer(ctx context.Context, id string) ([]byte, error) {
	if !validRef(id) {
		return nil, fmt.Errorf("dockerapi: invalid container reference %q", truncate(id))
	}
	return c.get(ctx, "/containers/"+id+"/json", nil)
}

// Images returns GET /images/json.
func (c *Client) Images(ctx context.Context) ([]byte, error) { return c.get(ctx, "/images/json", nil) }

// Volumes returns GET /volumes.
func (c *Client) Volumes(ctx context.Context) ([]byte, error) { return c.get(ctx, "/volumes", nil) }

// Networks returns GET /networks.
func (c *Client) Networks(ctx context.Context) ([]byte, error) { return c.get(ctx, "/networks", nil) }

// SystemDF returns GET /system/df (slow: walks volumes; callers poll hourly).
func (c *Client) SystemDF(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := c.do(ctx, c.stream, http.MethodGet, "/system/df", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readCapped(resp.Body)
}

var refRe = regexp.MustCompile(`^` + idPat + `$`)

func validRef(s string) bool { return refRe.MatchString(s) }

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
