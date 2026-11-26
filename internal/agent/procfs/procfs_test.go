package procfs

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	idA = "317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25"
	idB = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

func fixture(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", p))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseStat(t *testing.T) {
	cpu, btime, ok := ParseStat(fixture(t, "root/proc/stat"))
	if !ok || btime != 1795500000 {
		t.Fatalf("ok=%v btime=%d", ok, btime)
	}
	if cpu.User != 10132153 || cpu.Idle != 46828483 || cpu.Steal != 1000 {
		t.Fatalf("cpu = %+v", cpu)
	}
	if got, want := cpu.Total(), uint64(10132153+290696+3084719+46828483+16683+0+25195+1000); got != want {
		t.Fatalf("total %d want %d", got, want)
	}
}

func TestParseMeminfo(t *testing.T) {
	m, ok := ParseMeminfo(fixture(t, "root/proc/meminfo"))
	if !ok || m.MemTotal != 8130000*1024 || !m.HasAvailable {
		t.Fatalf("%+v", m)
	}
	if m.Used() != (8130000-5130000)*1024 {
		t.Fatalf("used %d", m.Used())
	}
	if m.SwapUsed() != (2097148-1048574)*1024 {
		t.Fatalf("swap %d", m.SwapUsed())
	}
	old := Meminfo{MemTotal: 100, MemFree: 10, Buffers: 5, Cached: 20, SReclaimable: 5}
	if old.Used() != 60 {
		t.Fatalf("pre-3.14 used %d", old.Used())
	}
}

func TestParseLoadavgUptime(t *testing.T) {
	l1, l5, l15, ok := ParseLoadavg(fixture(t, "root/proc/loadavg"))
	if !ok || l1 != 0.52 || l5 != 0.58 || math.Abs(l15-0.59) > 1e-9 {
		t.Fatalf("%v %v %v %v", l1, l5, l15, ok)
	}
	up, ok := ParseUptime(fixture(t, "root/proc/uptime"))
	if !ok || up != 57600.25 {
		t.Fatalf("uptime %v", up)
	}
}

func TestParseNetDev(t *testing.T) {
	got := map[string][2]uint64{}
	ParseNetDev(fixture(t, "root/proc/1/net/dev"), func(n []byte, rx, tx uint64) { got[string(n)] = [2]uint64{rx, tx} })
	want := map[string][2]uint64{
		"lo": {9000000, 9000000}, "eth0": {4000000000, 1000000000}, "docker0": {500000, 700000}, "veth1a2b3c": {123456, 654321},
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v want %v", k, got[k], v)
		}
	}
	rx, tx, ok := SumNetDev(fixture(t, "root/proc/4242/net/dev"))
	if !ok || rx != 2000000 || tx != 300000 {
		t.Fatalf("container sum %d %d %v", rx, tx, ok)
	}
}

func TestParseDiskstats(t *testing.T) {
	var rd, wr uint64
	var names []string
	ParseDiskstats(fixture(t, "root/proc/diskstats"), func(n []byte, r, w uint64) {
		if IsWholeDiskName(n) {
			names = append(names, string(n))
			rd += r
			wr += w
		}
	})
	if strings.Join(names, ",") != "sda,nvme0n1" || rd != 1600000 || wr != 3200000 {
		t.Fatalf("names=%v rd=%d wr=%d", names, rd, wr)
	}
}

func TestIsWholeDiskName(t *testing.T) {
	for name, want := range map[string]bool{
		"sda": true, "sda1": false, "vdb": true, "xvda": true, "xvda1": false, "nvme0n1": true, "nvme0n1p2": false,
		"mmcblk0": true, "mmcblk0p1": false, "loop0": false, "dm-0": false, "ram0": false, "zram0": false,
	} {
		if got := IsWholeDiskName([]byte(name)); got != want {
			t.Errorf("%s: %v want %v", name, got, want)
		}
	}
}

func TestCgroupFiles(t *testing.T) {
	dir := "root/sys/fs/cgroup/system.slice/docker-" + idA + ".scope/"
	if v, ok := CPUUsageUsec(fixture(t, dir+"cpu.stat")); !ok || v != 1500000 {
		t.Fatalf("usage %d", v)
	}
	if v, ok := InactiveFile(fixture(t, dir+"memory.stat")); !ok || v != 20971520 {
		t.Fatalf("inactive %d", v)
	}
	if v, ok := Single(fixture(t, dir+"memory.current")); !ok || v != 104857600 {
		t.Fatalf("current %d", v)
	}
	if _, ok := Single(fixture(t, dir+"memory.max")); ok {
		t.Fatal(`"max" must not parse`)
	}
	r, w := ParseIOStat(fixture(t, dir+"io.stat"))
	if r != 2097152 || w != 2097152 {
		t.Fatalf("io %d %d", r, w)
	}
	if v, ok := OOMKills(fixture(t, dir+"memory.events")); !ok || v != 0 {
		t.Fatalf("oom %d %v", v, ok)
	}
	if v, ok := OOMKills(fixture(t, "root-cgroupfs/sys/fs/cgroup/docker/"+idB+"/memory.events")); !ok || v != 1 {
		t.Fatalf("oom %d", v)
	}
	// key must match a whole word
	if _, ok := KeyValue([]byte("usage_usec_total 5\n"), []byte("usage_usec")); ok {
		t.Fatal("prefix match")
	}
}

func TestContainerIDFromCgroup(t *testing.T) {
	cases := map[string]string{
		"/system.slice/docker-" + idA + ".scope":                    idA,
		"/docker/" + idB:                                            idB,
		"/../../docker/" + idB:                                      idB,
		"/docker/" + idB + "/init":                                  idB,
		"/machine.slice/libpod-" + idA + ".scope/container":         idA,
		"/kubepods/burstable/pod1/cri-containerd-" + idA + ".scope": idA,
		"/system.slice/nginx.service":                               "",
		"/user.slice/user-1000.slice":                               "",
	}
	for in, want := range cases {
		if got := ContainerIDFromCgroup(in); got != want {
			t.Errorf("%s: %q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"/system.slice/nginx.service":                "nginx.service",
		"/system.slice/postgresql@16-main.service/x": "postgresql@16-main.service",
		"/../system.slice/caddy.service":             "caddy.service",
		"/system.slice/docker-" + idA + ".scope":     "",
	} {
		if got := UnitFromCgroup(in); got != want {
			t.Errorf("%s: %q want %q", in, got, want)
		}
	}
}

func TestContainerCgroupDir(t *testing.T) {
	sys := filepath.Join("testdata", "root", CgroupRoot)
	cfs := filepath.Join("testdata", "root-cgroupfs", CgroupRoot)
	cases := []struct {
		name, root, procPath, id, want string
	}{
		{"systemd from /proc", sys, "/system.slice/docker-" + idA + ".scope", idA, filepath.Join(sys, "system.slice/docker-"+idA+".scope")},
		{"systemd by id (private cgroupns)", sys, "/../docker-" + idA + ".scope", idA, filepath.Join(sys, "system.slice/docker-"+idA+".scope")},
		{"cgroupfs by id", cfs, "/../../docker/" + idB, idB, filepath.Join(cfs, "docker", idB)},
		{"cgroupfs no proc path", cfs, "", idB, filepath.Join(cfs, "docker", idB)},
		{"missing", cfs, "", idA, ""},
		{"own namespace root is never the container", sys, "/", idB, ""},
		{"own namespace root, found by id", cfs, "/", idB, filepath.Join(cfs, "docker", idB)},
	}
	for _, c := range cases {
		if got := ContainerCgroupDir(c.root, c.procPath, c.id); got != c.want {
			t.Errorf("%s: %q want %q", c.name, got, c.want)
		}
	}
	if !IsCgroupV2(sys) || IsCgroupV2("testdata") {
		t.Fatal("IsCgroupV2")
	}
}

func TestParsePidStat(t *testing.T) {
	s, ok := ParsePidStat(fixture(t, "root/proc/777/stat"))
	if !ok || string(s.Comm) != "my (weird) app" || s.State != 'S' || s.PPid != 1 || s.UTime != 4200 || s.STime != 1300 || s.StartTime != 6000000 || s.RSSPages != 5120 {
		t.Fatalf("%+v %q", s, s.Comm)
	}
	if _, ok := ParsePidStat([]byte("garbage")); ok {
		t.Fatal("garbage parsed")
	}
	uid, ok := ParseStatusUID(fixture(t, "root/proc/777/status"))
	if !ok || uid != 1001 {
		t.Fatalf("uid %d", uid)
	}
	args := SplitCmdline(fixture(t, "root/proc/777/cmdline"))
	if len(args) != 4 || args[0] != "/usr/bin/node" || args[3] != "s3cr3t-value" {
		t.Fatalf("%q", args)
	}
	pids, err := ListPids("testdata/root/proc")
	if err != nil || len(pids) != 6 {
		t.Fatalf("pids %v %v", pids, err)
	}
}

func TestParseSockets(t *testing.T) {
	type row struct {
		addr  string
		state uint8
		inode uint64
	}
	collect := func(p string) []row {
		var out []row
		ParseSocketsBytes(fixture(t, p), func(s Socket) {
			out = append(out, row{s.Local.String() + ":" + itoa(int(s.Port)), s.State, s.Inode})
		})
		return out
	}
	tcp := collect("root/proc/1/net/tcp")
	want := []row{
		{"127.0.0.1:8080", TCPListen, 11111}, {"0.0.0.0:22", TCPListen, 22222}, {"0.0.0.0:18080", TCPListen, 33333},
		{"127.0.0.1:8080", 1, 44444}, {"192.168.100.1:80", TCPListen, 55555},
	}
	if len(tcp) != len(want) {
		t.Fatalf("tcp rows %v", tcp)
	}
	for i := range want {
		if tcp[i] != want[i] {
			t.Errorf("tcp[%d] = %v want %v", i, tcp[i], want[i])
		}
	}
	tcp6 := collect("root/proc/1/net/tcp6")
	want6 := []row{{":::80", TCPListen, 66666}, {"127.0.0.1:5432", TCPListen, 77777}, {"::1:6379", TCPListen, 88888}}
	for i := range want6 {
		if i >= len(tcp6) || tcp6[i] != want6[i] {
			t.Errorf("tcp6[%d] = %v want %v (IPv4-mapped must unmap)", i, tcp6, want6[i])
		}
	}
	udp := collect("root/proc/1/net/udp")
	if len(udp) != 2 || udp[0] != (row{"127.0.0.53:53", UDPClose, 99999}) {
		t.Fatalf("udp %v", udp)
	}
	if len(collect("root/proc/1/net/udp6")) != 0 {
		t.Fatal("header-only table")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

func TestDeltaWrap(t *testing.T) {
	cases := []struct {
		prev, cur, want uint64
	}{
		{100, 150, 50},
		{150, 150, 0},
		{4294967000, 200, 496},       // 32-bit wrap
		{3000000000, 10, 1294967306}, // 32-bit wrap, large but plausible (< 2^31)
		{1 << 40, 5, 0},              // 64-bit counter went back: reset
		{2000000000, 1000000000, 0},  // "wrap" too large to be plausible: reset
		{math.MaxUint64 - 5, 10, 0},  // never fabricate a 64-bit wrap
	}
	for _, c := range cases {
		if got := Delta(c.prev, c.cur); got != c.want {
			t.Errorf("Delta(%d,%d)=%d want %d", c.prev, c.cur, got, c.want)
		}
	}
	var ctr Counter
	t0 := time.Unix(1000, 0)
	if _, ok := ctr.Observe(t0, 4294967000); ok {
		t.Fatal("first sample must not yield a rate")
	}
	r, ok := ctr.Observe(t0.Add(2*time.Second), 200)
	if !ok || r != 248 {
		t.Fatalf("wrapped rate %v %v", r, ok)
	}
}

func TestFileReread(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(p, []byte("100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 64)
	b, err := f.Read(buf)
	if v, _ := Single(b); err != nil || v != 100 {
		t.Fatalf("%v %v", v, err)
	}
	if err := os.WriteFile(p, []byte("250\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err = f.Read(buf)
	if v, _ := Single(b); err != nil || v != 250 {
		t.Fatalf("reread %v %v", v, err)
	}
	if _, err := f.Read(make([]byte, 2)); err != ErrTruncated {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	_ = f.Close()
	if _, err := f.Read(buf); err == nil {
		t.Fatal("read after close")
	}
}

func TestParseHelpers(t *testing.T) {
	if v, ok := ParseDecimal([]byte("12.5")); !ok || v != 12.5 {
		t.Fatal(v)
	}
	for _, bad := range []string{"", ".", "1.2.3", "-1", "1e3"} {
		if _, ok := ParseDecimal([]byte(bad)); ok {
			t.Errorf("ParseDecimal(%q) ok", bad)
		}
	}
	if _, ok := ParseUint([]byte("18446744073709551616")); ok {
		t.Fatal("overflow accepted")
	}
	if v, ok := ParseInt([]byte("-12")); !ok || v != -12 {
		t.Fatal(v)
	}
	if v, ok := ParseHex([]byte("1F90")); !ok || v != 8080 {
		t.Fatal(v)
	}
	if string(Field([]byte("  a  bb ccc "), 2)) != "ccc" || Field([]byte("a"), 3) != nil {
		t.Fatal("Field")
	}
}

func BenchmarkParseStat(b *testing.B) {
	buf := fixture(b, "root/proc/stat")
	b.ReportAllocs()
	for b.Loop() {
		ParseStat(buf)
	}
}

func BenchmarkCgroupFiles(b *testing.B) {
	dir := "root/sys/fs/cgroup/system.slice/docker-" + idA + ".scope/"
	cpu, mem, io := fixture(b, dir+"cpu.stat"), fixture(b, dir+"memory.stat"), fixture(b, dir+"io.stat")
	b.ReportAllocs()
	for b.Loop() {
		CPUUsageUsec(cpu)
		InactiveFile(mem)
		ParseIOStat(io)
	}
}
