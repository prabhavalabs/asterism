// Package cli implements the human-facing views of `asterism`: status, top,
// ps, ports, ingress, logs, topo and events. Every command is a thin adapter
// over the daemon's ConnectRPC API (see internal/apiclient); rendering is
// kept in pure functions so it can be golden-tested.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Commands returns the view commands for the root command.
func Commands() []*cobra.Command {
	return []*cobra.Command{
		newStatusCmd(), newTopCmd(), newPsCmd(), newPortsCmd(), newIngressCmd(),
		newLogsCmd(), newTopoCmd(), newEventsCmd(),
	}
}

// output carries the shared --json / --no-color flags and the resolved
// writer for one invocation.
type output struct {
	json    bool
	noColor bool

	w     io.Writer
	th    theme
	tty   bool // stdout is a terminal (the TUI needs one; colour is decided separately)
	loc   *time.Location
	nowFn func() time.Time
}

func (o *output) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&o.json, "json", false, "Print stable JSON (protojson of the API response, proto field names)")
	cmd.Flags().BoolVar(&o.noColor, "no-color", false, "Disable colours (also honoured: NO_COLOR, non-terminal output)")
}

// setup picks colour and the writer from the command's stdout. Colour is on
// only for a terminal without NO_COLOR or TERM=dumb; escape sequences are
// downsampled to what the terminal supports.
func (o *output) setup(cmd *cobra.Command) {
	stdout := cmd.OutOrStdout()
	o.loc = time.Local
	o.nowFn = time.Now
	profile := colorprofile.NoTTY
	if f, ok := stdout.(*os.File); ok {
		o.tty = term.IsTerminal(f.Fd())
		profile = colorprofile.Detect(f, os.Environ())
	}
	if o.noColor || o.json {
		profile = colorprofile.NoTTY
	}
	o.th = theme{color: profile >= colorprofile.ANSI}
	if o.th.color {
		o.w = &colorprofile.Writer{Forward: stdout, Profile: profile}
	} else {
		o.w = stdout
	}
}

func (o *output) now() time.Time { return o.nowFn().In(o.loc) }

// writeProto prints m as indented protojson with proto field names. The
// output is re-indented with encoding/json so whitespace is deterministic.
func writeProto(w io.Writer, m proto.Message) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	buf.WriteByte('\n')
	_, err = w.Write(buf.Bytes())
	return err
}

// writeProtoLine prints m as one compact protojson line (NDJSON).
func writeProtoLine(w io.Writer, m proto.Message) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	buf.WriteByte('\n')
	_, err = w.Write(buf.Bytes())
	return err
}
