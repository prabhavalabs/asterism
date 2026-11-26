package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// projectID accepts "shop" or "proj:shop".
func projectID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, ":") {
		return s
	}
	return "proj:" + s
}

func roleName(r asterismv1.WorkloadRole) string { return enumName(r.String(), "WORKLOAD_ROLE_") }

// enumFlag resolves a short name such as "public" to an enum number.
func enumFlag(flag, short, prefix string, values map[string]int32) (int32, error) {
	short = strings.TrimSpace(short)
	if short == "" || short == "all" {
		return 0, nil
	}
	if v, ok := values[prefix+strings.ToUpper(short)]; ok && v != 0 {
		return v, nil
	}
	var opts []string
	for name, v := range values {
		if v != 0 {
			opts = append(opts, strings.ToLower(strings.TrimPrefix(name, prefix)))
		}
	}
	slices.Sort(opts)
	return 0, fmt.Errorf("--%s: unknown value %q; use one of: %s", flag, short, strings.Join(opts, ", "))
}

// ---- ps ---------------------------------------------------------------------

func newPsCmd() *cobra.Command {
	var v view
	var project, role, query, sort string
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List workloads (containers, systemd units, processes)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := enumFlag("role", role, "WORKLOAD_ROLE_", asterismv1.WorkloadRole_value)
			if err != nil {
				return err
			}
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				var facets *asterismv1.ListWorkloadsResponse
				items, info, err := collect(ctx, func(ctx context.Context, page *asterismv1.PageRequest) ([]*asterismv1.Workload, *asterismv1.PageInfo, error) {
					resp, err := call(ctx, c.ListWorkloads, &asterismv1.ListWorkloadsRequest{
						ProjectId: projectID(project), Role: asterismv1.WorkloadRole(r), Query: query, OrderBy: sort, Page: page,
					})
					if facets == nil {
						facets = resp
					}
					return resp.GetWorkloads(), resp.GetPageInfo(), err
				})
				if err != nil {
					return err
				}
				resp := &asterismv1.ListWorkloadsResponse{Workloads: items, PageInfo: info,
					RoleCounts: facets.GetRoleCounts(), ProjectCounts: facets.GetProjectCounts()}
				if v.json {
					return writeProto(v.w, resp)
				}
				renderPs(v.w, v.th, resp)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&project, "project", "p", "", "Only workloads of this project (name or proj:<name>)")
	cmd.Flags().StringVar(&role, "role", "", "Only this role: proxy, app, worker, database, cache, queue, storage")
	cmd.Flags().StringVarP(&query, "query", "q", "", "Case-insensitive substring over name, id, image, service and state")
	cmd.Flags().StringVar(&sort, "sort", "", `Server-side order, e.g. "memory desc", "cpu desc", "restarts desc" (default "project, name")`)
	v.bind(cmd)
	return cmd
}

func (t theme) state(state string, health asterismv1.HealthStatus) string {
	s := clean(orDash(state))
	switch {
	case health == asterismv1.HealthStatus_HEALTH_STATUS_DOWN, state == "dead", state == "restarting":
		return t.bad(s)
	case health == asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, state == "paused", state == "exited", state == "created":
		return t.warn(s)
	case state == "running", state == "active":
		return t.ok(s)
	default:
		return s
	}
}

func portsText(ports []*asterismv1.PortBinding) string {
	var out []string
	for _, p := range ports {
		s := fmt.Sprintf("%d", p.GetContainerPort())
		if hp := p.GetHostPort(); hp > 0 && hp != p.GetContainerPort() {
			s = fmt.Sprintf("%d→%d", hp, p.GetContainerPort())
		} else if hp > 0 {
			s = fmt.Sprintf("%d", hp)
		}
		if p.GetExposure() == asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC {
			s += "*"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func renderPs(w io.Writer, th theme, r *asterismv1.ListWorkloadsResponse) {
	if len(r.GetWorkloads()) == 0 {
		fmt.Fprintln(w, th.dim("No workloads match."))
		return
	}
	t := newTable(
		column{title: "ID", max: 36}, column{title: "PROJECT", max: 20}, column{title: "ROLE"},
		column{title: "STATE"}, column{title: "HEALTH"}, column{title: "CPU", right: true},
		column{title: "MEM", right: true}, column{title: "MEM%", right: true}, column{title: "RESTARTS", right: true},
		column{title: "PORTS", max: 24}, column{title: "IMAGE", max: 48},
	)
	var restarts int
	for _, wl := range r.GetWorkloads() {
		memPct := "-"
		if wl.GetMemLimitBytes() > 0 {
			memPct = th.pct(share(wl.GetMemUsedBytes(), wl.GetMemLimitBytes()))
		}
		rc := fmt.Sprintf("%d", wl.GetRestartCount())
		if wl.GetRestartCount() > 0 {
			restarts++
			rc = th.warn(rc)
		}
		t.add(clean(wl.GetId()), clean(orDash(short(wl.GetProjectId()))), orDash(roleName(wl.GetRole())),
			th.state(wl.GetState(), wl.GetHealth()), th.health(wl.GetHealth()), pctText(wl.GetCpuUtil()),
			bytesText(wl.GetMemUsedBytes()), memPct, rc, orDash(portsText(wl.GetPorts())), clean(orDash(wl.GetImage())))
	}
	t.render(w, th)
	var roles []string
	for _, rc := range r.GetRoleCounts() {
		if n := roleName(rc.GetRole()); n != "" {
			roles = append(roles, fmt.Sprintf("%d %s", rc.GetCount(), n))
		}
	}
	summary := fmt.Sprintf("%d of %d workloads", len(r.GetWorkloads()), r.GetPageInfo().GetTotalSize())
	if len(roles) > 0 {
		summary += " · " + strings.Join(roles, ", ")
	}
	if restarts > 0 {
		summary += " · " + plural(restarts, "workload restarted", "workloads restarted")
	}
	fmt.Fprintf(w, "\n%s\n%s\n", th.dim(summary), th.dim("* public port"))
}

// ---- ports ------------------------------------------------------------------

func newPortsCmd() *cobra.Command {
	var v view
	var exposure, query string
	cmd := &cobra.Command{
		Use:   "ports",
		Short: "List open host and container ports and the workloads that own them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := enumFlag("exposure", exposure, "PORT_EXPOSURE_", asterismv1.PortExposure_value)
			if err != nil {
				return err
			}
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				var counts *asterismv1.PortExposureCounts
				items, info, err := collect(ctx, func(ctx context.Context, page *asterismv1.PageRequest) ([]*asterismv1.PortBinding, *asterismv1.PageInfo, error) {
					resp, err := call(ctx, c.ListPorts, &asterismv1.ListPortsRequest{Exposure: asterismv1.PortExposure(e), Query: query, Page: page})
					if counts == nil {
						counts = resp.GetExposureCounts()
					}
					return resp.GetPorts(), resp.GetPageInfo(), err
				})
				if err != nil {
					return err
				}
				resp := &asterismv1.ListPortsResponse{Ports: items, PageInfo: info, ExposureCounts: counts}
				if v.json {
					return writeProto(v.w, resp)
				}
				renderPorts(v.w, v.th, resp)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&exposure, "exposure", "", "Only this exposure: public, loopback, internal")
	cmd.Flags().StringVarP(&query, "query", "q", "", "Substring over workload, process, address, project and protocol; a number matches a port")
	v.bind(cmd)
	return cmd
}

func (t theme) exposure(e asterismv1.PortExposure) string {
	name := orDash(enumName(e.String(), "PORT_EXPOSURE_"))
	switch e {
	case asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC:
		return t.warn(name)
	case asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK:
		return t.ok(name)
	default:
		return t.dim(name)
	}
}

func listenText(p *asterismv1.PortBinding) string {
	if p.GetHostPort() == 0 {
		return "-"
	}
	ip := p.GetHostIp()
	if ip == "" {
		ip = "*"
	}
	if strings.Contains(ip, ":") {
		ip = "[" + ip + "]"
	}
	return fmt.Sprintf("%s:%d", clean(ip), p.GetHostPort())
}

func renderPorts(w io.Writer, th theme, r *asterismv1.ListPortsResponse) {
	if len(r.GetPorts()) == 0 {
		fmt.Fprintln(w, th.dim("No open ports match."))
		return
	}
	t := newTable(
		column{title: "EXPOSURE"}, column{title: "LISTEN"}, column{title: "PROTO"},
		column{title: "TARGET", right: true}, column{title: "WORKLOAD", max: 36},
		column{title: "PROJECT", max: 20}, column{title: "PROCESS", max: 20},
	)
	for _, p := range r.GetPorts() {
		target := "-"
		if p.GetContainerPort() > 0 {
			target = fmt.Sprintf("%d", p.GetContainerPort())
		}
		wl := p.GetWorkloadId()
		if wl == "" || wl == "host" {
			wl = orDash(p.GetWorkloadName())
		}
		t.add(th.exposure(p.GetExposure()), listenText(p), clean(orDash(p.GetProto())), target, clean(wl),
			clean(orDash(short(p.GetProjectId()))), clean(orDash(p.GetProcessName())))
	}
	t.render(w, th)
	c := r.GetExposureCounts()
	fmt.Fprintf(w, "\n%s\n", th.dim(fmt.Sprintf("%d of %d ports · %d public · %d loopback · %d internal",
		len(r.GetPorts()), r.GetPageInfo().GetTotalSize(), c.GetPublic(), c.GetLoopback(), c.GetInternal())))
}

// ---- ingress ----------------------------------------------------------------

func newIngressCmd() *cobra.Command {
	var v view
	var query string
	cmd := &cobra.Command{
		Use:   "ingress",
		Short: "Show the Nginx / Caddy / Traefik / Cloudflare Tunnel routing table",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				items, info, err := collect(ctx, func(ctx context.Context, page *asterismv1.PageRequest) ([]*asterismv1.IngressRoute, *asterismv1.PageInfo, error) {
					resp, err := call(ctx, c.ListIngress, &asterismv1.ListIngressRequest{Query: query, Page: page})
					return resp.GetRoutes(), resp.GetPageInfo(), err
				})
				if err != nil {
					return err
				}
				resp := &asterismv1.ListIngressResponse{Routes: items, PageInfo: info}
				if v.json {
					return writeProto(v.w, resp)
				}
				renderIngress(v.w, v.th, resp)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&query, "query", "q", "", "Substring over hostnames, path, strategy, proxy and upstreams")
	v.bind(cmd)
	return cmd
}

func proxyName(k asterismv1.ProxyKind) string { return enumName(k.String(), "PROXY_KIND_") }

func renderIngress(w io.Writer, th theme, r *asterismv1.ListIngressResponse) {
	routes := r.GetRoutes()
	if len(routes) == 0 {
		fmt.Fprintln(w, th.dim("No ingress routes discovered."))
		return
	}
	addrWidth := 0
	for _, rt := range routes {
		for _, u := range rt.GetUpstreams() {
			addrWidth = max(addrWidth, len(clean(u.GetAddress())))
		}
	}
	t := newTable(
		column{title: "PROXY"}, column{title: "HOSTS", max: 48}, column{title: "PORT", right: true},
		column{title: "TLS"}, column{title: "PATH", max: 24}, column{title: "STRATEGY"}, column{title: "UPSTREAMS", right: true},
		column{title: "VIA", max: 32},
	)
	var unhealthy int
	for _, rt := range routes {
		tls := th.dim("no")
		if rt.GetTls() {
			tls = th.ok("yes")
		}
		healthy := 0
		for _, u := range rt.GetUpstreams() {
			if u.GetHealthy() {
				healthy++
			}
		}
		ups := fmt.Sprintf("%d/%d", healthy, len(rt.GetUpstreams()))
		if healthy < len(rt.GetUpstreams()) {
			ups = th.warn(ups)
		}
		hosts := make([]string, 0, len(rt.GetHostnames()))
		for _, h := range rt.GetHostnames() {
			hosts = append(hosts, clean(h))
		}
		t.add(th.accent(orDash(proxyName(rt.GetProxyKind()))), th.bold(orDash(strings.Join(hosts, ", "))),
			fmt.Sprintf("%d", rt.GetListenPort()), tls, clean(orDash(rt.GetPathPrefix())), clean(orDash(rt.GetStrategy())),
			ups, clean(orDash(rt.GetProxyWorkloadId())))
		for i, u := range rt.GetUpstreams() {
			branch := "├─▶"
			if i == len(rt.GetUpstreams())-1 {
				branch = "└─▶"
			}
			state := th.ok("healthy")
			if !u.GetHealthy() {
				state = th.bad("unhealthy")
				unhealthy++
			}
			var flags []string
			if u.GetWeight() > 0 {
				flags = append(flags, fmt.Sprintf("weight %d", u.GetWeight()))
			}
			if u.GetBackup() {
				flags = append(flags, "backup")
			}
			addr := clean(u.GetAddress())
			line := fmt.Sprintf("  %s %s%s  %s  %s", th.dim(branch), addr, strings.Repeat(" ", addrWidth-len(addr)),
				state, th.dim(strings.Join(append([]string{orDash(clean(u.GetWorkloadId()))}, flags...), " · ")))
			t.addExtra(line)
		}
	}
	t.render(w, th)
	summary := plural(len(routes), "route", "routes")
	if unhealthy > 0 {
		summary += " · " + th.bad(plural(unhealthy, "unhealthy upstream", "unhealthy upstreams"))
	}
	fmt.Fprintf(w, "\n%s\n", th.dim(summary))
}
