package procfs

import "bytes"

// CPUTimes is the aggregate "cpu" line of /proc/stat, in USER_HZ ticks.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

// Total is the sum of all accounted time (guest time is already included in
// user and nice, so it is not added twice).
func (c CPUTimes) Total() uint64 {
	return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
}

// IdleAll is idle plus iowait.
func (c CPUTimes) IdleAll() uint64 { return c.Idle + c.IOWait }

var cpuPrefix = []byte("cpu ")

// ParseStat returns the aggregate CPU line and the boot time (btime, unix
// seconds; 0 when absent) from /proc/stat.
func ParseStat(b []byte) (cpu CPUTimes, btime uint64, ok bool) {
	var f [12][]byte
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		switch {
		case bytes.HasPrefix(line, cpuPrefix):
			fs := Fields(line, f[:0])
			vals := [8]*uint64{&cpu.User, &cpu.Nice, &cpu.System, &cpu.Idle, &cpu.IOWait, &cpu.IRQ, &cpu.SoftIRQ, &cpu.Steal}
			for i, p := range vals {
				if i+1 < len(fs) {
					*p, _ = ParseUint(fs[i+1])
				}
			}
			ok = len(fs) >= 5
		case bytes.HasPrefix(line, []byte("btime ")):
			btime, _ = ParseUint(Field(line, 1))
		}
	}
	return cpu, btime, ok
}

// Meminfo holds the /proc/meminfo values the agent uses, in bytes.
type Meminfo struct {
	MemTotal, MemFree, MemAvailable, Buffers, Cached, SReclaimable, SwapTotal, SwapFree uint64
	HasAvailable                                                                        bool
}

// Used is MemTotal minus MemAvailable (falling back to free+buffers+cache
// on kernels older than 3.14).
func (m Meminfo) Used() uint64 {
	avail := m.MemAvailable
	if !m.HasAvailable {
		avail = m.MemFree + m.Buffers + m.Cached + m.SReclaimable
	}
	if avail > m.MemTotal {
		return 0
	}
	return m.MemTotal - avail
}

// SwapUsed is SwapTotal minus SwapFree.
func (m Meminfo) SwapUsed() uint64 {
	if m.SwapFree > m.SwapTotal {
		return 0
	}
	return m.SwapTotal - m.SwapFree
}

// ParseMeminfo parses /proc/meminfo ("Key:   123 kB").
func ParseMeminfo(b []byte) (m Meminfo, ok bool) {
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := line[:colon]
		var dst *uint64
		switch string(key) { // string(key) in a switch does not allocate
		case "MemTotal":
			dst = &m.MemTotal
			ok = true
		case "MemFree":
			dst = &m.MemFree
		case "MemAvailable":
			dst = &m.MemAvailable
			m.HasAvailable = true
		case "Buffers":
			dst = &m.Buffers
		case "Cached":
			dst = &m.Cached
		case "SReclaimable":
			dst = &m.SReclaimable
		case "SwapTotal":
			dst = &m.SwapTotal
		case "SwapFree":
			dst = &m.SwapFree
		default:
			continue
		}
		rest := line[colon+1:]
		v, _ := ParseUint(Field(rest, 0))
		if u := Field(rest, 1); len(u) > 0 && (u[0] == 'k' || u[0] == 'K') {
			v *= 1024
		}
		*dst = v
	}
	return m, ok
}

// ParseLoadavg parses /proc/loadavg.
func ParseLoadavg(b []byte) (l1, l5, l15 float64, ok bool) {
	var f [5][]byte
	fs := Fields(b, f[:0])
	if len(fs) < 3 {
		return 0, 0, 0, false
	}
	l1, ok1 := ParseDecimal(fs[0])
	l5, ok2 := ParseDecimal(fs[1])
	l15, ok3 := ParseDecimal(fs[2])
	return l1, l5, l15, ok1 && ok2 && ok3
}

// ParseUptime parses /proc/uptime (seconds since boot).
func ParseUptime(b []byte) (float64, bool) {
	return ParseDecimal(Field(b, 0))
}

// ParseNetDev calls fn for every interface in /proc/net/dev with the
// cumulative received and transmitted byte counters.
func ParseNetDev(b []byte, fn func(name []byte, rx, tx uint64)) {
	var f [17][]byte
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue // header lines
		}
		name := bytes.TrimSpace(line[:colon])
		fs := Fields(line[colon+1:], f[:0])
		if len(fs) < 9 {
			continue
		}
		rx, ok1 := ParseUint(fs[0])
		tx, ok2 := ParseUint(fs[8])
		if ok1 && ok2 {
			fn(name, rx, tx)
		}
	}
}

// SumNetDev sums rx/tx over all interfaces except loopback (per-container
// network namespaces contain lo and eth0..N).
func SumNetDev(b []byte) (rx, tx uint64, ok bool) {
	ParseNetDev(b, func(name []byte, r, t uint64) {
		ok = true
		if string(name) == "lo" {
			return
		}
		rx += r
		tx += t
	})
	return rx, tx, ok
}

// SectorSize is the fixed unit of /proc/diskstats sector counters.
const SectorSize = 512

// ParseDiskstats calls fn for every device line of /proc/diskstats with the
// sectors read and written.
func ParseDiskstats(b []byte, fn func(name []byte, readSectors, writeSectors uint64)) {
	var f [20][]byte
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		fs := Fields(line, f[:0])
		if len(fs) < 10 {
			continue
		}
		rd, ok1 := ParseUint(fs[5])
		wr, ok2 := ParseUint(fs[9])
		if ok1 && ok2 {
			fn(fs[2], rd, wr)
		}
	}
}

// IsWholeDiskName is a fallback for when /sys/block is not readable: it
// accepts common whole-disk names (sda, vda, xvda, nvme0n1, mmcblk0) and
// rejects partitions, loop, ram, zram and device-mapper devices.
func IsWholeDiskName(name []byte) bool {
	n := string(name)
	switch {
	case hasPrefixDigits(n, "nvme"):
		// nvme0n1 yes, nvme0n1p1 no
		return !bytes.Contains(name, []byte("p")) || bytes.LastIndexByte(name, 'p') < bytes.LastIndexByte(name, 'n')
	case hasPrefixDigits(n, "mmcblk"):
		return !bytes.Contains(name[6:], []byte("p"))
	}
	for _, p := range [...]string{"sd", "vd", "xvd", "hd"} {
		if len(n) > len(p) && n[:len(p)] == p {
			rest := n[len(p):]
			for i := 0; i < len(rest); i++ {
				if rest[i] < 'a' || rest[i] > 'z' {
					return false
				}
			}
			return true
		}
	}
	return false
}

func hasPrefixDigits(s, p string) bool {
	return len(s) > len(p) && s[:len(p)] == p && s[len(p)] >= '0' && s[len(p)] <= '9'
}
