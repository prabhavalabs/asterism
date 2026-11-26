package dockercli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// StatsCommand samples docker stats once for every running container.
const StatsCommand = `docker stats --no-stream --no-trunc --format "{{.ID}}|{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.NetIO}}|{{.BlockIO}}|{{.PIDs}}" 2>/dev/null || true`

// CgroupIOScript prints cgroup v2 io.stat for every Docker container cgroup
// (systemd and cgroupfs drivers). Each file is prefixed with "#<path>".
const CgroupIOScript = `for f in /sys/fs/cgroup/system.slice/docker-*.scope/io.stat /sys/fs/cgroup/docker/*/io.stat; do [ -r "$f" ] || continue; echo "#$f"; cat "$f"; done 2>/dev/null`

// NetDevScript prints /proc/<pid>/net/dev for each pid (each prefixed "#<pid>").
// Pids are integers so the script cannot be used for injection.
func NetDevScript(pids []int32) string {
	if len(pids) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("for p in")
	n := 0
	for _, p := range pids {
		if p <= 0 {
			continue
		}
		fmt.Fprintf(&b, " %d", p)
		n++
	}
	if n == 0 {
		return ""
	}
	b.WriteString(`; do echo "#$p"; cat /proc/$p/net/dev 2>/dev/null; done`)
	return b.String()
}

// StatSample is one docker stats row with cumulative counters.
type StatSample struct {
	ID            string
	Name          string
	CPUUtil       float64 // CPU% / 100
	MemUsed       uint64
	MemLimit      uint64
	NetRx, NetTx  uint64
	BlkRd, BlkWr  uint64
	PIDs          uint32
	HasNetCounter bool
	HasBlkCounter bool
}

// ParseStats parses StatsCommand output, keyed by full container id.
func ParseStats(out string) map[string]StatSample {
	res := make(map[string]StatSample)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.Split(line, "|")
		if len(parts) < 4 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		if !ValidContainerID(id) {
			continue
		}
		s := StatSample{ID: id, Name: strings.TrimSpace(parts[1])}
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(parts[2]), "%"), 64)
		s.CPUUtil = clamp(cpu/100.0, 0, 256)
		s.MemUsed, s.MemLimit = parsePair(parts[3])
		if len(parts) > 4 {
			s.NetRx, s.NetTx = parsePair(parts[4])
			s.HasNetCounter = true
		}
		if len(parts) > 5 {
			s.BlkRd, s.BlkWr = parsePair(parts[5])
			s.HasBlkCounter = true
		}
		if len(parts) > 6 {
			p, _ := strconv.ParseUint(strings.TrimSpace(parts[6]), 10, 32)
			s.PIDs = uint32(p)
		}
		res[id] = s
	}
	return res
}

func parsePair(s string) (uint64, uint64) {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return ParseHumanBytes(s), 0
	}
	return ParseHumanBytes(a), ParseHumanBytes(b)
}

var cgroupIDRe = regexp.MustCompile(`([0-9a-f]{64})`)

// IOCounter is a cumulative read/write byte pair.
type IOCounter struct {
	Read, Write uint64
}

// ParseCgroupIO parses CgroupIOScript output keyed by full container id.
func ParseCgroupIO(out string) map[string]IOCounter {
	res := make(map[string]IOCounter)
	cur := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			cur = ""
			if m := cgroupIDRe.FindStringSubmatch(line); len(m) == 2 {
				cur = m[1]
				res[cur] = IOCounter{}
			}
			continue
		}
		if cur == "" {
			continue
		}
		c := res[cur]
		for _, f := range strings.Fields(line) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			n, _ := strconv.ParseUint(v, 10, 64)
			switch k {
			case "rbytes":
				c.Read += n
			case "wbytes":
				c.Write += n
			}
		}
		res[cur] = c
	}
	return res
}

// ParseNetDevByPid parses NetDevScript output keyed by pid (rx, tx summed over non-loopback interfaces).
func ParseNetDevByPid(out string) map[int32]IOCounter {
	res := make(map[int32]IOCounter)
	var cur int32 = -1
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			p, err := strconv.ParseInt(strings.TrimPrefix(line, "#"), 10, 32)
			if err != nil {
				cur = -1
				continue
			}
			cur = int32(p)
			continue
		}
		if cur < 0 {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		c := res[cur]
		c.Read += rx
		c.Write += tx
		res[cur] = c
	}
	return res
}

// ParseHumanBytes parses Docker's human sizes ("1.2GB", "342.5MiB", "0B").
func ParseHumanBytes(raw string) uint64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	val, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	mult := 1.0
	switch strings.TrimSpace(s[i:]) {
	case "", "B":
	case "KiB":
		mult = 1 << 10
	case "MiB":
		mult = 1 << 20
	case "GiB":
		mult = 1 << 30
	case "TiB":
		mult = 1 << 40
	case "kB", "KB":
		mult = 1e3
	case "MB":
		mult = 1e6
	case "GB":
		mult = 1e9
	case "TB":
		mult = 1e12
	}
	return uint64(val * mult)
}

// RateTracker turns cumulative counters into per-second rates.
type RateTracker struct {
	prev map[string]rateState
}

type rateState struct {
	ts             time.Time
	rx, tx, rd, wr uint64
}

// Rates holds per-second rates computed by RateTracker.
type Rates struct {
	NetRx, NetTx, BlkRd, BlkWr float64
}

// NewRateTracker returns an empty tracker.
func NewRateTracker() *RateTracker {
	return &RateTracker{prev: make(map[string]rateState)}
}

// Observe records counters for id at ts and returns rates since the previous
// observation. Counter resets (container restart) yield zero for that interval.
func (r *RateTracker) Observe(id string, ts time.Time, rx, tx, rd, wr uint64) Rates {
	var out Rates
	if p, ok := r.prev[id]; ok {
		dt := ts.Sub(p.ts).Seconds()
		if dt > 0 {
			rate := func(cur, prev uint64) float64 {
				if cur < prev {
					return 0
				}
				return float64(cur-prev) / dt
			}
			out = Rates{NetRx: rate(rx, p.rx), NetTx: rate(tx, p.tx), BlkRd: rate(rd, p.rd), BlkWr: rate(wr, p.wr)}
		}
	}
	r.prev[id] = rateState{ts: ts, rx: rx, tx: tx, rd: rd, wr: wr}
	return out
}

// Forget drops ids not present in keep.
func (r *RateTracker) Forget(keep map[string]bool) {
	for id := range r.prev {
		if !keep[id] {
			delete(r.prev, id)
		}
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
