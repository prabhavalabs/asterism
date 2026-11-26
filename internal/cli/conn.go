package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	"github.com/prabhavalabs/asterism/internal/config"
)

// DefaultTokenFile is read when no token is given on the command line or in
// the environment. `asterism mcp` uses the same file.
const DefaultTokenFile = "/etc/asterism/mcp.token"

// tokenHint tells the user how to create a token.
const tokenHint = "create one with `asterism token generate --name cli --scope read`, add the printed [[auth.tokens]] entry to asterism.toml, " +
	"then pass it with --token, --token-file or ASTERISM_TOKEN"

// conn holds the connection flags shared by every view command.
type conn struct {
	server    string
	token     string
	tokenFile string
	config    string
}

func (c *conn) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&c.server, "server", "", "Daemon URL (default $ASTERISM_URL, else http://<server.listen> from the config)")
	f.StringVar(&c.token, "token", "", "API bearer token (default $ASTERISM_TOKEN)")
	f.StringVar(&c.tokenFile, "token-file", "", "File holding an API bearer token (default "+DefaultTokenFile+" when readable)")
	f.StringVar(&c.config, "config", "", "Path to asterism.toml, used to find the daemon address")
}

// client resolves the server URL and token and returns an API client.
func (c *conn) client() (*apiclient.Client, error) {
	url, err := ResolveServerURL(c.server, c.config, "ASTERISM_URL", "ASTERISM_HUB_URL")
	if err != nil {
		return nil, err
	}
	token, err := c.resolveToken()
	if err != nil {
		return nil, err
	}
	return apiclient.New(url, token), nil
}

// resolveToken applies --token, --token-file, ASTERISM_TOKEN, then the
// default token file. No token at all is not an error here: the daemon
// answers with a clear Unauthenticated error, which friendlyError explains.
func (c *conn) resolveToken() (string, error) {
	if t := strings.TrimSpace(c.token); t != "" {
		return t, nil
	}
	if c.tokenFile != "" {
		return ReadTokenFile(c.tokenFile)
	}
	if t := strings.TrimSpace(os.Getenv("ASTERISM_TOKEN")); t != "" {
		return t, nil
	}
	if t, err := ReadTokenFile(DefaultTokenFile); err == nil {
		return t, nil
	}
	return "", nil
}

// ResolveServerURL returns the daemon URL from flag, then the first non-empty
// environment variable in envs, then http://<server.listen> from the config
// (which itself honours ASTERISM_LISTEN), defaulting to 127.0.0.1:7700.
func ResolveServerURL(flag, cfgPath string, envs ...string) (string, error) {
	if u := strings.TrimSpace(flag); u != "" {
		return u, nil
	}
	for _, env := range envs {
		if u := strings.TrimSpace(os.Getenv(env)); u != "" {
			return u, nil
		}
	}
	listen := "127.0.0.1:7700"
	cfg, _, err := config.Load(cfgPath)
	switch {
	case err == nil && cfg.Server.Listen != "":
		listen = cfg.Server.Listen
	case err != nil && cfgPath != "":
		return "", fmt.Errorf("load config: %w", err)
	}
	return URLFromListen(listen)
}

// URLFromListen maps a listen address to a URL a local client can dial:
// wildcard hosts become loopback.
func URLFromListen(listen string) (string, error) {
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

// ReadTokenFile returns the trimmed token stored in path.
func ReadTokenFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	t := strings.TrimSpace(string(raw))
	if t == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return t, nil
}

// friendlyError turns API errors into short, actionable messages.
func friendlyError(err error, server string) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code() {
	case connect.CodeUnavailable:
		return fmt.Errorf("daemon not reachable at %s; start `asterism serve` or set --server / ASTERISM_URL", server)
	case connect.CodeDeadlineExceeded:
		return fmt.Errorf("timed out waiting for the daemon at %s", server)
	case connect.CodeUnauthenticated:
		return fmt.Errorf("the daemon at %s needs a valid API token: %s", server, tokenHint)
	case connect.CodePermissionDenied:
		return fmt.Errorf("the API token lacks the read scope: %s", tokenHint)
	default:
		return errors.New(ce.Message())
	}
}
