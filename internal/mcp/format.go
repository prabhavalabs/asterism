package mcp

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// enumName turns a protobuf enum name such as "WORKLOAD_ROLE_APP" into "app".
// Unspecified values become "".
func enumName(full, prefix string) string {
	s := strings.TrimPrefix(full, prefix)
	if s == "UNSPECIFIED" || s == "" {
		return ""
	}
	return strings.ToLower(s)
}

// enumValue resolves a short name such as "app" back to an enum number using the
// generated <Enum>_value map. Empty input returns 0 (unspecified).
func enumValue(short, prefix string, values map[string]int32) (int32, error) {
	short = strings.TrimSpace(short)
	if short == "" {
		return 0, nil
	}
	if v, ok := values[prefix+strings.ToUpper(short)]; ok && v != 0 {
		return v, nil
	}
	var opts []string
	for name, v := range values {
		if v != 0 {
			opts = append(opts, strings.ToLower(strings.TrimPrefix(name, prefix)))
		}
	}
	slices.Sort(opts)
	return 0, fmt.Errorf("unknown value %q; use one of: %s", short, strings.Join(opts, ", "))
}

// iso formats epoch milliseconds as RFC 3339 UTC; zero becomes "".
func iso(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// pct converts a 0-1 ratio to a percentage rounded to one decimal.
func pct(ratio float64) float64 { return round1(ratio * 100) }

func round1(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

func share(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return round1(float64(used) / float64(total) * 100)
}

// human renders a byte count with a binary unit for text summaries.
func human(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

func humanRate(bps float64) string {
	if bps < 0 {
		bps = 0
	}
	return human(uint64(bps)) + "/s"
}

func humanDuration(seconds uint64) string {
	d := time.Duration(seconds) * time.Second
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
