package assemble

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Converters from structured inventory (what asterism-agent sends) into the
// `docker ps`-shaped facts Build consumes, so agent hosts and SSH hosts are
// classified by exactly the same rules.

// FormatPorts renders container ports like the `docker ps` ports column:
// "0.0.0.0:8080->80/tcp, [::]:8080->80/tcp, 5432/tcp".
func FormatPorts(ports []*asterismv1.ContainerPort) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		proto := p.GetProto()
		if proto == "" {
			proto = "tcp"
		}
		if p.GetPublished() && p.GetHostPort() > 0 {
			ip := p.GetHostIp()
			if ip == "" {
				ip = "0.0.0.0"
			}
			if strings.Contains(ip, ":") {
				ip = "[" + strings.Trim(ip, "[]") + "]"
			}
			parts = append(parts, fmt.Sprintf("%s:%d->%d/%s", ip, p.GetHostPort(), p.GetContainerPort(), proto))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d/%s", p.GetContainerPort(), proto))
	}
	return strings.Join(parts, ", ")
}

// FromContainer describes a structured container record as `docker ps` would
// at now (the status is recomputed, so its age never goes stale).
func FromContainer(c *asterismv1.Container, now time.Time) ContainerFact {
	status := dockercli.HumanStatus(c, now)
	nets := make([]string, 0, len(c.GetNetworks()))
	for _, n := range c.GetNetworks() {
		if n.GetName() != "" {
			nets = append(nets, n.GetName())
		}
	}
	if len(nets) == 0 && c.GetNetworkMode() != "" && !strings.HasPrefix(c.GetNetworkMode(), "container:") {
		nets = append(nets, c.GetNetworkMode())
	}
	return ContainerFact{
		Name:       c.GetName(),
		Image:      c.GetImage(),
		Status:     status,
		PortsRaw:   FormatPorts(c.GetPorts()),
		Project:    c.GetComposeProject(),
		Service:    c.GetComposeService(),
		WorkingDir: c.GetComposeWorkingDir(),
		Networks:   nets,
	}
}

// WorkloadHealth derives an aggregate health from a unit or process state
// when the reporter left it unset.
func WorkloadHealth(state string) asterismv1.HealthStatus {
	switch strings.ToLower(state) {
	case "active", "running", "online", "listening":
		return asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY
	case "activating", "reloading", "deactivating", "restarting", "unhealthy":
		return asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED
	case "failed", "inactive", "dead", "stopped", "exited", "errored":
		return asterismv1.HealthStatus_HEALTH_STATUS_DOWN
	default:
		return asterismv1.HealthStatus_HEALTH_STATUS_UNSPECIFIED
	}
}

// PlaceHostWorkload chooses the default project for a unit or process: proxy
// units go to "edge-ingress" (as the SSH bridge places host nginx), other
// units to "host-services", processes to "host-processes". Project rules
// can move them afterwards.
func PlaceHostWorkload(w *asterismv1.Workload) HostWorkload {
	if w.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
		w.Role = ClassifyWorkloadRole(w.Name, w.ServiceName, w.Image, "")
		if r := RoleFromLabel(w.Labels["asterism.role"]); r != asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
			w.Role = r
		}
	}
	if w.Health == asterismv1.HealthStatus_HEALTH_STATUS_UNSPECIFIED {
		w.Health = WorkloadHealth(w.State)
	}
	if w.Replicas == 0 {
		w.Replicas = 1
	}
	switch {
	case w.Kind == asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT && w.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY:
		return HostWorkload{Workload: w, Project: "edge-ingress", ProjectKind: "systemd", ProjectCategory: "infra"}
	case w.Kind == asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT:
		return HostWorkload{Workload: w, Project: "host-services", ProjectKind: "systemd", ProjectCategory: "infra"}
	default:
		return HostWorkload{Workload: w, Project: "host-processes", ProjectKind: "process"}
	}
}

var traefikHostRe = regexp.MustCompile("Host\\(([^)]*)\\)")

// TraefikRoutes derives ingress routes from Traefik Docker labels
// (traefik.http.routers.<r>.rule = Host(`a`, `b`) plus
// traefik.http.services.<s>.loadbalancer.server.port) on running
// containers. The proxy is the first container whose image is traefik.
func TraefikRoutes(cs []*asterismv1.Container) []ProxyRoute {
	sorted := append([]*asterismv1.Container(nil), cs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetName() < sorted[j].GetName() })
	proxyID := ""
	for _, c := range sorted {
		if strings.Contains(strings.ToLower(c.GetImageRepo()+" "+c.GetImage()), "traefik") {
			proxyID = "ctr:" + c.GetName()
			break
		}
	}
	var out []ProxyRoute
	for _, c := range sorted {
		labels := c.GetLabels()
		if !strings.EqualFold(labels["traefik.enable"], "true") && !hasRouterLabel(labels) {
			continue
		}
		port := ""
		for _, k := range dockercli.SortedKeys(labels) {
			if strings.HasPrefix(k, "traefik.http.services.") && strings.HasSuffix(k, ".loadbalancer.server.port") {
				port = strings.TrimSpace(labels[k])
				break
			}
		}
		if port == "" {
			for _, p := range c.GetPorts() {
				port = fmt.Sprint(p.GetContainerPort())
				break
			}
		}
		if port == "" {
			continue
		}
		for _, k := range dockercli.SortedKeys(labels) {
			if !strings.HasPrefix(k, "traefik.http.routers.") || !strings.HasSuffix(k, ".rule") {
				continue
			}
			router := strings.TrimSuffix(strings.TrimPrefix(k, "traefik.http.routers."), ".rule")
			var hosts []string
			for _, m := range traefikHostRe.FindAllStringSubmatch(labels[k], -1) {
				for _, h := range strings.Split(m[1], ",") {
					if h = strings.Trim(strings.TrimSpace(h), "`\"'"); h != "" {
						hosts = append(hosts, h)
					}
				}
			}
			if len(hosts) == 0 {
				continue
			}
			tls := strings.EqualFold(labels["traefik.http.routers."+router+".tls"], "true") ||
				labels["traefik.http.routers."+router+".tls.certresolver"] != ""
			out = append(out, ProxyRoute{
				Kind:            asterismv1.ProxyKind_PROXY_KIND_TRAEFIK,
				ProxyWorkloadID: proxyID,
				Hostnames:       hosts,
				TLS:             tls,
				Upstreams:       []string{c.GetName() + ":" + port},
			})
		}
	}
	return out
}

func hasRouterLabel(labels map[string]string) bool {
	for k := range labels {
		if strings.HasPrefix(k, "traefik.http.routers.") {
			return true
		}
	}
	return false
}
