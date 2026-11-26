// Command asterism-agent is the outbound-only VPS sidecar that streams host,
// container and service facts to an Asterism hub.
//
//	asterism-agent run [--config /etc/asterism/agent.yaml]   (default)
//	asterism-agent validate [--config ...]
//	asterism-agent print-config [--config ...]
//	asterism-agent discover [--config ...]
//	asterism-agent version
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/prabhavalabs/asterism/internal/agent/config"
)

// Set with -ldflags "-X main.version=v0.2.0 -X main.commit=abc123".
var (
	version = "dev"
	commit  = ""
)

const usage = `asterism-agent streams this host's containers, services, ports and metrics
to an Asterism hub over an outbound connection.

Usage:
  asterism-agent [command] [--config path]

Commands:
  run            connect to the hub and stream (default)
  validate       check the configuration and probe sockets and paths
  print-config   print the effective configuration (token redacted)
  discover       one-shot dry run of the detected sources
  version        print the version

The configuration path defaults to $ASTERISM_AGENT_CONFIG, then
/etc/asterism/agent.yaml.
`

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "run":
		return cmdRun(args, stderr)
	case "validate":
		return cmdValidate(args, stdout, stderr)
	case "print-config":
		return cmdPrintConfig(args, stdout, stderr)
	case "discover":
		return cmdDiscover(args, stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, versionString())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "asterism-agent: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func versionString() string {
	rev := commit
	if rev == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 12 {
					rev = s.Value[:12]
				}
			}
		}
	}
	if rev != "" {
		rev = " " + rev
	}
	return fmt.Sprintf("asterism-agent %s%s (%s/%s, %s)", version, rev, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// configFlags parses the common --config flag.
func configFlags(name string, args []string, stderr io.Writer) (path string, explicit bool, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	p := fs.String("config", "", "path to agent.yaml (default $ASTERISM_AGENT_CONFIG or "+config.DefaultPath+")")
	if err := fs.Parse(args); err != nil {
		return "", false, err
	}
	if fs.NArg() > 0 {
		return "", false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch {
	case *p != "":
		return *p, true, nil
	case os.Getenv(config.EnvConfig) != "":
		return os.Getenv(config.EnvConfig), true, nil
	default:
		return config.DefaultPath, false, nil
	}
}

// loadConfig loads the configuration; a missing default file is allowed so
// the agent can run from environment variables alone.
func loadConfig(name string, args []string, stderr io.Writer) (*config.Config, int) {
	path, explicit, err := configFlags(name, args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, 0
		}
		fmt.Fprintf(stderr, "asterism-agent %s: %v\n", name, err)
		return nil, 2
	}
	cfg, err := config.LoadFile(path, !explicit, os.LookupEnv)
	if err != nil {
		var is config.Issues
		if errors.As(err, &is) {
			for _, i := range is {
				fmt.Fprintln(stderr, i.String())
			}
		} else {
			fmt.Fprintf(stderr, "asterism-agent: %v\n", err)
		}
		return nil, 1
	}
	return cfg, -1
}

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadConfig("validate", args, stderr)
	if cfg == nil {
		return code
	}
	issues := append(cfg.Validate(), cfg.Probe()...)
	for _, i := range issues {
		fmt.Fprintln(stderr, i.String())
	}
	if issues.HasErrors() {
		fmt.Fprintf(stderr, "asterism-agent: %d error(s) in %s\n", len(issues.Errors()), displayPath(cfg))
		return 1
	}
	fmt.Fprintf(stdout, "ok: %s (%s, %d warning(s))\n", displayPath(cfg), cfg.Hash(), len(issues.Warnings()))
	return 0
}

func cmdPrintConfig(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadConfig("print-config", args, stderr)
	if cfg == nil {
		return code
	}
	out, err := cfg.PrintConfig()
	if err != nil {
		fmt.Fprintf(stderr, "asterism-agent: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(out)
	return 0
}

func displayPath(cfg *config.Config) string {
	if cfg.Path == "" {
		return "defaults and environment"
	}
	return cfg.Path
}
