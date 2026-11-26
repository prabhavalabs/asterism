package showcase

import (
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// HostSnapshotter is the subset of a host collector the showcase identity wraps.
type HostSnapshotter interface {
	HostSnapshot() *asterismv1.HostInfo
}

// HostIdentity keeps the live vitals of the machine running the showcase (CPU,
// memory, disk, network) but replaces its identity with a fictional Linux VPS,
// so demos and screenshots never reveal the real hostname, OS, or CPU model.
type HostIdentity struct {
	inner HostSnapshotter
}

// WithDemoIdentity wraps a host collector for showcase mode.
func WithDemoIdentity(inner HostSnapshotter) *HostIdentity {
	return &HostIdentity{inner: inner}
}

// HostSnapshot returns the wrapped snapshot with demo identity fields.
func (h *HostIdentity) HostSnapshot() *asterismv1.HostInfo {
	s := h.inner.HostSnapshot()
	s.Hostname = "vps-demo-1"
	s.Os = "ubuntu 24.04"
	s.Kernel = "6.8.0-49-generic"
	s.Arch = "x86_64"
	s.CpuModel = "AMD EPYC Processor"
	return s
}
