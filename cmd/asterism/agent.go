package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/auth"
)

func newAgentCmd() *cobra.Command {
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Enroll asterism-agent hosts (collect.mode = \"agent\")",
	}
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Manage agent ingest tokens",
	}
	var host string
	var allowActions bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an agent token and print the [[agents]] config entry",
		Long: "Create a token bound to one host. The token is printed once; only its SHA-256 digest goes into\n" +
			"asterism.toml. Agent tokens carry the \"ingest\" scope: they can push telemetry for that host and\n" +
			"read nothing.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printAgentToken(cmd.OutOrStdout(), host, allowActions)
		},
	}
	create.Flags().StringVar(&host, "host", "", "Host name the token is bound to (must match host.name in the agent's agent.yaml)")
	create.Flags().BoolVar(&allowActions, "allow-actions", false, "Set the hub-side per-agent action gate (actions still need [actions] allow_actions and the agent's own allow-list)")
	tokenCmd.AddCommand(create)
	agentCmd.AddCommand(tokenCmd)
	return agentCmd
}

func printAgentToken(w io.Writer, host string, allowActions bool) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("--host is required")
	}
	if !auth.ValidHostName(host) {
		return fmt.Errorf("--host %q: use 1-63 letters, digits, '.', '_' or '-' (it must match host.name in agent.yaml)", host)
	}
	token, digest, err := auth.GenerateAgentToken()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Agent token for %s (shown once; store it on the agent host as /etc/asterism/agent.token, mode 0640):\n  %s\n\n", host, token)
	fmt.Fprintf(w, "Add to asterism.toml and restart Asterism:\n\n")
	fmt.Fprintf(w, "[collect]\nmode = \"agent\"\n\n")
	fmt.Fprintf(w, "[[agents]]\nname = %q\nsha256 = %q\nmachine_id = \"\"         # optional pin; the hub logs the observed value on first connect\nallow_actions = %t   # hub-side per-agent gate\n", host, digest, allowActions)
	fmt.Fprintf(w, "\nOn the agent host, set host.name: %s and hub.token_file in /etc/asterism/agent.yaml.\n", host)
	return nil
}
