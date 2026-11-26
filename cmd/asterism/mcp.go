package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/config"
	mcpserver "github.com/prabhavalabs/asterism/internal/mcp"
)

const defaultMCPTokenFile = "/etc/asterism/mcp.token"

func newMCPCmd() *cobra.Command {
	var hubURL, tokenFile, cfgPath string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP over stdio, backed by a running Asterism hub",
		Long: `Serve the Asterism MCP tools over stdio for MCP clients such as Claude Code.

The command talks to a running hub (asterism serve) over its API with a
read-scoped bearer token, so it works locally or over SSH:

  claude mcp add asterism -- ssh my-vps asterism mcp

Hub URL: --hub, ASTERISM_HUB_URL, or http://<server.listen> from the config.
Token:   --token-file, ASTERISM_TOKEN, or ` + defaultMCPTokenFile + `.
Create a token with: asterism token generate --name mcp --scope read`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			url, err := resolveHubURL(hubURL, cfgPath)
			if err != nil {
				return err
			}
			token, err := resolveMCPToken(tokenFile)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			server := mcpserver.NewServer(mcpserver.NewRemoteBackend(url, token), mcpserver.Options{Version: version})
			if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil && !errors.Is(err, ctx.Err()) {
				return fmt.Errorf("mcp: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&hubURL, "hub", "", "Hub base URL (default http://<server.listen> from the config)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "File holding a read-scoped API token (default "+defaultMCPTokenFile+")")
	cmd.Flags().StringVar(&cfgPath, "config", "", "Path to asterism.toml, used to find the hub address")
	return cmd
}

func resolveHubURL(flag, cfgPath string) (string, error) {
	if u := strings.TrimSpace(flag); u != "" {
		return u, nil
	}
	if u := strings.TrimSpace(os.Getenv("ASTERISM_HUB_URL")); u != "" {
		return u, nil
	}
	listen := "127.0.0.1:7700"
	if cfg, _, err := config.Load(cfgPath); err == nil && cfg.Server.Listen != "" {
		listen = cfg.Server.Listen
	} else if cfgPath != "" && err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	return hubURLFromListen(listen)
}

// hubURLFromListen maps a listen address to a URL a local client can dial:
// wildcard hosts become loopback.
func hubURLFromListen(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("server.listen %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func resolveMCPToken(file string) (string, error) {
	if file == "" {
		if t := strings.TrimSpace(os.Getenv("ASTERISM_TOKEN")); t != "" {
			return t, nil
		}
		file = defaultMCPTokenFile
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("no API token: set ASTERISM_TOKEN or store one in %s (create it with `asterism token generate --name mcp --scope read`): %w", file, err)
	}
	t := strings.TrimSpace(string(raw))
	if t == "" {
		return "", fmt.Errorf("token file %s is empty", file)
	}
	return t, nil
}
