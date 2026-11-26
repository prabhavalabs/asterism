package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// topInterval is the TUI refresh period.
const topInterval = time.Second

func newTopCmd() *cobra.Command {
	var v view
	var by string
	var once bool
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Live view of the busiest workloads (refreshes every second)",
		Long: `Live view of host vitals and the busiest workloads, refreshed every second.

Keys: c sort by CPU, m sort by memory, tab toggle, q / Esc / Ctrl-C quit.

When stdout is not a terminal, or with --once or --json, top prints one
snapshot and exits. --json prints {"by", "host", "workloads"} where host is
a HostInfo and workloads are Workload messages (protojson, proto names).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			by = strings.ToLower(strings.TrimSpace(by))
			switch by {
			case "cpu":
			case "mem", "memory":
				by = "mem"
			default:
				return fmt.Errorf("--by: unknown value %q; use cpu or mem", by)
			}
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				fetch := func(ctx context.Context, by string) (*topSnapshot, error) { return fetchTop(ctx, c, by) }
				if once || v.json || !v.tty {
					snap, err := fetch(ctx, by)
					if err != nil {
						return err
					}
					if v.json {
						return writeTopJSON(v.w, snap, by)
					}
					_, err = io.WriteString(v.w, renderTop(v.th, snap, by, 0, 0, v.loc, nil))
					return err
				}
				m := newTopModel(ctx, fetch, by, v.th, v.loc, c.BaseURL)
				_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(cmd.OutOrStdout())).Run()
				if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			})
		},
	}
	cmd.Flags().StringVar(&by, "by", "cpu", "Sort by cpu or mem")
	cmd.Flags().BoolVar(&once, "once", false, "Print one snapshot and exit")
	v.bind(cmd)
	return cmd
}

type topSnapshot struct {
	Host      *asterismv1.HostInfo
	Workloads []*asterismv1.Workload
	Total     int32
	At        time.Time
}

func fetchTop(ctx context.Context, c *apiclient.Client, by string) (*topSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ov, err := call(ctx, c.GetOverview, &asterismv1.GetOverviewRequest{})
	if err != nil {
		return nil, err
	}
	order := "cpu desc"
	if by == "mem" {
		order = "memory desc"
	}
	wl, err := call(ctx, c.ListWorkloads, &asterismv1.ListWorkloadsRequest{OrderBy: order, Page: &asterismv1.PageRequest{PageSize: 200}})
	if err != nil {
		return nil, err
	}
	return &topSnapshot{Host: ov.GetHost(), Workloads: wl.GetWorkloads(), Total: wl.GetPageInfo().GetTotalSize(), At: time.Now()}, nil
}

func writeTopJSON(w io.Writer, s *topSnapshot, by string) error {
	opts := protojson.MarshalOptions{UseProtoNames: true}
	host, err := opts.Marshal(s.Host)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	out := struct {
		By        string            `json:"by"`
		Host      json.RawMessage   `json:"host"`
		Workloads []json.RawMessage `json:"workloads"`
	}{By: by, Host: host, Workloads: []json.RawMessage{}}
	for _, wl := range sortWorkloads(s.Workloads, by) {
		raw, err := opts.Marshal(wl)
		if err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		out.Workloads = append(out.Workloads, raw)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func sortWorkloads(ws []*asterismv1.Workload, by string) []*asterismv1.Workload {
	out := slices.Clone(ws)
	slices.SortStableFunc(out, func(a, b *asterismv1.Workload) int {
		cpu := cmp.Compare(finite(b.GetCpuUtil()), finite(a.GetCpuUtil()))
		mem := cmp.Compare(b.GetMemUsedBytes(), a.GetMemUsedBytes())
		if by == "mem" {
			return cmp.Or(mem, cpu, cmp.Compare(a.GetId(), b.GetId()))
		}
		return cmp.Or(cpu, mem, cmp.Compare(a.GetId(), b.GetId()))
	})
	return out
}

// renderTop draws one frame. width and height of 0 mean unlimited; err is
// the last refresh error, shown above the help line.
func renderTop(th theme, s *topSnapshot, by string, width, height int, loc *time.Location, err error) string {
	var head []string
	if s != nil && s.Host != nil {
		h := s.Host
		cpu := finite(h.GetCpuUtil()) * 100
		mem := share(h.GetMemUsedBytes(), h.GetMemTotalBytes())
		head = append(head,
			fmt.Sprintf("%s  %s  %s", th.title(clean(orDash(h.GetHostname()))),
				th.dim(fmt.Sprintf("%s · load %.2f %.2f %.2f", plural(int(h.GetCores()), "core", "cores"), h.GetLoad_1(), h.GetLoad_5(), h.GetLoad_15())),
				th.dim(s.At.In(loc).Format("15:04:05"))),
			fmt.Sprintf("%s %s %s   %s %s %s %s   %s %s",
				th.bold("CPU"), th.pct(cpu), th.bar(cpu, 10),
				th.bold("MEM"), th.pct(mem), th.bar(mem, 10), th.dim(bytesText(h.GetMemUsedBytes())+" / "+bytesText(h.GetMemTotalBytes())),
				th.bold("NET"), th.dim("rx "+rateText(h.GetNetRxBps())+" tx "+rateText(h.GetNetTxBps()))),
			"")
	}
	cpuTitle, memTitle := "CPU▼", "MEM"
	if by == "mem" {
		cpuTitle, memTitle = "CPU", "MEM▼"
	}
	t := newTable(
		column{title: "ID", max: 32}, column{title: "PROJECT", max: 18}, column{title: "ROLE"}, column{title: "STATE"},
		column{title: cpuTitle, right: true}, column{title: memTitle, right: true}, column{title: "MEM%", right: true},
		column{title: "NET RX", right: true}, column{title: "NET TX", right: true}, column{title: "RESTARTS", right: true},
	)
	var rows []*asterismv1.Workload
	var total int32
	if s != nil {
		rows, total = sortWorkloads(s.Workloads, by), s.Total
	}
	footer := []string{""}
	if err != nil {
		footer = append(footer, th.bad("refresh failed: "+err.Error()))
	}
	shown := len(rows)
	if height > 0 {
		shown = max(0, min(shown, height-len(head)-1-len(footer)-1))
	}
	for _, wl := range rows[:shown] {
		memPct := "-"
		if wl.GetMemLimitBytes() > 0 {
			memPct = th.pct(share(wl.GetMemUsedBytes(), wl.GetMemLimitBytes()))
		}
		t.add(clean(wl.GetId()), clean(orDash(short(wl.GetProjectId()))), orDash(roleName(wl.GetRole())),
			th.state(wl.GetState(), wl.GetHealth()), pctText(wl.GetCpuUtil()), bytesText(wl.GetMemUsedBytes()), memPct,
			rateText(wl.GetNetRxBps()), rateText(wl.GetNetTxBps()), fmt.Sprintf("%d", wl.GetRestartCount()))
	}
	var b strings.Builder
	for _, l := range head {
		b.WriteString(l + "\n")
	}
	if s == nil && err == nil {
		b.WriteString(th.dim("loading…") + "\n")
	} else {
		t.render(&b, th)
	}
	sortName := map[string]string{"cpu": "CPU", "mem": "memory"}[by]
	footer = append(footer, th.dim(fmt.Sprintf("%d of %d workloads by %s · c cpu · m memory · tab toggle · q quit", shown, total, sortName)))
	for _, l := range footer {
		b.WriteString(l + "\n")
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if width <= 0 {
		return out + "\n"
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "")
	}
	return strings.Join(lines, "\n")
}

// ---- bubbletea model ----------------------------------------------------------

type topFetch func(ctx context.Context, by string) (*topSnapshot, error)

type topModel struct {
	ctx    context.Context
	fetch  topFetch
	by     string
	th     theme
	loc    *time.Location
	server string

	snap          *topSnapshot
	err           error
	width, height int
}

type topDataMsg struct {
	snap *topSnapshot
	err  error
}

type topTickMsg struct{}

func newTopModel(ctx context.Context, fetch topFetch, by string, th theme, loc *time.Location, server string) topModel {
	return topModel{ctx: ctx, fetch: fetch, by: by, th: th, loc: loc, server: server}
}

func (m topModel) load() tea.Cmd {
	ctx, fetch, by := m.ctx, m.fetch, m.by
	return func() tea.Msg {
		snap, err := fetch(ctx, by)
		return topDataMsg{snap: snap, err: err}
	}
}

// Init starts the first fetch. Each fetch result schedules the next tick, so
// at most one request is in flight and a slow daemon never queues work.
func (m topModel) Init() tea.Cmd { return m.load() }

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "c":
			m.by = "cpu"
		case "m":
			m.by = "mem"
		case "tab", "s":
			if m.by == "cpu" {
				m.by = "mem"
			} else {
				m.by = "cpu"
			}
		}
	case topDataMsg:
		if msg.err != nil {
			m.err = friendlyError(msg.err, m.server)
		} else {
			m.snap, m.err = msg.snap, nil
		}
		return m, tea.Tick(topInterval, func(time.Time) tea.Msg { return topTickMsg{} })
	case topTickMsg:
		return m, m.load()
	}
	return m, nil
}

func (m topModel) View() tea.View {
	v := tea.NewView(renderTop(m.th, m.snap, m.by, m.width, m.height, m.loc, m.err))
	v.AltScreen = true
	return v
}
