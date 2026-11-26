package uplink

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
)

// PinMismatchError is returned when no certificate in the hub's chain has a
// public key matching hub.tls.pin_sha256.
type PinMismatchError struct {
	Got []string
}

func (e *PinMismatchError) Error() string {
	got := "none"
	if len(e.Got) > 0 {
		got = e.Got[0]
	}
	return fmt.Sprintf("hub certificate public key does not match hub.tls.pin_sha256 (leaf key is sha256/%s); "+
		"if the hub key was rotated on purpose, update the pin in agent.yaml", got)
}

// SPKIPin returns the base64 SHA-256 of a certificate's SubjectPublicKeyInfo.
func SPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// TLSConfig builds the client TLS configuration: a private CA (ca_file)
// replaces the system roots, server_name overrides SNI and verification,
// pins are checked after normal chain verification, and client_cert plus
// client_key enable mTLS.
func TLSConfig(t config.TLS) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.ServerName}
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read hub.tls.ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("hub.tls.ca_file %s contains no PEM certificates", t.CAFile)
		}
		cfg.RootCAs = pool
	}
	if t.ClientCert != "" || t.ClientKey != "" {
		cert, err := tls.LoadX509KeyPair(t.ClientCert, t.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("load hub.tls client_cert/client_key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if len(t.PinSHA256) > 0 {
		pins := make([][]byte, 0, len(t.PinSHA256))
		for _, p := range t.PinSHA256 {
			b, err := config.DecodePin(p)
			if err != nil {
				return nil, err
			}
			pins = append(pins, b)
		}
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			var got []string
			for _, c := range cs.PeerCertificates {
				sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
				for _, p := range pins {
					if bytes.Equal(sum[:], p) {
						return nil
					}
				}
				got = append(got, base64.StdEncoding.EncodeToString(sum[:]))
			}
			return &PinMismatchError{Got: got}
		}
	}
	return cfg, nil
}

// clients holds the HTTP clients for each transport.
type clients struct {
	bidi  *http.Client // HTTP/2 only (TLS ALPN h2, or h2c prior knowledge)
	unary *http.Client // HTTP/1.1 or HTTP/2
}

// newClients builds transports for hub.url. Plain http:// uses h2c for the
// bidi stream (Go's built-in http.Protocols, no x/net) and HTTP/1.1 for the
// unary fallback.
func newClients(hub config.Hub, token, userAgent string) (*clients, error) {
	u, err := url.Parse(hub.URL)
	if err != nil {
		return nil, fmt.Errorf("parse hub.url: %w", err)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	base := func() *http.Transport {
		return &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConns:        4,
			HTTP2: &http.HTTP2Config{
				SendPingTimeout: 20 * time.Second,
				PingTimeout:     10 * time.Second,
			},
		}
	}
	wrap := func(rt http.RoundTripper) *http.Client {
		return &http.Client{Transport: &authTransport{base: rt, token: token, userAgent: userAgent}}
	}
	switch u.Scheme {
	case "https":
		tc, err := TLSConfig(hub.TLS)
		if err != nil {
			return nil, err
		}
		t := base()
		t.TLSClientConfig = tc
		t.ForceAttemptHTTP2 = true
		t.Protocols = new(http.Protocols)
		t.Protocols.SetHTTP1(true)
		t.Protocols.SetHTTP2(true)
		c := wrap(t)
		return &clients{bidi: c, unary: c}, nil
	case "http":
		if !config.IsLoopbackHost(u.Hostname()) && !hub.TLS.InsecureDev {
			return nil, errors.New("refusing plain http:// to a non-loopback hub; use https:// or hub.tls.insecure_dev")
		}
		h2c := base()
		h2c.Protocols = new(http.Protocols)
		h2c.Protocols.SetUnencryptedHTTP2(true)
		h1 := base()
		h1.Protocols = new(http.Protocols)
		h1.Protocols.SetHTTP1(true)
		return &clients{bidi: wrap(h2c), unary: wrap(h1)}, nil
	default:
		return nil, fmt.Errorf("unsupported hub.url scheme %q", u.Scheme)
	}
}

func (c *clients) close() {
	for _, hc := range []*http.Client{c.bidi, c.unary} {
		if a, ok := hc.Transport.(*authTransport); ok {
			if t, ok := a.base.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}
	}
}

// authTransport adds the bearer token and User-Agent to every request.
type authTransport struct {
	base      http.RoundTripper
	token     string
	userAgent string
}

func (a *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+a.token)
	if a.userAgent != "" {
		r2.Header.Set("User-Agent", a.userAgent)
	}
	return a.base.RoundTrip(r2)
}

// trimURL removes a trailing slash for connect base URLs.
func trimURL(s string) string { return strings.TrimRight(s, "/") }
