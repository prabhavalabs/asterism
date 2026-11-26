package showcase

import (
	"testing"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

type fakeHost struct{ info *asterismv1.HostInfo }

func (f fakeHost) HostSnapshot() *asterismv1.HostInfo { return f.info }

func TestWithDemoIdentityMasksIdentityKeepsVitals(t *testing.T) {
	inner := fakeHost{info: &asterismv1.HostInfo{
		Hostname: "real-laptop", Os: "darwin 26.1", Kernel: "25.5.0",
		CpuModel: "Apple M4 Pro", Cores: 14, MemTotalBytes: 48 << 30,
	}}
	got := WithDemoIdentity(inner).HostSnapshot()
	for field, v := range map[string]string{"hostname": got.Hostname, "os": got.Os, "kernel": got.Kernel, "cpu": got.CpuModel} {
		switch v {
		case "real-laptop", "darwin 26.1", "25.5.0", "Apple M4 Pro", "":
			t.Errorf("%s = %q, want a demo value", field, v)
		}
	}
	if got.Cores != 14 || got.MemTotalBytes != 48<<30 {
		t.Errorf("vitals changed: cores=%d mem=%d", got.Cores, got.MemTotalBytes)
	}
}
