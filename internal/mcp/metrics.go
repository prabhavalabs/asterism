package mcp

import (
	"fmt"
	"slices"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// hotWindow is how much history the hub keeps in memory (900 points at 1 s).
const hotWindow = 15 * time.Minute

const maxRawPoints = 500

var nowFunc = time.Now

func parseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return hotWindow, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration such as 5m or 15m", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	if d > hotWindow {
		return 0, fmt.Errorf("%s is longer than the %s the hub keeps in memory", d, hotWindow)
	}
	return d, nil
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 time such as 2026-11-26T10:00:00Z", s)
	}
	return t, nil
}

// unitFor reports the unit used in results; ratio metrics are converted to
// percentages.
func unitFor(metric string) (unit string, scale float64) {
	switch {
	case strings.HasSuffix(metric, ".util"), strings.HasSuffix(metric, "_ratio"):
		return "percent", 100
	case strings.HasSuffix(metric, "_bytes"):
		return "bytes", 1
	case strings.HasSuffix(metric, "_bps"):
		return "bytes_per_second", 1
	default:
		return "value", 1
	}
}

func seriesStats(metric string, pts []*asterismv1.MetricPoint, raw bool) SeriesStats {
	unit, scale := unitFor(metric)
	st := SeriesStats{Metric: metric, Unit: unit, Count: len(pts)}
	if len(pts) == 0 {
		return st
	}
	vals := make([]float64, len(pts))
	sum := 0.0
	for i, p := range pts {
		v := p.GetValue() * scale
		vals[i] = v
		sum += v
	}
	st.Last = round2(vals[len(vals)-1])
	st.Avg = round2(sum / float64(len(vals)))
	sorted := slices.Clone(vals)
	slices.Sort(sorted)
	st.Min = round2(sorted[0])
	st.Max = round2(sorted[len(sorted)-1])
	st.P95 = round2(sorted[min(len(sorted)-1, int(float64(len(sorted))*0.95))])
	if raw {
		stride := (len(pts) + maxRawPoints - 1) / maxRawPoints
		for i := 0; i < len(pts); i += stride {
			st.Points = append(st.Points, PointView{Time: iso(pts[i].GetTsMs()), Value: round2(vals[i])})
		}
	}
	return st
}

func fmtValue(v float64, unit string) string {
	switch unit {
	case "percent":
		return fmt.Sprintf("%.1f%%", v)
	case "bytes":
		return human(uint64(max(v, 0)))
	case "bytes_per_second":
		return humanRate(v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}
