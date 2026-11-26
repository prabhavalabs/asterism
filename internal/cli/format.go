package cli

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// enumName turns a protobuf enum name such as "WORKLOAD_ROLE_APP" into "app";
// unspecified values become "".
func enumName(full, prefix string) string {
	s := strings.TrimPrefix(full, prefix)
	if s == "UNSPECIFIED" || s == "" {
		return ""
	}
	return strings.ToLower(s)
}

// short drops an entity-id prefix: "proj:shop" -> "shop".
func short(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[i+1:]
	}
	return id
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// pctText formats a 0-1 ratio as a percentage with one decimal.
func pctText(ratio float64) string { return fmt.Sprintf("%.1f%%", finite(ratio)*100) }

func share(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// bytesText renders a byte count with a binary unit.
func bytesText(b uint64) string {
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

func rateText(bps float64) string {
	bps = finite(bps)
	if bps < 0 {
		bps = 0
	}
	return bytesText(uint64(bps)) + "/s"
}

func durationText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
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

// ago renders how long before now ms was, e.g. "12m ago".
func ago(ms int64, now time.Time) string {
	if ms <= 0 {
		return "-"
	}
	d := now.Sub(time.UnixMilli(ms))
	if d < time.Minute {
		return "just now"
	}
	return durationText(d) + " ago"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
