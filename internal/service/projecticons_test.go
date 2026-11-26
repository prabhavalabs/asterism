package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/favicon"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/iconstore"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

const testSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 48, 48))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeDirs struct {
	refs  map[string]DirIconRef
	data  map[string][]byte
	reads atomic.Int32
}

func (f *fakeDirs) DirIcon(wd string) (DirIconRef, bool) { r, ok := f.refs[wd]; return r, ok }
func (f *fakeDirs) ReadDirIcon(_ context.Context, ref DirIconRef) ([]byte, error) {
	f.reads.Add(1)
	if d, ok := f.data[ref.SHA256]; ok {
		return d, nil
	}
	return nil, errors.New("gone")
}

type fakeFetcher struct {
	hosts []string
	res   map[string]favicon.Result
}

func (f *fakeFetcher) Fetch(_ context.Context, host string) (favicon.Result, error) {
	f.hosts = append(f.hosts, host)
	if r, ok := f.res[host]; ok {
		return r, nil
	}
	return favicon.Result{}, errors.New("connection refused")
}

var adminSession = auth.Principal{Kind: "session", Subject: "admin@example.com", Scopes: []auth.Scope{auth.ScopeRead}}

func iconTestService(t *testing.T, dirs DirIconSource, fetch FaviconFetcher, allowed func(string) bool) (*Observatory, *iconstore.Store) {
	t.Helper()
	inv := &listInventory{
		projects: []*asterismv1.Project{
			{Id: "proj:shop", Name: "shop", WorkingDir: "/srv/shop", Workloads: []*asterismv1.Workload{
				{Id: "ctr:shop-db", Name: "shop-db", ProjectId: "proj:shop", Image: "postgres:17"},
				{Id: "ctr:shop-web", Name: "shop-web", ProjectId: "proj:shop", Image: "ghcr.io/acme/web:1"},
			}},
			{Id: "proj:blog", Name: "blog", WorkingDir: "/srv/blog", Workloads: []*asterismv1.Workload{
				{Id: "ctr:blog-web", Name: "blog-web", ProjectId: "proj:blog", Image: "ghcr.io/acme/blog:1"},
			}},
			{Id: "proj:edge", Name: "edge", Workloads: []*asterismv1.Workload{
				{Id: "ctr:nginx", Name: "nginx", ProjectId: "proj:edge", Image: "nginx:1"},
			}},
		},
		ingress: []*asterismv1.IngressRoute{
			{Id: "ingress:nginx/blog", ProxyWorkloadId: "ctr:nginx", Hostnames: []string{"www.blog.example.com", "blog.example.com:443", "*.blog.example.com", "blog.local"},
				Upstreams: []*asterismv1.UpstreamTarget{{WorkloadId: "ctr:blog-web"}}},
		},
	}
	st, err := iconstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := New(bus.New(8), ring.New(10), nil, inv, "local")
	o.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.ConfigureIcons(IconOptions{Store: st, Dirs: dirs, Fetcher: fetch, FetchAllowed: allowed})
	return o, st
}

func project(t *testing.T, o *Observatory, id string) *asterismv1.Project {
	t.Helper()
	p, err := o.GetProject(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIconPrecedence(t *testing.T) {
	up := iconstore.Meta{Project: "p", SHA256: strings.Repeat("a", 64), ContentType: "image/png", Source: iconSourceUpload}
	fav := iconstore.Meta{Project: "p", SHA256: strings.Repeat("b", 64), ContentType: "image/svg+xml", Source: iconSourceDomain, Origin: "https://p.example.com/favicon.svg"}
	neg := iconstore.Meta{Project: "p", CheckedMs: 5, Status: "no favicon found"}
	K := func(k asterismv1.ProjectIconKind) *asterismv1.ProjectIcon {
		return &asterismv1.ProjectIcon{Kind: k, Slug: "postgres", Monogram: "PR"}
	}
	tests := []struct {
		name          string
		base          asterismv1.ProjectIconKind
		up, fav       iconstore.Meta
		hasUp, hasFav bool
		want          asterismv1.ProjectIconKind
		wantEtag      string
	}{
		{"upload beats configured", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED, up, fav, true, true, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UPLOADED, "aaaaaaaaaaaaaaaa"},
		{"upload beats everything", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_MONOGRAM, up, iconstore.Meta{}, true, false, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UPLOADED, "aaaaaaaaaaaaaaaa"},
		{"configured beats favicon", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED, iconstore.Meta{}, fav, false, true, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED, ""},
		{"label beats favicon", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL, iconstore.Meta{}, fav, false, true, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL, ""},
		{"favicon beats detected", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, iconstore.Meta{}, fav, false, true, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON, "bbbbbbbbbbbbbbbb"},
		{"favicon beats monogram", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_MONOGRAM, iconstore.Meta{}, fav, false, true, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON, "bbbbbbbbbbbbbbbb"},
		{"negative favicon keeps detected", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, iconstore.Meta{}, neg, false, true, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, ""},
		{"nothing stored", asterismv1.ProjectIconKind_PROJECT_ICON_KIND_MONOGRAM, iconstore.Meta{}, iconstore.Meta{}, false, false, asterismv1.ProjectIconKind_PROJECT_ICON_KIND_MONOGRAM, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			icon := K(tc.base)
			applyIconImages(icon, tc.up, tc.fav, tc.hasUp, tc.hasFav)
			if icon.Kind != tc.want || icon.ImageEtag != tc.wantEtag {
				t.Fatalf("kind = %v etag = %q, want %v %q", icon.Kind, icon.ImageEtag, tc.want, tc.wantEtag)
			}
			if tc.wantEtag != "" && !strings.HasPrefix(icon.ImageUrl, ProjectIconPath+iconstore.Key("p")+"?v=") {
				t.Fatalf("image url = %q", icon.ImageUrl)
			}
			if tc.wantEtag == "" && icon.ImageUrl != "" {
				t.Fatalf("unexpected image url %q", icon.ImageUrl)
			}
			if icon.Slug != "postgres" || icon.Monogram != "PR" {
				t.Fatal("fallback glyph lost")
			}
			if tc.hasFav && tc.fav.HasImage() && (icon.FaviconSource != asterismv1.FaviconSource_FAVICON_SOURCE_DOMAIN || icon.FaviconOrigin == "") {
				t.Fatalf("favicon source not reported: %+v", icon)
			}
			if tc.fav.Status != "" && icon.FaviconStatus != tc.fav.Status {
				t.Fatalf("status = %q", icon.FaviconStatus)
			}
		})
	}
}

func TestSetClearProjectIconAuthAndAudit(t *testing.T) {
	o, _ := iconTestService(t, nil, nil, nil)
	ctx := context.Background()
	pngData := testPNG(t)

	// A read token is refused and the refusal is audited.
	_, err := o.SetProjectIcon(ctx, readOnlyToken, ProjectIconRequest{Project: "shop", Image: pngData, ContentType: "image/png"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("token err = %v", err)
	}
	// So is a token that holds the actions scope.
	_, err = o.SetProjectIcon(ctx, auth.Principal{Kind: "token", Subject: "ops", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeActions}}, ProjectIconRequest{Project: "shop", Image: pngData})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("actions token err = %v", err)
	}
	if c := o.Capabilities(readOnlyToken); c.CanEditProjects {
		t.Fatal("token can edit projects")
	}
	if c := o.Capabilities(adminSession); !c.CanEditProjects {
		t.Fatal("session cannot edit projects")
	}

	// Invalid image, unknown project.
	if _, err := o.SetProjectIcon(ctx, adminSession, ProjectIconRequest{Project: "shop", Image: []byte("<html>")}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("invalid image err = %v", err)
	}
	big := append(testPNG(t), make([]byte, favicon.MaxUploadBytes)...)
	if _, err := o.SetProjectIcon(ctx, adminSession, ProjectIconRequest{Project: "shop", Image: big}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("oversized err = %v", err)
	}
	if _, err := o.SetProjectIcon(ctx, adminSession, ProjectIconRequest{Project: "nope", Image: pngData}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project err = %v", err)
	}

	resp, err := o.SetProjectIcon(ctx, adminSession, ProjectIconRequest{Project: "proj:shop", Image: pngData, ContentType: "image/png", RemoteAddr: "10.0.0.9:5555"})
	if err != nil {
		t.Fatalf("SetProjectIcon: %v", err)
	}
	if resp.Icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UPLOADED || !resp.Icon.HasUpload || resp.Icon.ImageContentType != "image/png" {
		t.Fatalf("icon = %+v", resp.Icon)
	}
	ev := resp.AuditEvent
	if ev.Kind != "audit.project.icon_set" || ev.EntityId != "proj:shop" || ev.Audit.Result != "success" ||
		ev.Audit.Actor != "admin@example.com" || ev.Audit.StateBefore != "detected" || ev.Audit.StateAfter != "uploaded" ||
		!strings.Contains(ev.Message, "image/png 48x48") {
		t.Fatalf("audit = %+v", ev)
	}
	img, err := o.ProjectIconImage(iconstore.Key("shop"))
	if err != nil || !bytes.Equal(img.Data, pngData) || img.ContentType != "image/png" || img.ETag != resp.Icon.ImageEtag {
		t.Fatalf("served image: %v %+v", err, img.ContentType)
	}

	// Clear reverts to automatic resolution.
	if _, err := o.ClearProjectIcon(ctx, readOnlyToken, ProjectIconRequest{Project: "shop"}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("clear by token err = %v", err)
	}
	resp, err = o.ClearProjectIcon(ctx, adminSession, ProjectIconRequest{Project: "shop"})
	if err != nil || resp.Icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED || resp.Icon.ImageUrl != "" || resp.Icon.HasUpload {
		t.Fatalf("clear: %v %+v", err, resp.GetIcon())
	}
	if _, err := o.ProjectIconImage(iconstore.Key("shop")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("image still served: %v", err)
	}

	// Every attempt is in the audit log: 2 token refusals, 3 invalid, set, clear refusal, clear.
	audits := 0
	denied := 0
	for _, e := range o.ListEvents("", 0) {
		if strings.HasPrefix(e.Kind, "audit.project.") {
			audits++
			if e.Audit.Result == "denied" {
				denied++
			}
		}
	}
	if audits != 8 || denied != 3 {
		t.Fatalf("audits = %d denied = %d", audits, denied)
	}
}

func TestIconOpsNeedStore(t *testing.T) {
	o := New(bus.New(8), ring.New(10), nil, &listInventory{projects: []*asterismv1.Project{{Id: "proj:a", Name: "a"}}}, "ssh")
	o.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := o.SetProjectIcon(context.Background(), adminSession, ProjectIconRequest{Project: "a", Image: []byte(testSVG)}); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("err = %v", err)
	}
	if o.Capabilities(adminSession).CanEditProjects {
		t.Fatal("editing advertised without a store")
	}
}

func TestDiscoverFavicons(t *testing.T) {
	svg := []byte(testSVG)
	sum := sha256.Sum256(svg)
	digest := hex.EncodeToString(sum[:])
	dirs := &fakeDirs{
		refs: map[string]DirIconRef{"/srv/shop": {WorkingDir: "/srv/shop", RelPath: "public/favicon.svg", SHA256: digest}},
		data: map[string][]byte{digest: svg},
	}
	pngData := testPNG(t)
	fetch := &fakeFetcher{res: map[string]favicon.Result{
		"www.blog.example.com": {Data: pngData, Image: favicon.Image{ContentType: "image/png", Width: 48, Height: 48}, URL: "https://www.blog.example.com/icon.png"},
	}}
	o, st := iconTestService(t, dirs, fetch, nil)
	ctx := context.Background()

	hosts := o.projectHostnames()
	if got := strings.Join(hosts["proj:blog"], ","); got != "blog.example.com,www.blog.example.com" {
		t.Fatalf("blog hostnames = %q", got)
	}
	if len(hosts["proj:edge"]) != 0 {
		t.Fatal("the proxy project must not inherit upstream hostnames")
	}

	o.DiscoverIcons(ctx, 5)
	shop := project(t, o, "proj:shop")
	if shop.Icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON || shop.Icon.FaviconSource != asterismv1.FaviconSource_FAVICON_SOURCE_DIRECTORY || shop.Icon.FaviconOrigin != "public/favicon.svg" {
		t.Fatalf("shop icon = %+v", shop.Icon)
	}
	blog := project(t, o, "proj:blog")
	if blog.Icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON || blog.Icon.FaviconSource != asterismv1.FaviconSource_FAVICON_SOURCE_DOMAIN {
		t.Fatalf("blog icon = %+v", blog.Icon)
	}
	// blog.example.com failed first, then www succeeded.
	if strings.Join(fetch.hosts, ",") != "blog.example.com,www.blog.example.com" {
		t.Fatalf("fetched %v", fetch.hosts)
	}
	edge := project(t, o, "proj:edge")
	if edge.Icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED || !strings.Contains(edge.Icon.FaviconStatus, "no public hostname") {
		t.Fatalf("edge icon = %+v", edge.Icon)
	}

	// A second pass is served from cache: no reads, no fetches.
	reads, fetches := dirs.reads.Load(), len(fetch.hosts)
	o.DiscoverIcons(ctx, 5)
	if dirs.reads.Load() != reads || len(fetch.hosts) != fetches {
		t.Fatalf("second pass did work: reads %d->%d fetches %d->%d", reads, dirs.reads.Load(), fetches, len(fetch.hosts))
	}

	// After the TTL the domain is fetched again; a failure keeps the old image.
	o.icons.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	delete(fetch.res, "www.blog.example.com")
	o.DiscoverIcons(ctx, 5)
	if len(fetch.hosts) == fetches {
		t.Fatal("expired favicon not refetched")
	}
	blog = project(t, o, "proj:blog")
	if blog.Icon.Kind != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON {
		t.Fatalf("old favicon dropped after a failed refetch: %+v", blog.Icon)
	}
	if m, _ := st.Get(iconstore.SlotFavicon, "blog"); !strings.Contains(m.Status, "no favicon found") {
		t.Fatalf("negative status = %q", m.Status)
	}

	// The fetch budget limits network work per pass.
	fetch.hosts = nil
	o.icons.now = func() time.Time { return time.Now().Add(50 * time.Hour) }
	o.DiscoverIcons(ctx, 0)
	if len(fetch.hosts) != 0 {
		t.Fatalf("budget 0 fetched %v", fetch.hosts)
	}
}

func TestRefreshProjectIcon(t *testing.T) {
	fetch := &fakeFetcher{res: map[string]favicon.Result{}}
	disabled := func(name string) bool { return name != "blog" }
	o, _ := iconTestService(t, nil, fetch, disabled)
	ctx := context.Background()
	if _, err := o.RefreshProjectIcon(ctx, readOnlyToken, ProjectIconRequest{Project: "blog"}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("token refresh err = %v", err)
	}
	resp, err := o.RefreshProjectIcon(ctx, adminSession, ProjectIconRequest{Project: "blog"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fetch.hosts) != 0 {
		t.Fatalf("per-project disable ignored: fetched %v", fetch.hosts)
	}
	if !strings.Contains(resp.Icon.FaviconStatus, "off for this project") || resp.AuditEvent.Kind != "audit.project.icon_refresh" {
		t.Fatalf("resp = %+v", resp)
	}
}
