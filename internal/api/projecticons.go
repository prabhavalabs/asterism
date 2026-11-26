package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"github.com/prabhavalabs/asterism/internal/service"
)

// iconCSP keeps an SVG icon inert even when opened directly: no scripts,
// no subresources, sandboxed document.
const iconCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// maxIconRequestBytes bounds a SetProjectIcon request (512 KiB image plus
// protobuf/JSON framing; JSON encodes bytes as base64).
const maxIconRequestBytes = 1 << 20

// registerProjectSettings mounts ProjectSettingsService and the icon image
// endpoint.
func (h *Handler) registerProjectSettings(mount func(string, http.Handler)) {
	mount(asterismv1connect.NewProjectSettingsServiceHandler(h,
		connect.WithRequireConnectProtocolHeader(),
		connect.WithReadMaxBytes(maxIconRequestBytes)))
	mount(service.ProjectIconPath, http.HandlerFunc(h.serveProjectIcon))
}

func (h *Handler) SetProjectIcon(ctx context.Context, req *connect.Request[asterismv1.SetProjectIconRequest]) (*connect.Response[asterismv1.ProjectIconResponse], error) {
	return respond(h.svc.SetProjectIcon(ctx, principal(ctx), service.ProjectIconRequest{
		Project: req.Msg.Project, Image: req.Msg.Image, ContentType: req.Msg.ContentType, RemoteAddr: req.Peer().Addr,
	}))
}

func (h *Handler) ClearProjectIcon(ctx context.Context, req *connect.Request[asterismv1.ClearProjectIconRequest]) (*connect.Response[asterismv1.ProjectIconResponse], error) {
	return respond(h.svc.ClearProjectIcon(ctx, principal(ctx), service.ProjectIconRequest{Project: req.Msg.Project, RemoteAddr: req.Peer().Addr}))
}

func (h *Handler) RefreshProjectIcon(ctx context.Context, req *connect.Request[asterismv1.RefreshProjectIconRequest]) (*connect.Response[asterismv1.ProjectIconResponse], error) {
	return respond(h.svc.RefreshProjectIcon(ctx, principal(ctx), service.ProjectIconRequest{Project: req.Msg.Project, RemoteAddr: req.Peer().Addr}))
}

// serveProjectIcon serves GET /api/project-icons/<key>. Callers are already
// authenticated (read scope) by the mount wrapper.
func (h *Handler) serveProjectIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, service.ProjectIconPath)
	img, err := h.svc.ProjectIconImage(key)
	hdr := w.Header()
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Security-Policy", iconCSP)
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
	if err != nil {
		hdr.Set("Cache-Control", "no-store")
		http.Error(w, "icon not found", http.StatusNotFound)
		return
	}
	etag := `"` + img.ETag + `"`
	hdr.Set("ETag", etag)
	// image_url carries ?v=<etag>, so a long private cache is safe.
	hdr.Set("Cache-Control", "private, max-age=86400")
	hdr.Set("Vary", "Cookie, Authorization")
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hdr.Set("Content-Type", img.ContentType)
	hdr.Set("Content-Length", strconv.Itoa(len(img.Data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(img.Data)
}

func etagMatch(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		if p == "*" || p == etag || strings.TrimPrefix(p, "W/") == etag {
			return true
		}
	}
	return false
}
