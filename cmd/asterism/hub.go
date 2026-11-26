package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prabhavalabs/asterism/internal/api"
	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/agentingest"
	"github.com/prabhavalabs/asterism/internal/collect/favicon"
	hostcollect "github.com/prabhavalabs/asterism/internal/collect/host"
	"github.com/prabhavalabs/asterism/internal/collect/showcase"
	"github.com/prabhavalabs/asterism/internal/collect/sshbridge"
	"github.com/prabhavalabs/asterism/internal/config"
	mcpserver "github.com/prabhavalabs/asterism/internal/mcp"
	"github.com/prabhavalabs/asterism/internal/service"
	"github.com/prabhavalabs/asterism/internal/store/iconstore"
	"github.com/prabhavalabs/asterism/internal/store/ring"
	"github.com/prabhavalabs/asterism/internal/web"
)

// hub is the wired daemon: collectors, service, and HTTP routes.
type hub struct {
	cfg     config.Config
	logger  *slog.Logger
	svc     *service.Observatory
	agents  *agentingest.Registry
	handler http.Handler
	runners []func(ctx context.Context) error
}

// newAuthManager builds the auth manager from config: the admin session
// holds the actions scope only when actions are enabled; API tokens get
// exactly their configured scopes; agent tokens get only "ingest".
func newAuthManager(cfg config.Config) (*auth.Manager, error) {
	sessionScopes := []auth.Scope{auth.ScopeRead}
	if cfg.Actions.AllowActions {
		sessionScopes = append(sessionScopes, auth.ScopeActions)
	}
	tokens := make([]auth.APIToken, 0, len(cfg.Auth.Tokens))
	for _, t := range cfg.Auth.Tokens {
		scopes := make([]auth.Scope, 0, len(t.Scopes))
		for _, raw := range t.Scopes {
			sc, scopeErr := auth.ParseScope(raw)
			if scopeErr != nil {
				return nil, fmt.Errorf("auth token %q: %w", t.Name, scopeErr)
			}
			scopes = append(scopes, sc)
		}
		tokens = append(tokens, auth.APIToken{Name: t.Name, SHA256: t.SHA256, Scopes: scopes})
	}
	agents := make([]auth.AgentToken, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		agents = append(agents, auth.AgentToken{Name: a.Name, SHA256: a.SHA256})
	}
	mgr, err := auth.NewManager(auth.Options{
		AdminEmail:    cfg.Auth.AdminEmail,
		AdminPassword: cfg.Auth.AdminPassword,
		PasswordHash:  cfg.Auth.PasswordHash,
		SessionTTL:    cfg.Auth.SessionTTL(),
		BehindTLS:     cfg.Server.BehindTLS,
		SessionScopes: sessionScopes,
		Tokens:        tokens,
		Agents:        agents,
	})
	if err != nil {
		return nil, fmt.Errorf("init auth: %w (configure [auth] in asterism.toml or ASTERISM_ADMIN_EMAIL/ASTERISM_ADMIN_PASSWORD)", err)
	}
	return mgr, nil
}

// newHub wires collectors, the service and the HTTP mux for cfg. Nothing
// runs until run is called.
func newHub(cfg config.Config, logger *slog.Logger, authMgr *auth.Manager) (*hub, error) {
	h := &hub{cfg: cfg, logger: logger}
	msgBus := bus.New(256)
	ringStore := ring.New(900)

	var hostProv service.HostProvider
	var invProv service.InventoryProvider
	var dockerProv service.DockerProvider
	var actuator service.ContainerActuator
	var dirIcons service.DirIconSource

	switch cfg.Collect.Mode {
	case "ssh":
		if cfg.Collect.SSHTarget == "" {
			return nil, fmt.Errorf("collect.ssh_target must be set when collect.mode = \"ssh\"")
		}
		bridge := sshbridge.New(logger, cfg.Collect.SSHTarget, msgBus, ringStore)
		hostProv, invProv, dockerProv, actuator = bridge, bridge, bridge, bridge
		h.runners = append(h.runners, named("ssh bridge", bridge.Run))
	case "showcase":
		hc := hostcollect.New(logger, msgBus, cfg.Collect.Mode, "")
		sc := showcase.NewWithOptions(msgBus, ringStore, showcase.Options{Fleet: cfg.Collect.ShowcaseFleet})
		hostProv, invProv, dockerProv, actuator = showcase.WithDemoIdentity(hc), sc, sc, sc
		dirIcons = sc
		h.runners = append(h.runners, named("host collector", hc.Run), named("showcase provider", sc.Run))
	case "agent":
		agents := make([]agentingest.Agent, 0, len(cfg.Agents))
		for _, a := range cfg.Agents {
			agents = append(agents, agentingest.Agent{Name: a.Name, MachineID: a.MachineID, AllowActions: a.AllowActions})
		}
		if cfg.Collect.Agent.PrimaryHost == "" && len(cfg.Agents) > 1 {
			logger.Warn("several agents are enrolled but collect.agent.primary_host is empty; showing the first one", "primary", cfg.Agents[0].Name)
		}
		reg, err := agentingest.New(agentingest.Options{
			Agents:      agents,
			PrimaryHost: cfg.PrimaryAgent(),
			StaleAfter:  cfg.Collect.Agent.StaleAfter,
			Bus:         msgBus,
			Logger:      logger.With("component", "agent-ingest"),
			HubVersion:  version,
		})
		if err != nil {
			return nil, err
		}
		h.agents = reg
		pv := reg.Primary()
		hostProv, invProv, dockerProv, actuator = pv, pv, pv, pv
		dirIcons = pv
		h.runners = append(h.runners, named("agent registry", reg.Run))
	default:
		hc := hostcollect.New(logger, msgBus, cfg.Collect.Mode, "")
		hostProv = hc
		dirIcons = favicon.NewLocalDirs()
		h.runners = append(h.runners, named("host collector", hc.Run))
	}

	svc := service.New(msgBus, ringStore, hostProv, invProv, cfg.Collect.Mode)
	svc.SetLogger(logger)
	display := make(map[string]service.ProjectDisplay, len(cfg.Projects))
	for name, pc := range cfg.Projects {
		display[name] = service.ProjectDisplay{Icon: pc.Icon, Color: pc.Color}
	}
	svc.ConfigureProjects(display)
	if icons, ok := iconOptions(cfg, logger, dirIcons); ok {
		svc.ConfigureIcons(icons)
		h.runners = append(h.runners, named("icon discovery", svc.RunIconDiscovery))
	}
	if dockerProv != nil {
		svc.ConfigureDocker(dockerProv, actuator, service.ActionPolicy{
			Enabled:        cfg.Actions.AllowActions,
			ProtectedNames: cfg.Actions.ProtectedContainers,
			AllowProtected: cfg.Actions.AllowProtected,
			DefaultTimeout: cfg.Actions.StopTimeout(),
		})
		h.runners = append(h.runners, named("docker watcher", func(ctx context.Context) error { return svc.RunDockerWatcher(ctx, 5*time.Second) }))
	} else {
		svc.ConfigureDocker(nil, nil, service.ActionPolicy{Enabled: cfg.Actions.AllowActions})
	}
	if h.agents != nil {
		svc.ConfigureAgents(h.agents)
		svc.OnLiveViewers(h.agents.SetLiveViewers)
	}
	h.runners = append(h.runners, named("ingestor", svc.RunIngestor))
	h.svc = svc

	mux := http.NewServeMux()
	authMgr.RegisterRoutes(mux)
	apiHandler := api.New(svc)
	apiHandler.Register(mux, authMgr.RequireSession)
	// MCP over stateless Streamable HTTP; bearer tokens with the read scope only.
	mux.Handle("/mcp", authMgr.RequireSession(mcpserver.HTTPHandler(apiHandler, version)))
	if h.agents != nil {
		api.NewAgentHandler(h.agents).Register(mux, authMgr.RequireIngest)
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", web.Handler(cfg.Server.PublicURL))
	h.handler = mux
	return h, nil
}

// iconOptions opens the icon store under data_dir. A data directory that
// cannot be created disables custom icons with a warning instead of
// stopping the hub.
func iconOptions(cfg config.Config, logger *slog.Logger, dirs service.DirIconSource) (service.IconOptions, bool) {
	dataDir, err := cfg.EnsureDataDir()
	if err != nil {
		logger.Warn("project icons disabled: data directory unavailable", "err", err)
		return service.IconOptions{}, false
	}
	st, err := iconstore.Open(dataDir)
	if err != nil {
		logger.Warn("project icons disabled: icon store unavailable", "err", err)
		return service.IconOptions{}, false
	}
	opts := service.IconOptions{Store: st, Dirs: dirs, FetchAllowed: cfg.FaviconFetchFor}
	// Showcase hostnames are fictional: never touch the network there.
	if cfg.FaviconFetch && cfg.Collect.Mode != "showcase" {
		opts.Fetcher = favicon.NewFetcher()
	}
	logger.Info("project icons enabled", "data_dir", dataDir, "favicon_fetch", opts.Fetcher != nil)
	return opts, true
}

// run starts every background loop; they stop when ctx ends.
func (h *hub) run(ctx context.Context) {
	for _, r := range h.runners {
		go func() {
			if err := r(ctx); err != nil && !errors.Is(err, context.Canceled) {
				h.logger.Error("background loop stopped", "err", err)
			}
		}()
	}
}

func named(name string, fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
}
