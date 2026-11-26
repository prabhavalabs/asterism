package procfs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// CgroupRoot is the cgroup v2 mount point relative to the host root.
const CgroupRoot = "/sys/fs/cgroup"

var (
	keyUsageUsec    = []byte("usage_usec")
	keyInactiveFile = []byte("inactive_file")
	keyOOMKill      = []byte("oom_kill")
	keyThrottled    = []byte("throttled_usec")
)

// CPUUsageUsec returns usage_usec from cgroup v2 cpu.stat.
func CPUUsageUsec(cpuStat []byte) (uint64, bool) { return KeyValue(cpuStat, keyUsageUsec) }

// CPUThrottledUsec returns throttled_usec from cgroup v2 cpu.stat.
func CPUThrottledUsec(cpuStat []byte) (uint64, bool) { return KeyValue(cpuStat, keyThrottled) }

// InactiveFile returns inactive_file from cgroup v2 memory.stat. Docker
// reports memory usage as memory.current minus inactive_file.
func InactiveFile(memStat []byte) (uint64, bool) { return KeyValue(memStat, keyInactiveFile) }

// OOMKills returns oom_kill from cgroup v2 memory.events.
func OOMKills(memEvents []byte) (uint64, bool) { return KeyValue(memEvents, keyOOMKill) }

// ParseIOStat sums rbytes and wbytes over all devices in cgroup v2 io.stat
// ("8:0 rbytes=1 wbytes=2 rios=3 wios=4 dbytes=0 dios=0").
func ParseIOStat(b []byte) (rbytes, wbytes uint64) {
	var f [8][]byte
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		fs := Fields(line, f[:0])
		for _, kv := range fs[min(1, len(fs)):] {
			eq := bytes.IndexByte(kv, '=')
			if eq <= 0 {
				continue
			}
			v, ok := ParseUint(kv[eq+1:])
			if !ok {
				continue
			}
			switch string(kv[:eq]) {
			case "rbytes":
				rbytes += v
			case "wbytes":
				wbytes += v
			}
		}
	}
	return rbytes, wbytes
}

// CgroupV2Path returns the unified ("0::") path from /proc/<pid>/cgroup.
// The path is relative to the reader's cgroup namespace and may start with
// "/.." when the agent runs in a private cgroup namespace.
func CgroupV2Path(b []byte) ([]byte, bool) {
	for len(b) > 0 {
		var line []byte
		line, b = NextLine(b)
		if bytes.HasPrefix(line, []byte("0::")) {
			return line[3:], true
		}
	}
	return nil, false
}

// ContainerIDFromCgroup extracts a 64-hex container id from a cgroup path:
// systemd driver "…/docker-<id>.scope", cgroupfs "/docker/<id>", podman
// "libpod-<id>.scope", containerd "cri-containerd-<id>.scope" and nested
// paths such as "/docker/<id>/init". It returns "" when none is present.
func ContainerIDFromCgroup(p string) string {
	for len(p) > 0 {
		var seg string
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			seg, p = p[i+1:], p[:i]
		} else {
			seg, p = p, ""
		}
		seg = strings.TrimSuffix(seg, ".scope")
		if i := strings.LastIndexByte(seg, '-'); i >= 0 {
			seg = seg[i+1:]
		}
		if isHex64(seg) {
			return seg
		}
	}
	return ""
}

// UnitFromCgroup returns the outermost systemd unit ("nginx.service",
// "postgresql@16-main.service") in a cgroup path, or "".
func UnitFromCgroup(p string) string {
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if strings.HasSuffix(seg, ".service") {
			return seg
		}
	}
	return ""
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// IsCgroupV2 reports whether cgroupRoot (host root + /sys/fs/cgroup) is a
// cgroup v2 (unified) hierarchy.
func IsCgroupV2(cgroupRoot string) bool {
	_, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers"))
	return err == nil
}

// ContainerCgroupDir finds the cgroup v2 directory of a container. It tries
// the path from /proc/<pid>/cgroup first (when it is absolute in the host's
// namespace) and then the well-known layouts by container id, so it also
// works when the agent itself runs in a private cgroup namespace.
func ContainerCgroupDir(cgroupRoot, procCgroupPath, fullID string) string {
	// The /proc path is only trusted when it names the container: a
	// process in the agent's own cgroup namespace (the agent container
	// itself) reads "0::/", which would otherwise resolve to the root.
	if procCgroupPath != "" && !strings.HasPrefix(procCgroupPath, "/..") && (fullID == "" || strings.Contains(procCgroupPath, fullID)) {
		d := filepath.Join(cgroupRoot, procCgroupPath)
		if procCgroupPath != "/" && hasFile(d, "cgroup.procs") {
			return d
		}
	}
	if fullID == "" {
		return ""
	}
	candidates := [...]string{
		"system.slice/docker-" + fullID + ".scope",
		"docker/" + fullID,
		"machine.slice/libpod-" + fullID + ".scope",
		"system.slice/libpod-" + fullID + ".scope",
		"docker.slice/docker-" + fullID + ".scope",
	}
	for _, c := range candidates {
		d := filepath.Join(cgroupRoot, c)
		if hasFile(d, "cgroup.procs") {
			return d
		}
	}
	// The tail of a "/../x/docker-<id>.scope" path is still meaningful.
	if procCgroupPath != "" && strings.Contains(procCgroupPath, fullID) {
		segs := strings.Split(strings.Trim(procCgroupPath, "/"), "/")
		for i := range segs {
			if segs[i] == ".." {
				continue
			}
			d := filepath.Join(append([]string{cgroupRoot}, segs[i:]...)...)
			if hasFile(d, "cgroup.procs") {
				return d
			}
		}
	}
	return ""
}

func hasFile(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}
