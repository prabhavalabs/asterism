package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Probe checks the host environment the configuration refers to (sockets,
// procfs, proxy configs) and returns warnings; `asterism-agent validate`
// prints them. It never opens a network connection.
func (c *Config) Probe() Issues {
	var out Issues
	warn := func(p, format string, args ...any) {
		out = append(out, Issue{Severity: SeverityWarning, File: c.file, Line: c.lines[p], Path: p, Message: fmt.Sprintf(format, args...)})
	}
	errf := func(p, format string, args ...any) {
		out = append(out, Issue{Severity: SeverityError, File: c.file, Line: c.lines[p], Path: p, Message: fmt.Sprintf(format, args...)})
	}
	root := strings.TrimSuffix(c.Host.Root, "/")
	if st, err := os.Stat(c.HostPath("/proc/stat")); err != nil || st.IsDir() {
		switch {
		case runtime.GOOS != "linux":
			warn("host.root", "%s is not readable: host collectors need Linux procfs (this is %s)", c.HostPath("/proc/stat"), runtime.GOOS)
		case root == "":
			errf("host.root", "/proc/stat is not readable; is procfs mounted?")
		default:
			errf("host.root", "%s is not readable; mount the host's /proc at %s/proc (read-only) or fix host.root", c.HostPath("/proc/stat"), root)
		}
	}
	if !c.Sources.Docker.Enabled.Off() {
		if sock, ok := strings.CutPrefix(c.Sources.Docker.Endpoint, "unix://"); ok {
			switch _, err := os.Stat(sock); {
			case err == nil:
			case errors.Is(err, fs.ErrPermission):
				warn("sources.docker.endpoint", "%s is not accessible: add the agent user to the docker group or use a socket proxy", sock)
			case c.Sources.Docker.Enabled.On():
				errf("sources.docker.endpoint", "%s does not exist but sources.docker.enabled is true", sock)
			}
		}
	}
	files := []struct {
		key     string
		enabled Tri
		path    string
		hint    string
	}{
		{"sources.proxies.nginx.config", c.Sources.Proxies.Nginx.Enabled, c.Sources.Proxies.Nginx.Config, "grant CAP_DAC_READ_SEARCH or make it group-readable"},
		{"sources.proxies.caddy.caddyfile", c.Sources.Proxies.Caddy.Enabled, c.Sources.Proxies.Caddy.Caddyfile, "make it group-readable or rely on admin_api"},
		{"sources.proxies.haproxy.config", c.Sources.Proxies.HAProxy.Enabled, c.Sources.Proxies.HAProxy.Config, "grant CAP_DAC_READ_SEARCH or make it group-readable"},
		{"sources.proxies.cloudflared.config", c.Sources.Proxies.Cloudflared.Enabled, c.Sources.Proxies.Cloudflared.Config, "make it group-readable; the agent never opens credentials-file"},
	}
	for _, f := range files {
		if f.enabled.Off() || f.path == "" {
			continue
		}
		p := c.HostPath(f.path)
		fh, err := os.Open(p)
		switch {
		case err == nil:
			_ = fh.Close()
		case errors.Is(err, fs.ErrPermission):
			warn(f.key, "%s is unreadable: %s", p, f.hint)
		case f.enabled.On():
			warn(f.key, "%s does not exist but the proxy is enabled", p)
		}
	}
	for i, l := range c.Sources.Logs.Files {
		if _, err := os.Stat(c.HostPath(l.Path)); err != nil {
			warn(fmt.Sprintf("sources.logs.files[%d].path", i), "%s is not readable: %v", l.Path, unwrapPathErr(err))
		}
	}
	return out.sorted()
}

// HostPath prefixes an absolute host path with host.root.
func (c *Config) HostPath(p string) string {
	if c.Host.Root == "" || c.Host.Root == "/" {
		return p
	}
	return filepath.Join(c.Host.Root, p)
}
