package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	"github.com/prabhavalabs/asterism/internal/collect/favicon"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/iconstore"
)

// ProjectIconPath is the URL prefix of the authenticated icon endpoint.
const ProjectIconPath = "/api/project-icons/"

// Icon sources recorded in iconstore.Meta.Source.
const (
	iconSourceUpload    = "upload"
	iconSourceDirectory = "directory"
	iconSourceDomain    = "domain"
	iconSourceGenerated = "generated"
)

// DirIconRef is a favicon candidate found in a project's working directory.
type DirIconRef = favicon.DirRef

// DirIconSource reports favicon files found in compose working directories:
// the hub's own scan in local mode, the agent's scan in agent mode, and
// generated images in showcase mode.
type DirIconSource interface {
	DirIcon(workingDir string) (DirIconRef, bool)
	ReadDirIcon(ctx context.Context, ref DirIconRef) ([]byte, error)
}

// FaviconFetcher downloads a favicon from a public hostname.
type FaviconFetcher interface {
	Fetch(ctx context.Context, host string) (favicon.Result, error)
}

// IconOptions configures project icon images.
type IconOptions struct {
	Store *iconstore.Store
	// Dirs is nil when no directory scan is possible (ssh mode).
	Dirs DirIconSource
	// Fetcher is nil when favicon fetching is off globally.
	Fetcher FaviconFetcher
	// FetchAllowed reports the per-project favicon_fetch switch.
	FetchAllowed func(project string) bool
	// DomainTTL is how long a domain favicon (or a failed fetch) is kept
	// before it is fetched again (default 24h).
	DomainTTL time.Duration
	// Interval is the background discovery period (default 10 min).
	Interval time.Duration
}

type iconManager struct {
	opts  IconOptions
	locks sync.Map // project name -> *sync.Mutex
	now   func() time.Time
}

// ConfigureIcons enables uploaded and discovered project icons.
func (o *Observatory) ConfigureIcons(opts IconOptions) {
	if opts.Store == nil {
		o.icons = nil
		return
	}
	if opts.DomainTTL <= 0 {
		opts.DomainTTL = 24 * time.Hour
	}
	if opts.Interval <= 0 {
		opts.Interval = 10 * time.Minute
	}
	if opts.FetchAllowed == nil {
		opts.FetchAllowed = func(string) bool { return true }
	}
	o.icons = &iconManager{opts: opts, now: time.Now}
}

func (m *iconManager) lock(project string) func() {
	v, _ := m.locks.LoadOrStore(project, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// applyIconImages overlays uploaded and favicon images on a resolved icon.
// Precedence: UPLOADED > CONFIGURED > LABEL > FAVICON > DETECTED > MONOGRAM.
func applyIconImages(icon *asterismv1.ProjectIcon, up, fav iconstore.Meta, hasUp, hasFav bool) {
	if hasFav {
		icon.FaviconCheckedMs = fav.CheckedMs
		icon.FaviconStatus = fav.Status
		if fav.HasImage() {
			icon.FaviconSource = faviconSource(fav.Source)
			icon.FaviconOrigin = fav.Origin
			icon.FaviconStatus = ""
		}
	}
	icon.HasUpload = hasUp && up.HasImage()
	setImage := func(kind asterismv1.ProjectIconKind, m iconstore.Meta) {
		icon.Kind = kind
		icon.ImageUrl = ProjectIconPath + iconstore.Key(m.Project) + "?v=" + m.ETag()
		icon.ImageEtag = m.ETag()
		icon.ImageContentType = m.ContentType
	}
	switch {
	case icon.HasUpload:
		setImage(asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UPLOADED, up)
	case icon.Kind == asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED,
		icon.Kind == asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL:
	case hasFav && fav.HasImage():
		setImage(asterismv1.ProjectIconKind_PROJECT_ICON_KIND_FAVICON, fav)
	}
}

func faviconSource(s string) asterismv1.FaviconSource {
	switch s {
	case iconSourceDirectory:
		return asterismv1.FaviconSource_FAVICON_SOURCE_DIRECTORY
	case iconSourceDomain:
		return asterismv1.FaviconSource_FAVICON_SOURCE_DOMAIN
	case iconSourceGenerated:
		return asterismv1.FaviconSource_FAVICON_SOURCE_GENERATED
	}
	return asterismv1.FaviconSource_FAVICON_SOURCE_UNSPECIFIED
}

// decorateIcon applies stored images to cp.Icon.
func (o *Observatory) decorateIcon(cp *asterismv1.Project) {
	if o.icons == nil || cp.Icon == nil {
		return
	}
	st := o.icons.opts.Store
	up, hasUp := st.Get(iconstore.SlotUploaded, cp.Name)
	fav, hasFav := st.Get(iconstore.SlotFavicon, cp.Name)
	applyIconImages(cp.Icon, up, fav, hasUp, hasFav)
}

// IconImage is one served icon.
type IconImage struct {
	Data        []byte
	ContentType string
	ETag        string
}

// ProjectIconImage returns the image served at /api/project-icons/<key>:
// the uploaded image, else the cached favicon.
func (o *Observatory) ProjectIconImage(key string) (IconImage, error) {
	if o.icons == nil || !iconstore.ValidKey(key) {
		return IconImage{}, newErr(ErrNotFound, "unknown icon")
	}
	for _, sl := range []iconstore.Slot{iconstore.SlotUploaded, iconstore.SlotFavicon} {
		data, m, err := o.icons.opts.Store.Read(sl, key)
		if err == nil {
			return IconImage{Data: data, ContentType: m.ContentType, ETag: m.ETag()}, nil
		}
	}
	return IconImage{}, newErr(ErrNotFound, "unknown icon")
}

// ---------------------------------------------------------------------------
// Admin RPCs
// ---------------------------------------------------------------------------

// ProjectIconRequest is one SetProjectIcon / ClearProjectIcon /
// RefreshProjectIcon call.
type ProjectIconRequest struct {
	Project     string
	Image       []byte
	ContentType string
	RemoteAddr  string
}

const editProjectsDeniedMsg = "project settings can only be changed from a signed-in browser session; API tokens are read-only here"

// canEditProjects reports whether p may change project settings.
func canEditProjects(p auth.Principal) bool {
	return p.Kind == "session" && p.Subject != ""
}

// SetProjectIcon validates and stores an uploaded icon.
func (o *Observatory) SetProjectIcon(ctx context.Context, p auth.Principal, req ProjectIconRequest) (*asterismv1.ProjectIconResponse, error) {
	return o.projectIconOp(ctx, p, "icon_set", req, func(m *iconManager, proj *asterismv1.Project) (string, error) {
		img, err := favicon.Validate(req.Image, req.ContentType, favicon.MaxUploadBytes)
		if err != nil {
			return "", newErr(ErrInvalidArgument, "%s", err.Error())
		}
		_, err = m.opts.Store.Put(iconstore.SlotUploaded, proj.Name, req.Image, iconstore.Meta{
			ContentType: img.ContentType, Width: img.Width, Height: img.Height,
			Source: iconSourceUpload, UpdatedMs: m.now().UnixMilli(), Actor: p.Actor(),
		})
		if err != nil {
			return "", newErr(ErrInternal, "store icon: %v", err)
		}
		detail := img.ContentType
		if img.Width > 0 {
			detail = fmt.Sprintf("%s %dx%d", img.ContentType, img.Width, img.Height)
		}
		return detail, nil
	})
}

// ClearProjectIcon removes the uploaded icon.
func (o *Observatory) ClearProjectIcon(ctx context.Context, p auth.Principal, req ProjectIconRequest) (*asterismv1.ProjectIconResponse, error) {
	return o.projectIconOp(ctx, p, "icon_clear", req, func(m *iconManager, proj *asterismv1.Project) (string, error) {
		if err := m.opts.Store.Delete(iconstore.SlotUploaded, proj.Name); err != nil {
			return "", newErr(ErrInternal, "remove icon: %v", err)
		}
		return "", nil
	})
}

// RefreshProjectIcon re-runs favicon discovery for one project now.
func (o *Observatory) RefreshProjectIcon(ctx context.Context, p auth.Principal, req ProjectIconRequest) (*asterismv1.ProjectIconResponse, error) {
	return o.projectIconOp(ctx, p, "icon_refresh", req, func(m *iconManager, proj *asterismv1.Project) (string, error) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		res := o.discoverFavicon(dctx, proj, o.projectHostnames(), true)
		return res, nil
	})
}

// projectIconOp authorises, runs and audits one icon mutation. Every
// attempt by an authenticated caller produces exactly one audit event.
func (o *Observatory) projectIconOp(_ context.Context, p auth.Principal, action string, req ProjectIconRequest,
	run func(m *iconManager, proj *asterismv1.Project) (string, error)) (*asterismv1.ProjectIconResponse, error) {
	start := time.Now()
	audit := &asterismv1.ActionAudit{
		Actor:      p.Actor(),
		ActorKind:  p.Kind,
		Action:     action,
		TargetId:   dockercli.Truncate(req.Project, 140),
		RemoteAddr: req.RemoteAddr,
	}
	fail := func(result string, err error) (*asterismv1.ProjectIconResponse, error) {
		audit.Result = result
		audit.Error = err.Error()
		audit.DurationMs = uint64(time.Since(start).Milliseconds())
		o.recordProjectAudit(audit, "")
		return nil, err
	}
	proj, perr := o.GetProject(strings.TrimSpace(req.Project))
	if perr == nil {
		audit.TargetId, audit.TargetName = proj.Id, proj.Name
		audit.StateBefore = iconKindName(proj.GetIcon().GetKind())
	}
	if !canEditProjects(p) {
		return fail("denied", newErr(ErrPermissionDenied, "%s (caller %s)", editProjectsDeniedMsg, p.Actor()))
	}
	if o.icons == nil {
		return fail("denied", newErr(ErrFailedPrecondition, "project icon storage is not configured (set [server] data_dir)"))
	}
	if perr != nil {
		return fail("not_found", newErr(ErrNotFound, "unknown project %q", req.Project))
	}
	unlock := o.icons.lock(proj.Name)
	detail, err := run(o.icons, proj)
	unlock()
	if err != nil {
		result := "failure"
		if errors.Is(err, ErrInvalidArgument) {
			result = "invalid"
		}
		return fail(result, err)
	}
	after, err := o.GetProject(proj.Id)
	if err != nil {
		after = proj
	}
	audit.StateAfter = iconKindName(after.GetIcon().GetKind())
	audit.Result = "success"
	audit.DurationMs = uint64(time.Since(start).Milliseconds())
	ev := o.recordProjectAudit(audit, detail)
	return &asterismv1.ProjectIconResponse{ProjectId: after.Id, Icon: after.Icon, AuditEvent: ev}, nil
}

func iconKindName(k asterismv1.ProjectIconKind) string {
	if k == asterismv1.ProjectIconKind_PROJECT_ICON_KIND_UNSPECIFIED {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(k.String(), "PROJECT_ICON_KIND_"))
}

var projectAuditVerb = map[string]string{
	"icon_set":     "set the icon of",
	"icon_clear":   "reset the icon of",
	"icon_refresh": "refreshed the favicon of",
}

// recordProjectAudit stores and publishes one project settings audit event.
func (o *Observatory) recordProjectAudit(a *asterismv1.ActionAudit, detail string) *asterismv1.Event {
	now := time.Now()
	target := a.TargetName
	if target == "" {
		target = a.TargetId
	}
	sev := "info"
	var msg string
	if a.Result == "success" {
		msg = fmt.Sprintf("%s %s %s (%s → %s)", a.Actor, projectAuditVerb[a.Action], target, orDash(a.StateBefore), orDash(a.StateAfter))
		if detail != "" {
			msg += ": " + detail
		}
	} else {
		sev = "warning"
		msg = fmt.Sprintf("%s could not change the icon of %s: %s", a.Actor, target, a.Error)
	}
	entity := a.TargetId
	if !strings.HasPrefix(entity, "proj:") {
		entity = "host"
	}
	ev := &asterismv1.Event{
		Id:         fmt.Sprintf("audit:%d-%d", now.UnixMilli(), auditSeq.Add(1)),
		TsMs:       now.UnixMilli(),
		EntityId:   entity,
		EntityName: target,
		Kind:       "audit.project." + a.Action,
		Severity:   sev,
		Message:    msg,
		Attrs:      map[string]string{"actor": a.Actor, "action": a.Action, "result": a.Result},
		Audit:      a,
	}
	o.events.Append(ev)
	o.bus.Publish(bus.Message{Events: []*asterismv1.Event{ev}})
	o.logger.Info("audit", "action", a.Action, "result", a.Result, "actor", a.Actor, "actor_kind", a.ActorKind,
		"target", a.TargetId, "duration_ms", a.DurationMs, "remote", a.RemoteAddr, "error", a.Error)
	return ev
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// RunIconDiscovery refreshes favicons in the background: a first pass
// shortly after start, then every Interval. Each pass does at most a few
// domain fetches; directory results are cheap (cached by the source).
func (o *Observatory) RunIconDiscovery(ctx context.Context) error {
	if o.icons == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	first := time.NewTimer(20 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-first.C:
	}
	t := time.NewTicker(o.icons.opts.Interval)
	defer t.Stop()
	for {
		o.DiscoverIcons(ctx, 6)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// DiscoverIcons runs one discovery pass over every project, doing at most
// maxFetches domain fetches.
func (o *Observatory) DiscoverIcons(ctx context.Context, maxFetches int) {
	if o.icons == nil {
		return
	}
	hosts := o.projectHostnames()
	budget := maxFetches
	for _, proj := range o.ListProjects("") {
		if ctx.Err() != nil {
			return
		}
		if o.icons.domainFetchDue(proj, hosts[proj.Id]) {
			if budget <= 0 {
				continue
			}
			budget--
		}
		unlock := o.icons.lock(proj.Name)
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		o.discoverFavicon(pctx, proj, hosts, false)
		cancel()
		unlock()
	}
}

// domainFetchDue reports whether the next discovery of proj would fetch
// from the network.
func (m *iconManager) domainFetchDue(proj *asterismv1.Project, hosts []string) bool {
	if m.opts.Fetcher == nil || len(hosts) == 0 || !m.opts.FetchAllowed(proj.Name) {
		return false
	}
	if m.opts.Dirs != nil && proj.WorkingDir != "" {
		if _, ok := m.opts.Dirs.DirIcon(proj.WorkingDir); ok {
			return false
		}
	}
	fav, ok := m.opts.Store.Get(iconstore.SlotFavicon, proj.Name)
	return !ok || m.now().Sub(time.UnixMilli(fav.CheckedMs)) >= m.opts.DomainTTL
}

// discoverFavicon updates the favicon cache of proj and returns a short
// outcome. The caller holds the project lock. force ignores the domain TTL.
func (o *Observatory) discoverFavicon(ctx context.Context, proj *asterismv1.Project, hosts map[string][]string, force bool) string {
	m := o.icons
	st := m.opts.Store
	now := m.now()
	prev, hasPrev := st.Get(iconstore.SlotFavicon, proj.Name)

	// 1. A file in the project directory (or a generated showcase icon).
	if m.opts.Dirs != nil && proj.WorkingDir != "" {
		if ref, ok := m.opts.Dirs.DirIcon(proj.WorkingDir); ok {
			source := iconSourceDirectory
			if ref.Generated {
				source = iconSourceGenerated
			}
			if hasPrev && prev.HasImage() && prev.SHA256 == ref.SHA256 && prev.Source == source {
				return "favicon unchanged (" + ref.RelPath + ")"
			}
			data, err := m.opts.Dirs.ReadDirIcon(ctx, ref)
			if err == nil {
				var img favicon.Image
				img, err = favicon.Validate(data, "", favicon.MaxFaviconBytes)
				if err == nil {
					_, err = st.Put(iconstore.SlotFavicon, proj.Name, data, iconstore.Meta{
						ContentType: img.ContentType, Width: img.Width, Height: img.Height,
						Source: source, Origin: ref.RelPath, UpdatedMs: now.UnixMilli(), CheckedMs: now.UnixMilli(),
					})
				}
			}
			if err == nil {
				return "favicon found in the project directory (" + ref.RelPath + ")"
			}
			o.logger.Debug("project icon: directory candidate rejected", "project", proj.Name, "file", ref.RelPath, "err", err)
		}
	}
	if force && hasPrev && prev.Source != iconSourceDomain {
		// The directory file is gone or invalid; forget it before trying the domain.
		_ = st.Delete(iconstore.SlotFavicon, proj.Name)
		hasPrev = false
	}

	// 2. A public hostname routed to the project.
	projHosts := hosts[proj.Id]
	status := "no favicon found"
	switch {
	case m.opts.Fetcher == nil:
		status = "no favicon in the project directory; domain fetching is off ([projects] favicon_fetch)"
	case !m.opts.FetchAllowed(proj.Name):
		status = "no favicon in the project directory; domain fetching is off for this project"
	case len(projHosts) == 0:
		status = "no favicon in the project directory and no public hostname routes to it"
	default:
		if !force && hasPrev && now.Sub(time.UnixMilli(prev.CheckedMs)) < m.opts.DomainTTL {
			if prev.HasImage() {
				return "favicon cached from " + prev.Origin
			}
			return prev.Status
		}
		var errs []string
		for i, h := range projHosts {
			if i >= 3 {
				break
			}
			res, err := m.opts.Fetcher.Fetch(ctx, h)
			if err != nil {
				errs = append(errs, dockercli.Truncate(err.Error(), 200))
				continue
			}
			_, err = st.Put(iconstore.SlotFavicon, proj.Name, res.Data, iconstore.Meta{
				ContentType: res.Image.ContentType, Width: res.Image.Width, Height: res.Image.Height,
				Source: iconSourceDomain, Origin: res.URL, UpdatedMs: now.UnixMilli(), CheckedMs: now.UnixMilli(),
			})
			if err == nil {
				return "favicon fetched from " + res.URL
			}
			errs = append(errs, err.Error())
		}
		status = "no favicon found on " + strings.Join(projHosts[:min(3, len(projHosts))], ", ")
		o.logger.Debug("project icon: domain fetch failed", "project", proj.Name, "errors", strings.Join(errs, "; "))
	}
	// Negative cache; a previous domain image is kept until a fetch succeeds.
	_, _ = st.Update(iconstore.SlotFavicon, proj.Name, func(meta *iconstore.Meta) {
		meta.CheckedMs = now.UnixMilli()
		meta.Status = status
	})
	return status
}

// projectHostnames maps project ids to the public hostnames routed to their
// workloads, sorted, without wildcards or local-only names.
func (o *Observatory) projectHostnames() map[string][]string {
	out := map[string][]string{}
	if o.inventory == nil {
		return out
	}
	projOf := map[string]string{}
	for _, w := range o.inventory.Workloads() {
		projOf[w.Id] = w.ProjectId
	}
	seen := map[string]map[string]bool{}
	for _, r := range o.inventory.IngressRoutes() {
		for _, u := range r.Upstreams {
			pid := projOf[u.WorkloadId]
			if pid == "" {
				continue
			}
			for _, h := range r.Hostnames {
				h = normalizeHost(h)
				if !favicon.ValidHostname(h) {
					continue
				}
				if seen[pid] == nil {
					seen[pid] = map[string]bool{}
				}
				if !seen[pid][h] {
					seen[pid][h] = true
					out[pid] = append(out[pid], h)
				}
			}
		}
	}
	for pid := range out {
		sort.Slice(out[pid], func(i, j int) bool {
			a, b := out[pid][i], out[pid][j]
			// Shorter names first: the apex or www usually carries the favicon.
			if len(a) != len(b) {
				return len(a) < len(b)
			}
			return a < b
		})
	}
	return out
}

// normalizeHost strips a scheme, port and path from an ingress hostname.
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil {
			h = u.Hostname()
		}
	}
	if i := strings.IndexAny(h, "/"); i >= 0 {
		h = h[:i]
	}
	if host, _, ok := strings.Cut(h, ":"); ok {
		h = host
	}
	return strings.TrimSuffix(h, ".")
}
