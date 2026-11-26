package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestProjectViewIconFields(t *testing.T) {
	p := &asterismv1.Project{Id: "proj:blog", Name: "blog", Icon: &asterismv1.ProjectIcon{
		Kind: asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON, Slug: "ghost",
		ImageUrl: "/api/project-icons/abc?v=1", ImageEtag: "1", ImageContentType: "image/png",
		FaviconSource: asterismv1.FaviconSource_FAVICON_SOURCE_DOMAIN, FaviconOrigin: "https://blog.example.com/favicon.png",
	}}
	v := projectView(p)
	if v.IconKind != "favicon" || v.IconSource != "public domain: https://blog.example.com/favicon.png" || v.Icon != "ghost" {
		t.Fatalf("view = %+v", v)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "project-icons") || strings.Contains(string(raw), "image/") {
		t.Fatalf("image reference leaked into MCP output: %s", raw)
	}
	if projectView(&asterismv1.Project{}).IconKind != "" {
		t.Fatal("empty icon kind")
	}
}
