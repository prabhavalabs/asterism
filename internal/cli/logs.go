package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// maxLogPage is the server's per-call line limit.
const maxLogPage = 500

// followInterval is how often `logs -f` asks for new lines.
var followInterval = time.Second

func newLogsCmd() *cobra.Command {
	var v view
	var follow bool
	var grep string
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <workload>",
		Short: "Print a workload's recent log lines (secrets are redacted by the daemon)",
		Long: `Print a workload's recent log lines, oldest first.

<workload> is an id such as ctr:shop-api-1 or unit:nginx.service, or a
name that matches one workload (see asterism ps).

With -f the command keeps polling for new lines once a second until
Ctrl-C. With --json the output is a TailLogsResponse, or one LogLine
object per line (NDJSON) when following.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tail < 0 {
				return fmt.Errorf("--tail must not be negative")
			}
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				id, err := resolveWorkload(ctx, c, args[0])
				if err != nil {
					return err
				}
				lines, err := tailLogs(ctx, c, id, grep, tail)
				if err != nil {
					return err
				}
				p := logPrinter{w: v.w, th: v.th, loc: v.loc, json: v.json}
				if !follow {
					if v.json {
						return writeProto(v.w, &asterismv1.TailLogsResponse{Lines: lines})
					}
					p.print(lines)
					return nil
				}
				p.print(lines)
				return p.follow(ctx, c, id, grep, lines, cmd.ErrOrStderr())
			})
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Keep printing new lines until Ctrl-C")
	cmd.Flags().StringVarP(&grep, "grep", "g", "", "Only lines containing this text (case-insensitive)")
	cmd.Flags().IntVarP(&tail, "tail", "n", 100, "Number of recent lines to print first")
	v.bind(cmd)
	return cmd
}

// resolveWorkload maps a bare name to a workload id; ids pass through.
func resolveWorkload(ctx context.Context, c *apiclient.Client, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if strings.Contains(arg, ":") {
		// TailLogs answers an unknown id with no lines, so check it exists.
		_, err := call(ctx, c.GetWorkload, &asterismv1.GetWorkloadRequest{Id: arg})
		if connect.CodeOf(err) == connect.CodeNotFound {
			_, err = call(ctx, c.GetContainer, &asterismv1.GetContainerRequest{Id: arg})
		}
		if err != nil {
			return "", notFoundHint(err, arg)
		}
		return arg, nil
	}
	resp, err := call(ctx, c.ListWorkloads, &asterismv1.ListWorkloadsRequest{Query: arg, Page: &asterismv1.PageRequest{PageSize: 50}})
	if err != nil {
		return "", err
	}
	ws := resp.GetWorkloads()
	for _, w := range ws {
		if w.GetName() == arg || short(w.GetId()) == arg {
			return w.GetId(), nil
		}
	}
	switch len(ws) {
	case 0:
		return "", fmt.Errorf("no workload matches %q; list them with `asterism ps`", arg)
	case 1:
		return ws[0].GetId(), nil
	}
	ids := make([]string, 0, 5)
	for _, w := range ws[:min(len(ws), 5)] {
		ids = append(ids, w.GetId())
	}
	return "", fmt.Errorf("%q matches %d workloads; pick one: %s", arg, len(ws), strings.Join(ids, ", "))
}

func notFoundHint(err error, arg string) error {
	if connect.CodeOf(err) == connect.CodeNotFound {
		return fmt.Errorf("unknown workload %q; list them with `asterism ps`", arg)
	}
	return err
}

// tailLogs returns the newest n matching lines, oldest first, paging
// backwards when n exceeds one page.
func tailLogs(ctx context.Context, c *apiclient.Client, id, grep string, n int) ([]*asterismv1.LogLine, error) {
	var pages [][]*asterismv1.LogLine
	var before int64
	for got := 0; got < n; {
		resp, err := call(ctx, c.TailLogs, &asterismv1.TailLogsRequest{
			WorkloadId: id, Grep: grep, Limit: uint32(min(n-got, maxLogPage)), BeforeTsMs: before,
		})
		if err != nil {
			return nil, err
		}
		pages = append(pages, resp.GetLines())
		got += len(resp.GetLines())
		if !resp.GetHasMore() || resp.GetNextBeforeTsMs() == 0 || len(resp.GetLines()) == 0 {
			break
		}
		before = resp.GetNextBeforeTsMs()
	}
	slices.Reverse(pages)
	lines := slices.Concat(pages...)
	// Pages never split a timestamp group, so they can overshoot n.
	return lines[max(0, len(lines)-n):], nil
}

type logPrinter struct {
	w    io.Writer
	th   theme
	loc  *time.Location
	json bool
}

func (p logPrinter) print(lines []*asterismv1.LogLine) {
	for _, l := range lines {
		if p.json {
			_ = writeProtoLine(p.w, l)
			continue
		}
		fmt.Fprintln(p.w, formatLogLine(p.th, l, p.loc))
	}
}

func formatLogLine(th theme, l *asterismv1.LogLine, loc *time.Location) string {
	ts := time.UnixMilli(l.GetTsMs()).In(loc).Format("2006-01-02 15:04:05.000")
	stream := l.GetStream()
	marker := " "
	if stream == "stderr" {
		marker = th.bad("!")
	}
	return th.dim(ts) + " " + marker + " " + clean(l.GetText())
}

// cursor remembers the newest timestamp printed and which lines at that
// timestamp were already shown, so polling never prints a line twice.
type cursor struct {
	ts   int64
	seen map[string]int
}

func newCursor(lines []*asterismv1.LogLine) *cursor {
	c := &cursor{seen: map[string]int{}}
	c.advance(lines)
	return c
}

// advance returns the lines newer than the cursor and moves it forward.
func (c *cursor) advance(lines []*asterismv1.LogLine) []*asterismv1.LogLine {
	var fresh []*asterismv1.LogLine
	atTS := map[string]int{}
	for _, l := range lines {
		ts := l.GetTsMs()
		if ts < c.ts {
			continue
		}
		key := l.GetStream() + "\x00" + l.GetText()
		if ts == c.ts {
			atTS[key]++
			if atTS[key] <= c.seen[key] {
				continue
			}
		}
		fresh = append(fresh, l)
	}
	for _, l := range fresh {
		key := l.GetStream() + "\x00" + l.GetText()
		if l.GetTsMs() > c.ts {
			c.ts = l.GetTsMs()
			c.seen = map[string]int{}
		}
		if l.GetTsMs() == c.ts {
			c.seen[key]++
		}
	}
	return fresh
}

// follow polls TailLogs for lines newer than the last one printed. The API
// pages backwards only, so each poll reads the newest page; a burst of more
// than one page between polls is reported as a gap.
func (p logPrinter) follow(ctx context.Context, c *apiclient.Client, id, grep string, initial []*asterismv1.LogLine, stderr io.Writer) error {
	if len(initial) == 0 {
		// Nothing printed yet (--tail 0): start after the newest line.
		resp, err := call(ctx, c.TailLogs, &asterismv1.TailLogsRequest{WorkloadId: id, Grep: grep, Limit: 1})
		if err != nil {
			return err
		}
		initial = resp.GetLines()
	}
	cur := newCursor(initial)
	ticker := time.NewTicker(followInterval)
	defer ticker.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		resp, err := call(ctx, c.TailLogs, &asterismv1.TailLogsRequest{WorkloadId: id, Grep: grep, Limit: maxLogPage})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !failing {
				fmt.Fprintf(stderr, "asterism: %v (retrying)\n", friendlyError(err, c.BaseURL))
				failing = true
			}
			continue
		}
		if failing {
			fmt.Fprintln(stderr, "asterism: reconnected")
			failing = false
		}
		lines := resp.GetLines()
		if len(lines) == maxLogPage && resp.GetHasMore() && cur.ts > 0 && lines[0].GetTsMs() > cur.ts {
			fmt.Fprintln(stderr, "asterism: output too fast; some lines were skipped")
		}
		p.print(cur.advance(lines))
	}
}
