package api

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/showcase"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/iconstore"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

func newIconServer(t *testing.T) (*httptest.Server, *service.Observatory, string) {
	t.Helper()
	readTok, readDigest, _ := auth.GenerateToken()
	mgr, err := auth.NewManager(auth.Options{
		AdminEmail: "a@example.com", AdminPassword: "pw", SessionScopes: []auth.Scope{auth.ScopeRead},
		Tokens: []auth.APIToken{{Name: "reader", SHA256: readDigest, Scopes: []auth.Scope{auth.ScopeRead}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New(64)
	r := ring.New(30)
	sc := showcase.New(b, r)
	svc := service.New(b, r, nil, sc, "showcase")
	svc.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	st, err := iconstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.ConfigureIcons(service.IconOptions{Store: st, Dirs: sc})
	mux := http.NewServeMux()
	mgr.RegisterRoutes(mux)
	New(svc).Register(mux, mgr.RequireSession)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc, readTok
}

// sessionClient logs in and returns a cookie-carrying client.
func sessionClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	resp, err := hc.Post(srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"email":"a@example.com","password":"pw"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %v %v", err, resp)
	}
	resp.Body.Close()
	return hc
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestProjectIconRPCAuth(t *testing.T) {
	srv, svc, readTok := newIconServer(t)
	ctx := context.Background()
	img := pngBytes(t)
	req := &asterismv1.SetProjectIconRequest{Project: "proj:staging-lab", Image: img, ContentType: "image/png"}

	// Read token: authenticated but refused.
	tokenHC := &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: map[string]string{"Authorization": "Bearer " + readTok}}}
	tokenCl := asterismv1connect.NewProjectSettingsServiceClient(tokenHC, srv.URL)
	_, err := tokenCl.SetProjectIcon(ctx, connect.NewRequest(req))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("token err = %v", err)
	}
	// No credentials at all.
	anon := asterismv1connect.NewProjectSettingsServiceClient(http.DefaultClient, srv.URL)
	if _, err := anon.SetProjectIcon(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anon err = %v", err)
	}

	// Session: allowed.
	hc := sessionClient(t, srv)
	cl := asterismv1connect.NewProjectSettingsServiceClient(hc, srv.URL)
	resp, err := cl.SetProjectIcon(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("session SetProjectIcon: %v", err)
	}
	icon := resp.Msg.Icon
	if icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UPLOADED || !strings.HasPrefix(icon.ImageUrl, "/api/project-icons/") {
		t.Fatalf("icon = %+v", icon)
	}
	if resp.Msg.AuditEvent.GetAudit().GetActorKind() != "session" {
		t.Fatalf("audit = %+v", resp.Msg.AuditEvent)
	}

	// Invalid images map to InvalidArgument.
	bad := &asterismv1.SetProjectIconRequest{Project: "staging-lab", Image: []byte("<script>alert(1)</script>")}
	if _, err := cl.SetProjectIcon(ctx, connect.NewRequest(bad)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad image err = %v", err)
	}

	// The CSRF guard: a request without the Connect-Protocol-Version header is rejected.
	raw, _ := http.NewRequest(http.MethodPost, srv.URL+asterismv1connect.ProjectSettingsServiceClearProjectIconProcedure, strings.NewReader(`{"project":"staging-lab"}`))
	raw.Header.Set("Content-Type", "application/json")
	r2, err := hc.Do(raw)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode == http.StatusOK {
		t.Fatal("request without Connect-Protocol-Version accepted")
	}

	// The image endpoint.
	get := func(c *http.Client, url string, hdr map[string]string) *http.Response {
		t.Helper()
		rq, _ := http.NewRequest(http.MethodGet, url, nil)
		for k, v := range hdr {
			rq.Header.Set(k, v)
		}
		res, err := c.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := get(hc, srv.URL+icon.ImageUrl, nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	h := res.Header
	if res.StatusCode != http.StatusOK || !bytes.Equal(body, img) {
		t.Fatalf("GET icon: %d", res.StatusCode)
	}
	for k, want := range map[string]string{
		"Content-Type":            "image/png",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; sandbox",
		"Cache-Control":           "private, max-age=86400",
		"ETag":                    `"` + icon.ImageEtag + `"`,
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	res = get(hc, srv.URL+icon.ImageUrl, map[string]string{"If-None-Match": `"` + icon.ImageEtag + `"`})
	res.Body.Close()
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET = %d", res.StatusCode)
	}
	// Read tokens may fetch images; anonymous callers may not.
	res = get(tokenHC, srv.URL+icon.ImageUrl, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("token GET = %d", res.StatusCode)
	}
	res = get(http.DefaultClient, srv.URL+icon.ImageUrl, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon GET = %d", res.StatusCode)
	}
	for _, p := range []string{"/api/project-icons/../../etc/passwd", "/api/project-icons/" + strings.Repeat("z", 32), "/api/project-icons/"} {
		res = get(hc, srv.URL+p, nil)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d", p, res.StatusCode)
		}
	}

	// Reset to automatic.
	cr, err := cl.ClearProjectIcon(ctx, connect.NewRequest(&asterismv1.ClearProjectIconRequest{Project: "staging-lab"}))
	if err != nil || cr.Msg.Icon.Kind == asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UPLOADED {
		t.Fatalf("clear: %v %+v", err, cr)
	}
	if _, err := svc.ProjectIconImage(iconstore.Key("staging-lab")); err == nil {
		t.Fatal("image still served after reset")
	}

	// Refresh picks up the generated showcase favicon of nova-core.
	rr, err := cl.RefreshProjectIcon(ctx, connect.NewRequest(&asterismv1.RefreshProjectIconRequest{Project: "nova-core"}))
	if err != nil || rr.Msg.Icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON ||
		rr.Msg.Icon.FaviconSource != asterismv1.FaviconSource_FAVICON_SOURCE_GENERATED {
		t.Fatalf("refresh: %v %+v", err, rr)
	}
	res = get(hc, srv.URL+rr.Msg.Icon.ImageUrl, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("svg GET = %d %v", res.StatusCode, res.Header)
	}

	// Capabilities tell the UI whether to show the controls.
	inv := asterismv1connect.NewInventoryServiceClient(hc, srv.URL)
	ov, err := inv.GetOverview(ctx, connect.NewRequest(&asterismv1.GetOverviewRequest{}))
	if err != nil || !ov.Msg.Capabilities.CanEditProjects {
		t.Fatalf("session capabilities: %v", err)
	}
	tov, err := asterismv1connect.NewInventoryServiceClient(tokenHC, srv.URL).GetOverview(ctx, connect.NewRequest(&asterismv1.GetOverviewRequest{}))
	if err != nil || tov.Msg.Capabilities.CanEditProjects {
		t.Fatalf("token capabilities: %v", err)
	}
}
