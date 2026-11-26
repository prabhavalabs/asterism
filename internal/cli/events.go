package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func newEventsCmd() *cobra.Command {
	var v view
	var since, severity, entity, kind, query string
	var limit int
	cmd := &cobra.Command{
		Use:   "events",
		Short: "List recent events and alerts (oldest first)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sevs, err := severitiesFrom(severity)
			if err != nil {
				return err
			}
			if limit <= 0 {
				return fmt.Errorf("--limit must be positive")
			}
			return v.run(cmd, func(ctx context.Context, c *apiclient.Client) error {
				var cutoff int64
				if since != "" {
					t, err := sinceTime(since, v.now())
					if err != nil {
						return err
					}
					cutoff = t.UnixMilli()
				}
				resp, err := fetchEvents(ctx, c, &asterismv1.ListEventsRequest{
					EntityId: entity, Severities: sevs, KindGroup: kind, Query: query, OrderBy: "ts desc",
				}, cutoff, limit)
				if err != nil {
					return err
				}
				if v.json {
					return writeProto(v.w, resp)
				}
				renderEvents(v.w, v.th, resp, v.loc)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "Only events newer than this: a duration (30m, 1h, 2d) or an RFC 3339 time")
	cmd.Flags().StringVar(&severity, "severity", "", "Minimum severity: info, warning, critical")
	cmd.Flags().StringVar(&entity, "entity", "", "Only events of this entity id, e.g. ctr:shop-api-1")
	cmd.Flags().StringVar(&kind, "kind", "", "Kind group: lifecycle, audit, other")
	cmd.Flags().StringVarP(&query, "query", "q", "", "Substring over message, kind and entity")
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum events to show (the newest are kept)")
	v.bind(cmd)
	return cmd
}

// severitiesFrom expands a minimum severity to the API's severity list.
func severitiesFrom(level string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info", "all":
		return nil, nil
	case "warning", "warn":
		return []string{"warning", "critical"}, nil
	case "critical", "error":
		return []string{"critical"}, nil
	default:
		return nil, fmt.Errorf("--severity: unknown value %q; use info, warning or critical", level)
	}
}

// sinceTime parses "90s", "30m", "1h", "2d", "1w" or an RFC 3339 time.
func sinceTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	mult := time.Duration(0)
	switch {
	case strings.HasSuffix(s, "d"):
		mult = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		mult = 7 * 24 * time.Hour
	}
	if mult > 0 {
		n, err := strconv.ParseFloat(strings.TrimRight(s, "dw"), 64)
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("--since: invalid duration %q", s)
		}
		return now.Add(-time.Duration(n * float64(mult))), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("--since: %q is not a duration (30m, 1h, 2d) or RFC 3339 time", s)
	}
	return now.Add(-d), nil
}

// fetchEvents pages newest-first until limit events at or after cutoff are
// collected. The returned events stay newest first, like the API.
func fetchEvents(ctx context.Context, c *apiclient.Client, req *asterismv1.ListEventsRequest, cutoff int64, limit int) (*asterismv1.ListEventsResponse, error) {
	out := &asterismv1.ListEventsResponse{}
	for page := int32(1); ; page++ {
		req.Page = &asterismv1.PageRequest{Page: page, PageSize: int32(min(max(limit, 10), 200))}
		resp, err := call(ctx, c.ListEvents, req)
		if err != nil {
			return nil, err
		}
		if page == 1 {
			out.SeverityCounts, out.KindCounts = resp.GetSeverityCounts(), resp.GetKindCounts()
			out.PageInfo = resp.GetPageInfo()
		}
		done := len(resp.GetEvents()) == 0 || page >= resp.GetPageInfo().GetTotalPages()
		for _, e := range resp.GetEvents() {
			if e.GetTsMs() < cutoff || len(out.Events) >= limit {
				done = true
				break
			}
			out.Events = append(out.Events, e)
		}
		if done {
			break
		}
	}
	out.PageInfo = &asterismv1.PageInfo{Page: 1, PageSize: int32(len(out.Events)), TotalSize: out.GetPageInfo().GetTotalSize(), TotalPages: 1}
	return out, nil
}

func renderEvents(w io.Writer, th theme, r *asterismv1.ListEventsResponse, loc *time.Location) {
	evs := slices.Clone(r.GetEvents())
	if len(evs) == 0 {
		fmt.Fprintln(w, th.dim("No events match."))
		return
	}
	slices.Reverse(evs) // oldest first, newest at the bottom
	t := newTable(column{title: "TIME"}, column{title: "SEVERITY"}, column{title: "KIND", max: 28},
		column{title: "ENTITY", max: 28}, column{title: "MESSAGE"})
	for _, e := range evs {
		entity := e.GetEntityName()
		if entity == "" {
			entity = e.GetEntityId()
		}
		t.add(th.dim(time.UnixMilli(e.GetTsMs()).In(loc).Format("2006-01-02 15:04:05")), th.severity(e.GetSeverity()),
			clean(orDash(e.GetKind())), clean(orDash(entity)), clean(e.GetMessage()))
	}
	t.render(w, th)
	sc := r.GetSeverityCounts()
	fmt.Fprintf(w, "\n%s\n", th.dim(fmt.Sprintf("%d of %d events · %d critical · %d warning · %d info",
		len(evs), r.GetPageInfo().GetTotalSize(), sc.GetCritical(), sc.GetWarning(), sc.GetInfo())))
}
