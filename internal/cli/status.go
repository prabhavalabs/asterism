package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func newStatusCmd() *cobra.Command {
	var v view
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show host vitals and a categorized project summary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				resp, err := call(ctx, c.GetOverview, &asterismv1.GetOverviewRequest{})
				if err != nil {
					return err
				}
				if v.json {
					return writeProto(v.w, resp)
				}
				renderStatus(v.w, v.th, resp, v.now())
				return nil
			})
		},
	}
	v.bind(cmd)
	return cmd
}

func healthName(h asterismv1.HealthStatus) string {
	return enumName(h.String(), "HEALTH_STATUS_")
}

func (t theme) health(h asterismv1.HealthStatus) string {
	name := orDash(healthName(h))
	switch h {
	case asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY:
		return t.ok(name)
	case asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED:
		return t.warn(name)
	case asterismv1.HealthStatus_HEALTH_STATUS_DOWN:
		return t.bad(name)
	default:
		return t.dim(name)
	}
}

func (t theme) pct(p float64) string {
	return t.level(pctLevel(p), fmt.Sprintf("%.1f%%", finite(p)))
}

func renderStatus(w io.Writer, th theme, o *asterismv1.GetOverviewResponse, now time.Time) {
	h := o.GetHost()
	facts := []string{}
	for _, f := range []string{h.GetOs(), h.GetArch()} {
		if f != "" {
			facts = append(facts, clean(f))
		}
	}
	if h.GetCores() > 0 {
		facts = append(facts, plural(int(h.GetCores()), "core", "cores"))
	}
	if h.GetUptimeSeconds() > 0 {
		facts = append(facts, "up "+durationText(time.Duration(h.GetUptimeSeconds())*time.Second))
	}
	if m := h.GetSourceMode(); m != "" {
		mode := m
		if h.GetSshTarget() != "" {
			mode += " " + clean(h.GetSshTarget())
		}
		facts = append(facts, mode+" mode")
	}
	headline := th.title(clean(orDash(h.GetHostname())))
	if len(facts) > 0 {
		headline += "  " + th.dim(strings.Join(facts, " · "))
	}
	fmt.Fprintf(w, "%s\n\n", headline)

	cpu := finite(h.GetCpuUtil()) * 100
	vitals := newTable(column{title: ""}, column{title: "", right: true}, column{title: ""}, column{title: ""})
	vitals.add(th.bold("CPU"), th.pct(cpu), th.bar(cpu, 20),
		th.dim(fmt.Sprintf("load %.2f %.2f %.2f", h.GetLoad_1(), h.GetLoad_5(), h.GetLoad_15())))
	usage := func(label string, used, total uint64) {
		if total == 0 {
			return
		}
		p := share(used, total)
		vitals.add(th.bold(label), th.pct(p), th.bar(p, 20), th.dim(bytesText(used)+" / "+bytesText(total)))
	}
	usage("MEM", h.GetMemUsedBytes(), h.GetMemTotalBytes())
	usage("SWAP", h.GetSwapUsedBytes(), h.GetSwapTotalBytes())
	usage("DISK", h.GetDiskUsedBytes(), h.GetDiskTotalBytes())
	vitals.add(th.bold("NET"), "", "", th.dim("rx "+rateText(h.GetNetRxBps())+"  tx "+rateText(h.GetNetTxBps())))
	renderBare(w, vitals)
	fmt.Fprintln(w)

	if cc := o.GetContainerCounts(); cc.GetTotal() > 0 {
		parts := []string{
			fmt.Sprintf("%d total", cc.GetTotal()),
			th.ok(fmt.Sprintf("%d running", cc.GetRunning())),
		}
		if n := cc.GetExited(); n > 0 {
			parts = append(parts, th.dim(fmt.Sprintf("%d exited", n)))
		}
		if n := cc.GetAttention(); n > 0 {
			parts = append(parts, th.bad(fmt.Sprintf("%d need attention", n)))
		}
		fmt.Fprintf(w, "%s  %s\n", th.bold("Containers"), strings.Join(parts, " · "))
	}
	alerts := fmt.Sprintf("%d active alerts", o.GetActiveAlertCount())
	if o.GetActiveAlertCount() > 0 {
		alerts = th.warn(alerts)
	}
	fmt.Fprintf(w, "%s  %s · %s · %s\n\n", th.bold("Exposure  "),
		plural(int(o.GetPublicPortCount()), "public port", "public ports"),
		plural(int(o.GetIngressRouteCount()), "ingress route", "ingress routes"), alerts)

	renderProjects(w, th, o.GetProjects())

	if evs := o.GetRecentEvents(); len(evs) > 0 {
		fmt.Fprintf(w, "\n%s\n", th.bold("RECENT EVENTS"))
		t := newTable(column{title: "WHEN", right: true}, column{title: "SEVERITY"}, column{title: "ENTITY", max: 24}, column{title: "MESSAGE"})
		for _, e := range evs[:min(len(evs), 5)] {
			t.add(ago(e.GetTsMs(), now), th.severity(e.GetSeverity()), clean(orDash(e.GetEntityName())), clean(e.GetMessage()))
		}
		t.render(w, th)
	}
}

// renderBare prints a table without its header row.
func renderBare(w io.Writer, t *table) {
	var buf strings.Builder
	t.render(&buf, theme{})
	_, body, _ := strings.Cut(buf.String(), "\n")
	_, _ = io.WriteString(w, body)
}

// renderProjects prints one table grouped by category, biggest memory first.
func renderProjects(w io.Writer, th theme, projects []*asterismv1.Project) {
	if len(projects) == 0 {
		fmt.Fprintln(w, th.dim("No projects discovered yet."))
		return
	}
	ps := slices.Clone(projects)
	slices.SortStableFunc(ps, func(a, b *asterismv1.Project) int {
		ca, cb := a.GetCategory(), b.GetCategory()
		if (ca == "") != (cb == "") {
			if ca == "" {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(ca, cb), cmp.Compare(b.GetMemUsedBytes(), a.GetMemUsedBytes()), cmp.Compare(a.GetName(), b.GetName()))
	})
	t := newTable(
		column{title: "CATEGORY"}, column{title: "PROJECT", max: 28}, column{title: "STATUS"},
		column{title: "SCORE", right: true}, column{title: "CPU", right: true}, column{title: "MEM", right: true},
		column{title: "SHARE", right: true}, column{title: "WORKLOADS", right: true},
	)
	prev := "\x00"
	for _, p := range ps {
		cat := ""
		if p.GetCategory() != prev {
			cat = th.accent(clean(orDash(p.GetCategory())))
			prev = p.GetCategory()
		}
		t.add(cat, clean(p.GetName()), th.health(p.GetStatus()), fmt.Sprintf("%d", p.GetHealthScore()),
			pctText(p.GetCpuUtil()), bytesText(p.GetMemUsedBytes()), fmt.Sprintf("%.1f%%", finite(p.GetMemSharePct())),
			fmt.Sprintf("%d", p.GetWorkloadCount()))
	}
	t.render(w, th)
}

func (t theme) severity(s string) string {
	switch strings.ToLower(s) {
	case "critical", "error":
		return t.bad(s)
	case "warning":
		return t.warn(s)
	default:
		return t.dim(orDash(s))
	}
}
