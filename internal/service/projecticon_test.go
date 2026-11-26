package service

import (
	"testing"

	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

func TestDetectTech(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"postgres:17.4-alpine", "postgres"},
		{"docker.io/library/postgres:16", "postgres"},
		{"bitnami/postgresql:15", "postgres"},
		{"pgvector/pgvector:pg16", "postgres"},
		{"timescale/timescaledb:2.18-pg17", "timescale"},
		{"timescale/timescaledb-ha:pg17", "timescale"},
		{"mysql:8.4", "mysql"},
		{"mariadb:11", "mariadb"},
		{"mongo:7", "mongodb"},
		{"redis:7.4-alpine", "redis"},
		{"redis/redis-stack-server:latest", "redis"},
		{"valkey/valkey:8.1-alpine", "valkey"},
		{"nginx:1.27", "nginx"},
		{"nginx 1.28.0 (host systemd)", "nginx"},
		{"jc21/nginx-proxy-manager:latest", "nginx"},
		{"caddy:2.10-alpine", "caddy"},
		{"traefik:v3.2", "traefik"},
		{"cloudflare/cloudflared:2026.9.0", "cloudflared"},
		{"node:22-alpine", "node"},
		{"nodered/node-red:4", "nodered"},
		{"python:3.13-slim", "python"},
		{"golang:1.25", "go"},
		{"rabbitmq:4-management", "rabbitmq"},
		{"nats:2.11-alpine", "nats"},
		{"minio/minio:RELEASE.2025-01-01", "minio"},
		{"dxflrs/garage:v1.0.1", "garage"},
		{"grafana/grafana:11.4.0", "grafana"},
		{"grafana/loki:3", "grafana"},
		{"prom/prometheus:v3.0.0", "prometheus"},
		{"prom/node-exporter:v1.8", "prometheus"},
		{"quay.io/prometheuscommunity/postgres-exporter:v0.16", "prometheus"},
		{"wordpress:6-php8.3-apache", "wordpress"},
		{"louislam/uptime-kuma:1", "uptimekuma"},
		{"mcr.microsoft.com/dotnet/aspnet:9.0", "dotnet"},
		{"restic/restic:0.17.3", "restic"},
		{"registry.example.com:5000/team/nginx@sha256:abcd", "nginx"},
		{"ghcr.io/example/core-api:v2.4.1", ""},
		{"ghcr.io/acme/postgresql-backup-tool:1", "postgres"},
		{"ghcr.io/acme/nodeapp:1", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := DetectTech(tc.image); got != tc.want {
			t.Errorf("DetectTech(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

func TestTechCatalogIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range techCatalog {
		if tc.Slug == "" || tc.Label == "" {
			t.Fatalf("catalogue entry missing slug or label: %+v", tc)
		}
		if seen[tc.Slug] {
			t.Fatalf("duplicate slug %q", tc.Slug)
		}
		seen[tc.Slug] = true
		if tc.Color() == "" {
			t.Fatalf("%s: category %q has no colour", tc.Slug, tc.Category)
		}
		if len(tc.Names) == 0 && len(tc.Orgs) == 0 {
			t.Fatalf("%s: no image names or orgs", tc.Slug)
		}
	}
}

func TestMonogram(t *testing.T) {
	tests := []struct{ in, want string }{
		{"staging-lab", "SL"},
		{"analytics_hub", "AH"},
		{"shop", "SH"},
		{"x", "X"},
		{"my cool app", "MC"},
		{"42-things", "4T"},
		{"--", "?"},
		{"", "?"},
		{"élan-vital", "ÉV"},
	}
	for _, tc := range tests {
		if got := Monogram(tc.in); got != tc.want {
			t.Errorf("Monogram(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMonogramColorIsDeterministicPaletteToken(t *testing.T) {
	for _, name := range []string{"shop", "staging-lab", "edge", "Shop"} {
		c := MonogramColor(name)
		if !validColorToken(c) || c == "primary" {
			t.Fatalf("MonogramColor(%q) = %q, not a palette token", name, c)
		}
		if again := MonogramColor(name); again != c {
			t.Fatalf("MonogramColor(%q) not deterministic: %q vs %q", name, c, again)
		}
	}
	if MonogramColor("shop") != MonogramColor("SHOP") {
		t.Fatal("MonogramColor should ignore case")
	}
}

func TestResolveProjectIcon(t *testing.T) {
	core := []iconSource{
		{id: "ctr:api", image: "ghcr.io/acme/api:1", mem: 300},
		{id: "ctr:pg", image: "postgres:17", mem: 200},
		{id: "ctr:redis", image: "redis:7", mem: 900},
		{id: "ctr:nginx", image: "nginx:1.27", mem: 50},
	}
	tests := []struct {
		name      string
		project   string
		cfg       ProjectDisplay
		sources   []iconSource
		wantKind  asterismv1.ProjectIconKind
		wantSlug  string
		wantColor string
		wantLabel string
	}{
		{
			name: "configured icon and colour win", project: "shop",
			cfg:      ProjectDisplay{Icon: "wordpress", Color: "chart-5"},
			sources:  append([]iconSource{{id: "ctr:a", image: "x", labels: map[string]string{LabelIcon: "grafana", LabelColor: "role-db"}}}, core...),
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED, wantSlug: "wordpress", wantColor: "chart-5", wantLabel: "WordPress",
		},
		{
			name: "configured colour accepts var() and dashes", project: "shop",
			cfg:      ProjectDisplay{Icon: "Grafana", Color: "var(--role-cache)"},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED, wantSlug: "grafana", wantColor: "role-cache", wantLabel: "Grafana",
		},
		{
			name: "unknown configured slug falls through to the label", project: "shop",
			cfg:      ProjectDisplay{Icon: "made-up"},
			sources:  append([]iconSource{{id: "ctr:z", image: "x", labels: map[string]string{LabelIcon: "grafana"}}}, core...),
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL, wantSlug: "grafana", wantColor: "chart-2", wantLabel: "Grafana",
		},
		{
			name: "label colour applies to the label icon", project: "shop",
			sources:  []iconSource{{id: "ctr:z", image: "x", labels: map[string]string{LabelIcon: "node", LabelColor: "chart-4"}}},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL, wantSlug: "node", wantColor: "chart-4", wantLabel: "Node.js",
		},
		{
			name: "first label by container id wins", project: "shop",
			sources: []iconSource{
				{id: "ctr:b", labels: map[string]string{LabelIcon: "redis"}},
				{id: "ctr:a", labels: map[string]string{LabelIcon: "mysql"}},
			},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL, wantSlug: "mysql", wantColor: "role-db", wantLabel: "MySQL",
		},
		{
			name: "database outranks cache and proxy", project: "core", sources: core,
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, wantSlug: "postgres", wantColor: "role-db", wantLabel: "PostgreSQL",
		},
		{
			name: "apps outrank databases", project: "blog",
			sources:  []iconSource{{id: "ctr:db", image: "mariadb:11"}, {id: "ctr:wp", image: "wordpress:6"}},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, wantSlug: "wordpress", wantColor: "chart-1", wantLabel: "WordPress",
		},
		{
			name: "same priority: more containers win", project: "edge",
			sources: []iconSource{
				{id: "ctr:c1", image: "caddy:2", mem: 10},
				{id: "ctr:n1", image: "nginx:1", mem: 500},
				{id: "ctr:c2", image: "caddy:2", mem: 10},
			},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, wantSlug: "caddy", wantColor: "role-proxy", wantLabel: "Caddy",
		},
		{
			name: "same priority and count: more memory wins", project: "edge",
			sources: []iconSource{
				{id: "ctr:c1", image: "caddy:2", mem: 10},
				{id: "ctr:n1", image: "nginx:1", mem: 500},
			},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, wantSlug: "nginx", wantColor: "role-proxy", wantLabel: "Nginx",
		},
		{
			name: "project named after a technology", project: "uptime-kuma",
			sources:  []iconSource{{id: "ctr:x", image: "ghcr.io/acme/thing:1"}},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, wantSlug: "uptimekuma", wantColor: "chart-2", wantLabel: "Uptime Kuma",
		},
		{
			name: "configured colour tints a detected icon", project: "core", cfg: ProjectDisplay{Color: "chart-3"}, sources: core,
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, wantSlug: "postgres", wantColor: "chart-3", wantLabel: "PostgreSQL",
		},
		{
			name: "nothing recognisable: monogram", project: "staging-lab",
			sources:  []iconSource{{id: "ctr:x", image: "ghcr.io/acme/preview:sha-1"}},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_MONOGRAM, wantColor: MonogramColor("staging-lab"),
		},
		{
			name: "invalid colours are ignored", project: "staging-lab", cfg: ProjectDisplay{Color: "#ff0000"},
			sources:  []iconSource{{id: "ctr:x", labels: map[string]string{LabelColor: "red"}}},
			wantKind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_MONOGRAM, wantColor: MonogramColor("staging-lab"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveProjectIcon(tc.project, tc.cfg, tc.sources)
			if got.Kind != tc.wantKind || got.Slug != tc.wantSlug || got.ColorToken != tc.wantColor || got.Label != tc.wantLabel {
				t.Fatalf("got kind=%v slug=%q color=%q label=%q; want kind=%v slug=%q color=%q label=%q",
					got.Kind, got.Slug, got.ColorToken, got.Label, tc.wantKind, tc.wantSlug, tc.wantColor, tc.wantLabel)
			}
			if got.Monogram != Monogram(tc.project) {
				t.Fatalf("monogram = %q, want %q", got.Monogram, Monogram(tc.project))
			}
		})
	}
}

type fakeInventory struct{ projects []*asterismv1.Project }

func (f *fakeInventory) Projects() []*asterismv1.Project { return f.projects }
func (f *fakeInventory) Workloads() []*asterismv1.Workload {
	var out []*asterismv1.Workload
	for _, p := range f.projects {
		out = append(out, p.Workloads...)
	}
	return out
}
func (f *fakeInventory) Workload(id string) (*asterismv1.Workload, bool) {
	for _, w := range f.Workloads() {
		if w.Id == id {
			return w, true
		}
	}
	return nil, false
}
func (f *fakeInventory) Ports() []*asterismv1.PortBinding          { return nil }
func (f *fakeInventory) IngressRoutes() []*asterismv1.IngressRoute { return nil }
func (f *fakeInventory) Edges() []*asterismv1.TopologyEdge         { return nil }
func (f *fakeInventory) Events() []*asterismv1.Event               { return nil }
func (f *fakeInventory) Logs(string) []*asterismv1.LogLine         { return nil }

func TestListProjectsDecoratesCopies(t *testing.T) {
	inv := &fakeInventory{projects: []*asterismv1.Project{
		{Id: "proj:shop", Name: "shop", Category: "production", Workloads: []*asterismv1.Workload{
			{Id: "ctr:shop-api", Name: "shop-api", ProjectId: "proj:shop", Image: "ghcr.io/acme/shop-api:1"},
			{Id: "ctr:shop-db", Name: "shop-db", ProjectId: "proj:shop", Image: "postgres:17"},
		}},
		{Id: "proj:lab", Name: "lab", Category: "staging", Workloads: []*asterismv1.Workload{
			{Id: "ctr:lab-web", Name: "lab-web", Image: "ghcr.io/acme/lab:1"},
		}},
	}}
	o := New(bus.New(8), ring.New(10), nil, inv, "showcase")
	fd := &fakeDocker{containers: []*asterismv1.Container{
		// A stopped container outside the topology still contributes its label.
		{Id: "ctr:lab-job", Name: "lab-job", ProjectId: "proj:lab", Image: "ghcr.io/acme/job:1", Labels: map[string]string{LabelIcon: "python"}},
	}}
	o.ConfigureDocker(fd, nil, ActionPolicy{})
	o.ConfigureProjects(map[string]ProjectDisplay{"shop": {Color: "chart-5"}})

	got := o.ListProjects("")
	if len(got) != 2 {
		t.Fatalf("got %d projects", len(got))
	}
	shop, lab := got[0], got[1]
	if shop.Icon.GetSlug() != "postgres" || shop.Icon.GetColorToken() != "chart-5" || shop.Icon.GetKind() != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED {
		t.Fatalf("shop icon = %+v", shop.Icon)
	}
	if shop.Workloads[1].Tech != "postgres" || shop.Workloads[0].Tech != "" {
		t.Fatalf("workload tech = %q, %q", shop.Workloads[0].Tech, shop.Workloads[1].Tech)
	}
	if lab.Icon.GetKind() != asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL || lab.Icon.GetSlug() != "python" {
		t.Fatalf("lab icon = %+v", lab.Icon)
	}
	// The provider's objects must stay untouched.
	if inv.projects[0].Icon != nil || inv.projects[0].Workloads[1].Tech != "" {
		t.Fatal("ListProjects mutated provider-owned projects")
	}
	if cat := o.ListProjects("staging"); len(cat) != 1 || cat[0].Name != "lab" {
		t.Fatalf("category filter returned %v", cat)
	}
	ws := o.ListWorkloads("proj:shop", asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED)
	if len(ws) != 2 || ws[1].Tech != "postgres" || inv.projects[0].Workloads[1].Tech != "" {
		t.Fatalf("ListWorkloads tech not set on a copy")
	}
	ov := o.Overview(readOnlyToken)
	if ov.ContainerCounts.GetTotal() != 1 {
		t.Fatalf("overview container counts = %+v", ov.ContainerCounts)
	}
}
