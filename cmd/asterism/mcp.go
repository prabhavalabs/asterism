package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/cli"
	mcpserver "github.com/prabhavalabs/asterism/internal/mcp"
)

const defaultMCPTokenFile = cli.DefaultTokenFile

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
			url, err := cli.ResolveServerURL(hubURL, cfgPath, "ASTERISM_HUB_URL")
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

func resolveMCPToken(file string) (string, error) {
	if file == "" {
		if t := strings.TrimSpace(os.Getenv("ASTERISM_TOKEN")); t != "" {
			return t, nil
		}
		file = defaultMCPTokenFile
	}
	t, err := cli.ReadTokenFile(file)
	if err != nil {
		return "", fmt.Errorf("no API token: set ASTERISM_TOKEN or store one in %s (create it with `asterism token generate --name mcp --scope read`): %w", file, err)
	}
	return t, nil
}
