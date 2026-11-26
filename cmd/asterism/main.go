package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/config"
)

const version = "0.1.0-dev"

func main() {
	rootCmd := &cobra.Command{
		Use:   "asterism",
		Short: "Self-hosted VPS observatory and visual infrastructure portal",
	}

	rootCmd.AddCommand(newServeCmd(), newHashPasswordCmd(), newTokenCmd(), newAgentCmd(), newMCPCmd(), newVersionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func newServeCmd() *cobra.Command {
	var cfgPath string
	var listenOverride string
	var modeOverride string
	var sshOverride string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Asterism daemon, ConnectRPC API, and embedded web UI",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

			cfg, loadedPath, err := config.Load(cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if listenOverride != "" {
				cfg.Server.Listen = listenOverride
			}
			if modeOverride != "" {
				cfg.Collect.Mode = modeOverride
			}
			if sshOverride != "" {
				cfg.Collect.Mode = "ssh"
				cfg.Collect.SSHTarget = sshOverride
			}

			authMgr, err := newAuthManager(cfg)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			h, err := newHub(cfg, logger, authMgr)
			if err != nil {
				return err
			}
			h.run(ctx)
			mux := h.handler

			srv := &http.Server{
				Addr:              cfg.Server.Listen,
				Handler:           h2c.NewHandler(mux, &http2.Server{}),
				ReadHeaderTimeout: 10 * time.Second,
			}

			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()

			attrs := []any{
				"addr", cfg.Server.Listen,
				"mode", cfg.Collect.Mode,
				"config", loadedPath,
				"allow_actions", cfg.Actions.AllowActions,
			}
			switch cfg.Collect.Mode {
			case "ssh":
				attrs = append(attrs, "ssh_target", cfg.Collect.SSHTarget)
			case "agent":
				attrs = append(attrs, "agents", len(cfg.Agents), "primary_host", cfg.PrimaryAgent())
			}
			logger.Info("asterism listening", attrs...)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("http server: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to TOML configuration file")
	cmd.Flags().StringVar(&listenOverride, "listen", "", "Override listen address (default 127.0.0.1:7700)")
	cmd.Flags().StringVar(&modeOverride, "mode", "", "Collector mode: local, showcase, ssh, or agent")
	cmd.Flags().StringVar(&sshOverride, "ssh", "", "Remote SSH target for read-only bridge mode (e.g. user@vps)")
	return cmd
}

func newHashPasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hash-password <password>",
		Short: "Generate an Argon2id password hash for asterism.toml",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			hash, err := auth.HashPassword(args[0])
			if err != nil {
				return err
			}
			fmt.Println(hash)
			return nil
		},
	}
}

func newTokenCmd() *cobra.Command {
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API bearer tokens for CLI, MCP, and API clients",
	}
	var name string
	var scopes []string
	gen := &cobra.Command{
		Use:   "generate",
		Short: "Generate a bearer token and print the [[auth.tokens]] config entry",
		RunE: func(_ *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			quoted := make([]string, 0, len(scopes))
			for _, raw := range scopes {
				sc, err := auth.ParseScope(raw)
				if err != nil {
					return err
				}
				quoted = append(quoted, fmt.Sprintf("%q", string(sc)))
			}
			token, digest, err := auth.GenerateToken()
			if err != nil {
				return err
			}
			fmt.Printf("Token (shown once, store it securely):\n  %s\n\n", token)
			fmt.Printf("Add to asterism.toml:\n\n[[auth.tokens]]\nname = %q\nsha256 = %q\nscopes = [%s]\n", name, digest, strings.Join(quoted, ", "))
			return nil
		},
	}
	gen.Flags().StringVar(&name, "name", "", "Token name shown in audit records")
	gen.Flags().StringSliceVar(&scopes, "scope", []string{"read"}, "Scopes: read, actions (repeatable)")
	tokenCmd.AddCommand(gen)
	return tokenCmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print Asterism version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("asterism %s\n", version)
		},
	}
}
