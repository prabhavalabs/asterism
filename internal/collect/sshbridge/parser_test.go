package sshbridge

import (
	"strings"
	"testing"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestParseNginxSites(t *testing.T) {
	fixture := `
###FILE:/etc/nginx/sites-enabled/example-api
    listen 80;
    server_name api.example.com;
    listen 443 ssl http2;
    server_name api.example.com;
    proxy_pass http://127.0.0.1:8790;
    proxy_pass http://127.0.0.1:8790;
###FILE:/etc/nginx/sites-enabled/example-web
    listen 443 ssl;
    server_name app.example.com www.example.com;
    proxy_pass http://127.0.0.1:3000;
`
	sites := parseNginxSites(fixture)
	if len(sites) != 2 {
		t.Fatalf("expected 2 sites, got %d", len(sites))
	}
	if !sites[0].tls || sites[0].listenPort != 443 || len(sites[0].targetPorts) != 1 || sites[0].targetPorts[0] != 8790 {
		t.Fatalf("unexpected first site: %+v", sites[0])
	}
	if len(sites[1].hostnames) != 2 || sites[1].targetPorts[0] != 3000 {
		t.Fatalf("unexpected second site: %+v", sites[1])
	}
}

func TestParseDockerPorts(t *testing.T) {
	ports := parseDockerPorts("80/tcp, 127.0.0.1:18174->8080/tcp", "ctr:admin", "admin", "proj:demo")
	if len(ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(ports))
	}
	if ports[0].Exposure != asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL || ports[0].ContainerPort != 80 {
		t.Fatalf("unexpected internal port: %+v", ports[0])
	}
	if ports[1].Exposure != asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK || ports[1].HostPort != 18174 {
		t.Fatalf("unexpected loopback port: %+v", ports[1])
	}
}

func TestRedactLog(t *testing.T) {
	got := RedactLog("auth failed Authorization: Bearer eyJhbGciOi.secret token=abc123 dsn=postgres://app:pw1@db/app")
	if want := "auth failed Authorization: Bearer [REDACTED] token=[REDACTED] dsn=postgres://app:[REDACTED]@db/app"; got != want {
		t.Fatalf("RedactLog = %q, want %q", got, want)
	}
}

func TestActionCommand(t *testing.T) {
	id := "3f2a9c1b7d4e"
	tests := []struct {
		action  asterismv1.ContainerAction
		id      string
		timeout time.Duration
		want    string
		wantErr bool
	}{
		{asterismv1.ContainerAction_CONTAINER_ACTION_START, id, 0, "docker start '3f2a9c1b7d4e'", false},
		{asterismv1.ContainerAction_CONTAINER_ACTION_STOP, id, 30 * time.Second, "docker stop -t 30 '3f2a9c1b7d4e'", false},
		{asterismv1.ContainerAction_CONTAINER_ACTION_RESTART, id, 10 * time.Second, "docker restart -t 10 '3f2a9c1b7d4e'", false},
		{asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE, id, 0, "docker pause '3f2a9c1b7d4e'", false},
		{asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE, id, 0, "docker unpause '3f2a9c1b7d4e'", false},
		{asterismv1.ContainerAction_CONTAINER_ACTION_UNSPECIFIED, id, 0, "", true},
		{asterismv1.ContainerAction_CONTAINER_ACTION_STOP, "shop-api; reboot", 0, "", true},
		{asterismv1.ContainerAction_CONTAINER_ACTION_STOP, "$(id)", 0, "", true},
		{asterismv1.ContainerAction_CONTAINER_ACTION_STOP, "3f2a9c1b7d4e' ; rm -rf / '", 0, "", true},
		{asterismv1.ContainerAction_CONTAINER_ACTION_STOP, "shop-api-1", 0, "", true},
	}
	for _, tc := range tests {
		got, err := ActionCommand(tc.action, tc.id, tc.timeout)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Fatalf("ActionCommand(%v, %q) = %q, %v; want %q, err=%v", tc.action, tc.id, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestInventoryScriptInspectsAllContainersOnce(t *testing.T) {
	if strings.Count(inventoryScript, "docker inspect") != 1 || !strings.Contains(inventoryScript, "docker ps -aq --no-trunc") {
		t.Fatal("inventory script must batch one docker inspect over all container ids")
	}
	if strings.Contains(inventoryScript, "{{json .Config.Env}}") {
		t.Fatal("inventory script must not fetch environment values")
	}
}

func TestParseDockerStatus(t *testing.T) {
	tests := []struct {
		status string
		health asterismv1.HealthStatus
		state  string
	}{
		{"Up 3 hours (healthy)", asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, "running"},
		{"Up 3 hours (unhealthy)", asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, "unhealthy"},
		{"Restarting (1) 5 seconds ago", asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, "unhealthy"},
		{"Up 2 minutes (Paused)", asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, "paused"},
		{"Exited (137) 1 hour ago", asterismv1.HealthStatus_HEALTH_STATUS_DOWN, "stopped"},
	}
	for _, tc := range tests {
		h, s := parseDockerStatus(tc.status)
		if h != tc.health || s != tc.state {
			t.Fatalf("parseDockerStatus(%q) = %v %q", tc.status, h, s)
		}
	}
}
