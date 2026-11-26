package assemble

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// ParseDockerStatus maps a `docker ps` status to an aggregate health and a
// workload state ("running", "unhealthy", "paused", "stopped").
func ParseDockerStatus(status string) (asterismv1.HealthStatus, string) {
	low := strings.ToLower(status)
	if strings.HasPrefix(low, "exited") || strings.HasPrefix(low, "dead") || strings.HasPrefix(low, "created") {
		return asterismv1.HealthStatus_HEALTH_STATUS_DOWN, "stopped"
	}
	if strings.Contains(low, "(paused)") {
		return asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, "paused"
	}
	if strings.Contains(low, "(unhealthy)") || strings.Contains(low, "restarting") {
		return asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, "unhealthy"
	}
	return asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, "running"
}

var dockerPortMappingRe = regexp.MustCompile(`^([0-9a-fA-F.:*\[\]]+):(\d+)(?:-\d+)?->(\d+)(?:-\d+)?/(tcp|udp)$`)
var dockerInternalPortRe = regexp.MustCompile(`^(\d+)(?:-\d+)?/(tcp|udp)$`)

// ParseDockerPorts parses the `docker ps` ports column into port bindings
// owned by workload wlID.
func ParseDockerPorts(raw, wlID, wlName, projID string) []*asterismv1.PortBinding {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []*asterismv1.PortBinding
	seen := make(map[string]bool)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if m := dockerPortMappingRe.FindStringSubmatch(item); len(m) == 5 {
			ip := strings.Trim(m[1], "[]")
			hp, _ := strconv.Atoi(m[2])
			cp, _ := strconv.Atoi(m[3])
			proto := m[4]
			key := fmt.Sprintf("%s:%s:%d:%d", proto, ip, hp, cp)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, &asterismv1.PortBinding{
				Id:            fmt.Sprintf("port:%s:%d:%s", wlName, hp, proto),
				WorkloadId:    wlID,
				WorkloadName:  wlName,
				ProjectId:     projID,
				HostIp:        ip,
				HostPort:      uint32(hp),
				ContainerPort: uint32(cp),
				Proto:         proto,
				Exposure:      dockercli.ClassifyExposure(ip),
				ProcessName:   wlName,
			})
		} else if m := dockerInternalPortRe.FindStringSubmatch(item); len(m) == 3 {
			cp, _ := strconv.Atoi(m[1])
			proto := m[2]
			key := fmt.Sprintf("%s:int:%d", proto, cp)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, &asterismv1.PortBinding{
				Id:            fmt.Sprintf("port:%s:int:%d:%s", wlName, cp, proto),
				WorkloadId:    wlID,
				WorkloadName:  wlName,
				ProjectId:     "",
				HostPort:      0,
				ContainerPort: uint32(cp),
				Proto:         proto,
				Exposure:      asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL,
				ProcessName:   wlName,
			})
		}
	}
	return out
}

// ClassifyProjectCategory guesses a project's category from its name.
func ClassifyProjectCategory(name string) string {
	low := strings.ToLower(name)
	switch {
	case low == "platform" || low == "infra" || low == "edge-ingress" || strings.Contains(low, "proxy"):
		return "infra"
	case strings.Contains(low, "staging") || strings.Contains(low, "dev") || strings.Contains(low, "sandbox"):
		return "staging"
	default:
		return "production"
	}
}

// ClassifyWorkloadRole guesses a workload's role from its name, compose
// service, image, and `docker ps` ports column.
func ClassifyWorkloadRole(name, service, image, portsRaw string) asterismv1.WorkloadRole {
	all := strings.ToLower(name + " " + service + " " + image)
	switch {
	case strings.Contains(all, "postgres") || strings.Contains(all, "mysql") || strings.Contains(all, "mariadb") || strings.Contains(all, "mongo") || strings.Contains(all, "timescale"):
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE
	case strings.Contains(all, "redis") || strings.Contains(all, "valkey") || strings.Contains(all, "memcached"):
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE
	case strings.Contains(all, "nats") || strings.Contains(all, "rabbitmq") || strings.Contains(all, "kafka"):
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_QUEUE
	case strings.Contains(all, "garage") || strings.Contains(all, "minio") || strings.Contains(all, "backup"):
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_STORAGE
	case strings.Contains(all, "nginx") || strings.Contains(all, "caddy") || strings.Contains(all, "traefik") || strings.Contains(all, "cloudflared") || strings.Contains(service, "gateway") || strings.Contains(portsRaw, "2019/tcp"):
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY
	case strings.Contains(all, "worker") || strings.Contains(all, "indexer") || strings.Contains(all, "scanner") || strings.Contains(all, "cron"):
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER
	default:
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_APP
	}
}

// RoleFromLabel parses an asterism.role label value.
func RoleFromLabel(v string) asterismv1.WorkloadRole {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "proxy":
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY
	case "app":
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_APP
	case "worker":
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER
	case "database", "db":
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE
	case "cache":
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE
	case "queue":
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_QUEUE
	case "storage":
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_STORAGE
	default:
		return asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED
	}
}

// NginxUnitID is the workload id of a host nginx systemd unit.
const NginxUnitID = "unit:nginx.service"

// NginxUnit returns the host nginx unit (listening on 80 and 443) placed in
// the "edge-ingress" infra project, as the SSH bridge reports it when
// `systemctl is-active nginx` succeeds.
func NginxUnit() HostWorkload {
	wl := &asterismv1.Workload{
		Id:          NginxUnitID,
		Kind:        asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT,
		Role:        asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY,
		Name:        "nginx",
		ServiceName: "nginx",
		Image:       "host systemd reverse proxy",
		State:       "active",
		Health:      asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY,
		Replicas:    1,
		Networks:    []string{"host"},
	}
	for _, portNum := range []uint32{80, 443} {
		wl.Ports = append(wl.Ports, &asterismv1.PortBinding{
			Id:            fmt.Sprintf("port:nginx:%d", portNum),
			WorkloadId:    wl.Id,
			WorkloadName:  "nginx",
			HostIp:        "0.0.0.0",
			HostPort:      portNum,
			ContainerPort: portNum,
			Proto:         "tcp",
			Exposure:      asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC,
			ProcessName:   "nginx",
		})
	}
	return HostWorkload{
		Workload:        wl,
		Project:         "edge-ingress",
		ProjectKind:     "systemd",
		ProjectCategory: "infra",
		WorkingDir:      "/etc/nginx/sites-enabled",
	}
}
