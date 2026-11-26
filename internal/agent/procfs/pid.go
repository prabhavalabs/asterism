package procfs

import (
	"bytes"
	"os"
	"strconv"
)

// PidStat holds the /proc/<pid>/stat fields the agent uses.
type PidStat struct {
	Comm      []byte // aliases the input buffer
	State     byte
	PPid      int64
	UTime     uint64 // clock ticks
	STime     uint64
	StartTime uint64 // clock ticks after boot
	RSSPages  uint64
}

// ParsePidStat parses /proc/<pid>/stat. The command name is in parentheses
// and may itself contain spaces and parentheses, so fields are counted from
// the last ')'.
func ParsePidStat(b []byte) (PidStat, bool) {
	var s PidStat
	open := bytes.IndexByte(b, '(')
	closeIdx := bytes.LastIndexByte(b, ')')
	if open < 0 || closeIdx < open {
		return s, false
	}
	s.Comm = b[open+1 : closeIdx]
	var f [52][]byte
	fs := Fields(b[closeIdx+1:], f[:0])
	// After ')': state(3) ppid(4) ... utime(14) stime(15) ... starttime(22) vsize(23) rss(24).
	if len(fs) < 22 {
		return s, false
	}
	if len(fs[0]) > 0 {
		s.State = fs[0][0]
	}
	s.PPid, _ = ParseInt(fs[1])
	s.UTime, _ = ParseUint(fs[11])
	s.STime, _ = ParseUint(fs[12])
	s.StartTime, _ = ParseUint(fs[19])
	s.RSSPages, _ = ParseUint(fs[21])
	return s, true
}

// ParseStatusUID returns the real uid from /proc/<pid>/status.
func ParseStatusUID(b []byte) (uint32, bool) {
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		if bytes.HasPrefix(line, []byte("Uid:")) {
			v, ok := ParseUint(Field(line[4:], 0))
			return uint32(v), ok
		}
	}
	return 0, false
}

// SplitCmdline converts a NUL-separated /proc/<pid>/cmdline into arguments.
func SplitCmdline(b []byte) []string {
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = string(p)
	}
	return out
}

// ListPids returns the numeric entries of procDir (normally <root>/proc).
func ListPids(procDir string) ([]int, error) {
	d, err := os.Open(procDir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(names))
	for _, n := range names {
		if n == "" || n[0] < '0' || n[0] > '9' {
			continue
		}
		if p, err := strconv.Atoi(n); err == nil && p > 0 {
			pids = append(pids, p)
		}
	}
	return pids, nil
}

// PidPath returns <procDir>/<pid>/<name> without fmt.
func PidPath(procDir string, pid int, name string) string {
	return procDir + "/" + strconv.Itoa(pid) + "/" + name
}

// SocketInodes walks /proc/<pid>/fd for pid and calls fn for every socket
// inode. It returns an error when the fd directory cannot be read (usually
// EACCES without CAP_SYS_PTRACE / CAP_DAC_READ_SEARCH).
func SocketInodes(procDir string, pid int, fn func(inode uint64)) error {
	dir := PidPath(procDir, pid, "fd")
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return err
	}
	for _, n := range names {
		link, err := os.Readlink(dir + "/" + n)
		if err != nil || len(link) < 10 || link[:8] != "socket:[" {
			continue
		}
		if ino, err := strconv.ParseUint(link[8:len(link)-1], 10, 64); err == nil {
			fn(ino)
		}
	}
	return nil
}
