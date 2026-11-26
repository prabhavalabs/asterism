package favicon

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func pngOf(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func icoOf(w, h byte) []byte {
	b := make([]byte, 6+16+4)
	b[2], b[4] = 1, 1
	b[6], b[7] = w, h
	b[6+8] = 4   // size
	b[6+12] = 22 // offset
	return b
}

func TestValidate(t *testing.T) {
	vp8l := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x00\x00\x00\x00\x2f"), 0x1f, 0xc0, 0x07, 0x00)
	vp8l = append(vp8l, make([]byte, 16)...)
	tests := []struct {
		name     string
		data     []byte
		declared string
		max      int
		want     string
		w, h     int
		wantErr  string
	}{
		{name: "png", data: pngOf(t, 32, 16), want: "image/png", w: 32, h: 16},
		{name: "png declared", data: pngOf(t, 8, 8), declared: "image/png", want: "image/png", w: 8, h: 8},
		{name: "ico", data: icoOf(0, 0), want: "image/x-icon", w: 256, h: 256},
		{name: "ico alias", data: icoOf(16, 16), declared: "image/vnd.microsoft.icon", want: "image/x-icon", w: 16, h: 16},
		{name: "webp lossless", data: vp8l, want: "image/webp", w: 32, h: 32},
		{name: "svg", data: []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`), want: "image/svg+xml"},
		{name: "svg with script still an svg", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), want: "image/svg+xml"},
		{name: "svg entity bomb", data: []byte(`<?xml version="1.0"?><!DOCTYPE svg [<!ENTITY a "aaaa">]><svg>&a;</svg>`), wantErr: "DTD"},
		{name: "svg malformed", data: []byte(`<svg><g></svg>`), wantErr: "malformed"},
		{name: "html", data: []byte(`<!doctype html><html></html>`), wantErr: "not a PNG"},
		{name: "gif", data: []byte("GIF89a\x01\x00\x01\x00"), wantErr: "not a PNG"},
		{name: "empty", data: nil, wantErr: "empty"},
		{name: "declared mismatch", data: pngOf(t, 4, 4), declared: "image/svg+xml", wantErr: "does not match"},
		{name: "too large", data: pngOf(t, 4, 4), max: 10, wantErr: "limit"},
		{name: "truncated png", data: pngOf(t, 4, 4)[:12], wantErr: "invalid image/png"},
		{name: "huge dimensions", data: pngOf(t, 4097, 1), wantErr: "dimensions"},
		{name: "ico data outside", data: []byte{0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 0, 0, 0, 0, 0xff, 0, 0, 0, 22, 0, 0, 0}, wantErr: "outside"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			max := tc.max
			if max == 0 {
				max = MaxUploadBytes
			}
			img, err := Validate(tc.data, tc.declared, max)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if img.ContentType != tc.want || img.Width != tc.w || img.Height != tc.h {
				t.Fatalf("got %+v, want %s %dx%d", img, tc.want, tc.w, tc.h)
			}
		})
	}
}

func TestParseIconLinks(t *testing.T) {
	base, _ := url.Parse("https://shop.example.com/store/")
	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "svg first then largest",
			html: `<html><head>
				<link rel="icon" href="/favicon-32.png" sizes="32x32">
				<link rel="apple-touch-icon" href="apple.png">
				<link rel="icon" type="image/svg+xml" href="/icon.svg">
				<link rel="stylesheet" href="/x.css">
				<link rel="icon" href="/big.png" sizes="16x16 512x512">
			</head><body><link rel="icon" href="/late.png"></body></html>`,
			want: []string{"https://shop.example.com/icon.svg", "https://shop.example.com/big.png", "https://shop.example.com/store/apple.png", "https://shop.example.com/favicon-32.png"},
		},
		{
			name: "shortcut icon and case",
			html: `<HEAD><LINK REL="Shortcut Icon" HREF="//cdn.example.com/f.ico"></HEAD>`,
			want: []string{"https://cdn.example.com/f.ico"},
		},
		{
			name: "non-https and data URIs skipped",
			html: `<head><link rel=icon href="http://x.example.com/a.png"><link rel=icon href="data:image/png;base64,AAA"><link rel=icon href="javascript:alert(1)"><link rel=icon href="https://u:p@x.example.com/a.png"></head>`,
			want: nil,
		},
		{
			name: "stops at body",
			html: `<body><link rel="icon" href="/x.png"></body>`,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseIconLinks(strings.NewReader(tc.html), base)
			var urls []string
			for _, l := range got {
				urls = append(urls, l.URL)
			}
			if strings.Join(urls, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("links = %v\nwant    %v", urls, tc.want)
			}
		})
	}
}

func TestPublicAddr(t *testing.T) {
	tests := map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "::1": false,
		"fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"224.0.0.1": false, "255.255.255.255": false, "64:ff9b::a00:1": false, "198.18.0.1": false,
	}
	for in, want := range tests {
		if got := PublicAddr(netip.MustParseAddr(in)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestValidHostname(t *testing.T) {
	tests := map[string]bool{
		"shop.example.com": true, "a-b.co.uk": true, "WWW.Example.COM.": true,
		"": false, "localhost": false, "*.example.com": false, "127.0.0.1": false, "[::1]": false,
		"api.internal": false, "nas.local": false, "example.com:8443": false, "-bad.example.com": false,
		"under_score.example.com": false, "router.lan": false,
	}
	for in, want := range tests {
		if got := ValidHostname(in); got != want {
			t.Errorf("ValidHostname(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestGuardRefusesLoopback checks the production dialer: the test server
// listens on 127.0.0.1, so the connect-time guard must refuse it.
func TestGuardRefusesLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer srv.Close()
	f := NewFetcher()
	_, _, err := f.get(context.Background(), srv.URL, "*/*", 1024)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "10.0.0.5:443", "169.254.169.254:80", "8.8.8.8:443"} {
		err := guardControl("tcp", addr, nil)
		if blocked := errors.Is(err, ErrBlockedAddress); blocked == (addr == "8.8.8.8:443") {
			t.Errorf("guardControl(%s) = %v", addr, err)
		}
	}
	if _, err := f.Fetch(context.Background(), "localhost"); err == nil {
		t.Fatal("localhost accepted")
	}
}

// testFetcher routes every hostname to srv and trusts its certificate.
func testFetcher(t *testing.T, srv *httptest.Server) *Fetcher {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	addr := srv.Listener.Addr().String()
	d := &net.Dialer{Timeout: time.Second}
	return newFetcher(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}, &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12})
}

func TestFetch(t *testing.T) {
	icon := pngOf(t, 64, 64)
	var gotUA string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		_, _ = w.Write([]byte(`<html><head><link rel="icon" href="/missing.png" sizes="512x512"><link rel="icon" href="/icon.png" sizes="64x64"></head></html>`))
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(icon) })
	mux.HandleFunc("/missing.png", http.NotFound)
	srv := httptest.NewUnstartedServer(mux)
	srv.StartTLS()
	defer srv.Close()

	f := testFetcher(t, srv)
	res, err := f.Fetch(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(res.Data, icon) || res.URL != "https://example.com/icon.png" || res.Image.Width != 64 {
		t.Fatalf("res = %s %+v", res.URL, res.Image)
	}
	if gotUA != UserAgent {
		t.Fatalf("User-Agent = %q", gotUA)
	}
}

func TestFetchFallbackAndLimits(t *testing.T) {
	ico := icoOf(32, 32)
	redirects := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html><head></head></html>")) })
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(ico) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		redirects++
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/favicon.ico", http.StatusFound)
	})
	mux.HandleFunc("/huge", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), MaxFaviconBytes+10))
	})
	mux.HandleFunc("/html.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) })
	srv := httptest.NewUnstartedServer(mux)
	srv.StartTLS()
	defer srv.Close()
	f := testFetcher(t, srv)
	ctx := context.Background()

	res, err := f.Fetch(ctx, "example.com")
	if err != nil || res.Image.ContentType != "image/x-icon" {
		t.Fatalf("fallback: %+v %v", res.Image, err)
	}
	if _, err := f.image(ctx, "https://example.com/loop"); err == nil || redirects > maxRedirects+1 {
		t.Fatalf("redirect loop: err=%v redirects=%d", err, redirects)
	}
	if _, err := f.image(ctx, "https://example.com/plain"); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("https downgrade: %v", err)
	}
	if _, err := f.image(ctx, "https://example.com/huge"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("size cap: %v", err)
	}
	if _, err := f.image(ctx, "https://example.com/html.png"); !errors.Is(err, ErrNotImage) {
		t.Fatalf("non-image: %v", err)
	}
}
