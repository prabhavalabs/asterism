package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/actions"
	"github.com/prabhavalabs/asterism/internal/agent/collect/cgroup"
	"github.com/prabhavalabs/asterism/internal/agent/collect/docker"
	"github.com/prabhavalabs/asterism/internal/agent/collect/host"
	"github.com/prabhavalabs/asterism/internal/agent/collect/listeners"
	"github.com/prabhavalabs/asterism/internal/agent/collect/pm2"
	"github.com/prabhavalabs/asterism/internal/agent/collect/process"
	"github.com/prabhavalabs/asterism/internal/agent/collect/proxy"
	"github.com/prabhavalabs/asterism/internal/agent/collect/redact"
	"github.com/prabhavalabs/asterism/internal/agent/collect/self"
	"github.com/prabhavalabs/asterism/internal/agent/collect/systemd"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/agent/uplink"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// selfInterval is the cadence of the agent's own footprint series.
const selfInterval = 60 * time.Second

// wiring is the per-configuration shared state of the collectors: the
// target registry (Docker -> cgroup/listeners, systemd -> cgroup,
// processes/pm2 -> listeners), the redactor and the Docker client, which
// buildExecutor reuses for actions.
type wiring struct {
	reg    *targets.Registry
	red    *redact.Redactor
	redErr error
	docker *dockerapi.Client
	podman *dockerapi.Client
	dsrc   *docker.Source
	psrc   *docker.Source
}

var (
	wiringMu sync.Mutex
	wirings  = map[*config.Config]*wiring{}
)

func wire(cfg *config.Config) *wiring {
	wiringMu.Lock()
	defer wiringMu.Unlock()
	if w, ok := wirings[cfg]; ok {
		return w
	}
	w := &wiring{reg: targets.New()}
	w.red, w.redErr = redact.New(cfg.Redaction)
	if !cfg.Sources.Docker.Enabled.Off() {
		if c, err := dockerapi.New(cfg.Sources.Docker.Endpoint, dockerapi.Options{}); err == nil {
			c.PostProcess = w.red.Container
			w.docker = c
		}
	}
	if cfg.Sources.Podman.Enabled.On() {
		if c, err := dockerapi.New(cfg.Sources.Podman.Endpoint, dockerapi.Options{}); err == nil {
			c.PostProcess = w.red.Container
			w.podman = c
		}
	}
	wirings[cfg] = w
	return w
}

// buildSources returns the collectors for this configuration, in tick
// order, each honouring its sources.* switch. Sources whose Init fails
// (no Docker socket, no systemd, no proxy config, ...) are disabled by the
// scheduler and logged; the agent keeps running with the rest.
func buildSources(cfg *config.Config, env state.Env) []state.Source {
	w := wire(cfg)
	if w.redErr != nil {
		logger := env.Logger
		if logger == nil {
			logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
		}
		logger.Warn("invalid redaction rule skipped", "err", w.redErr)
	}
	iv := cfg.Intervals
	src := cfg.Sources
	out := []state.Source{host.New(cfg, iv.Host.D())}
	if w.docker != nil {
		w.dsrc = docker.New(docker.Options{Name: "docker", Docker: src.Docker, Interval: iv, Logs: src.Logs, Client: w.docker, Registry: w.reg, Redactor: w.red})
		out = append(out, w.dsrc)
	}
	if w.podman != nil {
		pd := src.Docker
		pd.Endpoint = src.Podman.Endpoint
		w.psrc = docker.New(docker.Options{Name: "podman", Docker: pd, Interval: iv, Logs: src.Logs, Client: w.podman, Registry: w.reg, Redactor: w.red})
		out = append(out, w.psrc)
	}
	if src.Processes.Enabled && len(src.Processes.Match) > 0 {
		out = append(out, process.New(src.Processes.Match, iv.Processes.D(), w.reg, w.red))
	}
	if !src.PM2.Enabled.Off() {
		out = append(out, pm2.New(src.PM2, iv.Processes.D(), w.reg))
	}
	if src.Ports.Enabled {
		out = append(out, listeners.New(src.Ports, iv.Listeners.D(), w.reg))
	}
	if !src.Systemd.Enabled.Off() {
		out = append(out, systemd.New(src.Systemd, iv.Units.D(), w.reg))
	}
	if src.Docker.Stats != "api" {
		out = append(out, cgroup.New(w.reg, iv.Containers.D()))
	}
	out = append(out, proxy.New(src.Proxies, src.Routes, iv.Proxies.D()))
	out = append(out, self.New(selfInterval))
	return out
}

// buildExecutor returns the Docker action executor (the agent's Docker
// client, whose only mutating endpoints are the five lifecycle actions),
// or nil when the Docker source is disabled.
func buildExecutor(cfg *config.Config) actions.Executor {
	w := wire(cfg)
	if w.docker == nil {
		return nil
	}
	return w.docker
}

// buildLogs returns the on-demand log handler: the Docker source, then the
// Podman source, for container workloads. It is nil (the uplink answers
// "logs are not available") when neither engine source is configured.
// Call it after buildSources.
func buildLogs(cfg *config.Config) uplink.LogHandler {
	w := wire(cfg)
	var r logRouter
	for _, s := range []*docker.Source{w.dsrc, w.psrc} {
		if s != nil {
			r = append(r, s)
		}
	}
	if len(r) == 0 {
		return nil
	}
	return r
}

// logRouter tries each engine source in turn; a source that does not know
// the container answers with notFoundMsg and no lines, and the next one is
// asked.
type logRouter []uplink.LogHandler

// notFoundMsg is docker.Source's answer for a container it does not track.
const notFoundMsg = "container not found on this agent"

func (r logRouter) Logs(ctx context.Context, req *v1.LogRequest, send func(*v1.LogChunk) bool) {
	for i, h := range r {
		last := i == len(r)-1
		if last {
			h.Logs(ctx, req, send)
			return
		}
		missing, first := false, true
		h.Logs(ctx, req, func(ch *v1.LogChunk) bool {
			if first {
				first = false
				if ch.GetEof() && len(ch.GetLines()) == 0 && ch.GetError() == notFoundMsg {
					missing = true
					return false
				}
			}
			return send(ch)
		})
		if !missing {
			return
		}
	}
}
