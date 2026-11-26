package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/actions"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/sched"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/agent/uplink"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// shutdownGrace bounds how long run waits for goroutines after a signal.
const shutdownGrace = 10 * time.Second

func cmdRun(args []string, stderr io.Writer) int {
	cfg, code := loadConfig("run", args, stderr)
	if cfg == nil {
		return code
	}
	logger := newLogger(cfg.Agent, stderr)
	issues := cfg.Validate()
	for _, w := range issues.Warnings() {
		logger.Warn("config warning", "issue", w.String())
	}
	if issues.HasErrors() {
		for _, e := range issues.Errors() {
			fmt.Fprintln(stderr, e.String())
		}
		return 1
	}
	token, err := cfg.ResolveToken()
	if err != nil {
		fmt.Fprintf(stderr, "asterism-agent: %v\n", err)
		return 1
	}
	tuning := applyRuntime(cfg.Agent, os.LookupEnv)
	logger.Info("starting asterism-agent", "version", version, "config", displayPath(cfg), "config_hash", cfg.Hash(),
		"hub", cfg.Hub.URL, "transport", cfg.Hub.Transport, "gomaxprocs", tuning.gomaxprocs,
		"memory_limit", tuning.memoryLimit, "gc_percent", tuning.gcPercent)
	if cfg.Hub.TLS.InsecureDev {
		logger.Warn("hub.tls.insecure_dev is enabled: the token and all data may travel unencrypted")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, token, logger); err != nil {
		logger.Error("agent stopped", "err", err)
		return 1
	}
	logger.Info("asterism-agent stopped")
	return 0
}

// agent is the wired process, separated from cmdRun for tests.
type agent struct {
	cfg    *config.Config
	log    *slog.Logger
	store  *state.Store
	sched  *sched.Scheduler
	up     *uplink.Client
	gate   *actions.Gate
	icons  *iconScanner
	caps   []string
	host   string
	idHash string
}

func newAgent(ctx context.Context, cfg *config.Config, token string, logger *slog.Logger) (*agent, error) {
	hostname, _ := os.Hostname()
	name := cfg.Host.Name
	if name == "" {
		name = strings.ToLower(hostname)
	}
	gate := actions.NewGate(cfg.Actions, actions.DetectSelf("/proc/self/cgroup", hostname))
	store := state.NewStore(state.Options{
		KeyframeEvery:     cfg.Intervals.Keyframe.D(),
		MaxEvents:         cfg.Hub.Buffer.MaxEvents,
		Projects:          cfg.Projects,
		AnnotateContainer: gate.Annotate,
	})
	env := state.Env{Config: cfg, Root: cfg.Host.Root}
	s := sched.New(store, sched.Options{Intervals: cfg.Intervals, Logger: logger.With("component", "sched"), Env: env})
	for _, src := range buildSources(cfg, env) {
		s.Add(src)
	}
	a := &agent{cfg: cfg, log: logger, store: store, sched: s, gate: gate, host: name}
	a.idHash = machineIDHash(cfg, name)
	a.caps = append(s.Init(ctx), gate.Capabilities()...)
	if len(gate.Allowed()) == 0 && cfg.Actions.Enabled {
		logger.Warn("actions.enabled is true but no action is allowed for any container; check actions.allow and actions.containers")
	}
	if !cfg.Sources.ProjectIcons.Enabled.Off() {
		a.icons = newIconScanner(cfg, store, logger.With("component", "icons"))
		a.caps = append(a.caps, "project_icons")
	}
	exec := buildExecutor(cfg)
	svc := actions.NewService(gate, exec, store.FindContainer, logger.With("component", "actions"))
	cpu := newCPUMeter()
	var icons uplink.IconHandler
	if a.icons != nil {
		icons = a.icons
	}
	up, err := uplink.New(uplink.Options{
		Config:   cfg,
		Token:    token,
		Store:    store,
		Hello:    a.hello,
		Commands: svc,
		Logs:     buildLogs(cfg),
		Icons:    icons,
		OnHints:  s.SetHints,
		Stats: func(st *v1.AgentStats) {
			st.RssBytes = residentBytes()
			st.CpuUtil = cpu.util()
			st.CollectorErrors = s.CollectorErrors()
		},
		Logger:  logger.With("component", "uplink"),
		Version: version,
	})
	if err != nil {
		return nil, err
	}
	a.up = up
	return a, nil
}

func (a *agent) hello() *v1.AgentHello {
	host := a.store.HostInfo()
	if host == nil {
		host = &v1.HostInfo{Hostname: a.host, Os: runtime.GOOS, Arch: runtime.GOARCH, Cores: uint32(runtime.NumCPU())}
	}
	return &v1.AgentHello{
		AgentVersion:   version,
		HostName:       a.host,
		MachineIdHash:  a.idHash,
		Host:           host,
		Capabilities:   a.caps,
		ConfigHash:     a.cfg.Hash(),
		Labels:         a.cfg.Host.Labels,
		AllowedActions: a.gate.Allowed(),
	}
}

func run(ctx context.Context, cfg *config.Config, token string, logger *slog.Logger) error {
	a, err := newAgent(ctx, cfg, token, logger)
	if err != nil {
		return err
	}
	return a.run(ctx)
}

func (a *agent) run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Go(func() { _ = a.sched.Run(ctx) })
	wg.Go(func() { _ = a.up.Run(ctx) })
	if a.icons != nil {
		wg.Go(func() { a.icons.run(ctx) })
	}
	<-ctx.Done()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(shutdownGrace):
		return fmt.Errorf("goroutines did not stop within %s", shutdownGrace)
	}
}

// machineIDHash is the hex SHA-256 of /etc/machine-id plus the host name;
// the raw machine id never leaves the host.
func machineIDHash(cfg *config.Config, host string) string {
	b, err := os.ReadFile(cfg.HostPath("/etc/machine-id"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(b)) + "\n" + host))
	return hex.EncodeToString(sum[:])
}

func newLogger(a config.Agent, w io.Writer) *slog.Logger {
	var level slog.Level
	switch a.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if a.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
