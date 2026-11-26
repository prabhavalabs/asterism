package dockercli

import (
	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Snapshot is a point-in-time, caller-owned copy of Docker inventory.
type Snapshot struct {
	Engine        *asterismv1.DockerEngine
	Containers    []*asterismv1.Container
	Images        []*asterismv1.DockerImage
	Volumes       []*asterismv1.DockerVolume
	Networks      []*asterismv1.DockerNetwork
	DiskUsage     *asterismv1.DockerDiskUsage
	CollectedAtMs int64
}

// CloneAll deep-copies every message in a slice so collectors can hand data to
// callers without sharing pointers their poll loops later mutate under a lock.
// A nil slice stays nil.
func CloneAll[T proto.Message](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	for i, m := range in {
		out[i] = proto.Clone(m).(T)
	}
	return out
}
