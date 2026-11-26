package sshbridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const (
	resourcesInterval = 60 * time.Second
	// docker system df walks image layers and volume directories; keep it rare.
	diskUsageEvery = 5 // resource polls (~5 minutes)
)

// runResourcesLoop polls engine info, images, volumes, networks, and disk usage
// on its own goroutine so slow docker calls never delay the 5 s metrics loop.
func (c *Collector) runResourcesLoop(ctx context.Context) {
	t := time.NewTicker(resourcesInterval)
	defer t.Stop()
	for n := 0; ; n++ {
		if err := c.collectResources(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("ssh docker resources poll failed", "target", c.target, "err", err)
		}
		if n%diskUsageEvery == 0 {
			if err := c.collectDiskUsage(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("ssh docker disk usage poll failed", "target", c.target, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) collectResources(ctx context.Context) error {
	out, err := c.runSSHTimeout(ctx, dockercli.ResourcesScript, 30*time.Second)
	if err != nil {
		return err
	}
	sections := splitSections(out)
	now := time.Now()
	engine := dockercli.ParseInfo(sections["INFO"], now)
	images := dockercli.ParseImages(sections["IMAGES"])
	volumes := dockercli.ParseVolumes(sections["VOLUMES"])
	networks := dockercli.ParseNetworks(sections["NETWORKS"])

	c.mu.Lock()
	c.engine = engine
	c.images = images
	c.volumes = volumes
	c.networks = networks
	c.mu.Unlock()
	return nil
}

func (c *Collector) collectDiskUsage(ctx context.Context) error {
	out, err := c.runSSHTimeout(ctx, dockercli.DiskUsageScript, 90*time.Second)
	if err != nil {
		return err
	}
	sections := splitSections(out)
	du := dockercli.ParseDiskUsage(sections["DF"], time.Now())
	sizes := dockercli.ParseVolumeSizes(sections["DFV"])
	c.mu.Lock()
	c.diskUsage = du
	if sizes != nil {
		c.volumeSizes = sizes
	}
	c.mu.Unlock()
	return nil
}

// ownNetns reports whether a container has its own network namespace, so
// /proc/<pid>/net/dev reflects only that container's traffic.
func ownNetns(mode string) bool {
	return mode != "host" && !strings.HasPrefix(mode, "container:") && mode != "none"
}

// DockerSnapshot implements the service DockerProvider interface.
func (c *Collector) DockerSnapshot() dockercli.Snapshot {
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()

	snap := dockercli.Snapshot{CollectedAtMs: c.dockerSyncMs}
	if c.engine != nil {
		snap.Engine = proto.Clone(c.engine).(*asterismv1.DockerEngine)
	} else if c.dockerSyncMs == 0 {
		snap.Engine = &asterismv1.DockerEngine{Available: false, UnavailableReason: "waiting for the first Docker poll over SSH"}
	} else {
		snap.Engine = &asterismv1.DockerEngine{Available: len(c.containers) > 0}
	}
	for _, ctr := range c.containers {
		cl := proto.Clone(ctr).(*asterismv1.Container)
		if st, ok := c.ctrStats[cl.ContainerId]; ok && (cl.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING || cl.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED) {
			cl.Stats = proto.Clone(st).(*asterismv1.ContainerStats)
		}
		// Uptime and status age move between inventory polls.
		cl.UptimeSeconds = 0
		if (cl.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING || cl.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED) && cl.StartedAtMs > 0 && now.UnixMilli() > cl.StartedAtMs {
			cl.UptimeSeconds = uint64((now.UnixMilli() - cl.StartedAtMs) / 1000)
		}
		cl.Status = dockercli.HumanStatus(cl, now)
		snap.Containers = append(snap.Containers, cl)
	}
	for _, img := range c.images {
		snap.Images = append(snap.Images, proto.Clone(img).(*asterismv1.DockerImage))
	}
	for _, v := range c.volumes {
		vc := proto.Clone(v).(*asterismv1.DockerVolume)
		if sz, ok := c.volumeSizes[vc.Name]; ok {
			vc.SizeBytes = sz
		}
		snap.Volumes = append(snap.Volumes, vc)
	}
	for _, n := range c.networks {
		snap.Networks = append(snap.Networks, proto.Clone(n).(*asterismv1.DockerNetwork))
	}
	if c.diskUsage != nil {
		snap.DiskUsage = proto.Clone(c.diskUsage).(*asterismv1.DockerDiskUsage)
	}
	dockercli.LinkUsage(snap.Containers, snap.Images, snap.Volumes)
	return snap
}

// SupportedContainerActions implements the service ContainerActuator interface.
func (c *Collector) SupportedContainerActions() []asterismv1.ContainerAction {
	return []asterismv1.ContainerAction{
		asterismv1.ContainerAction_CONTAINER_ACTION_START,
		asterismv1.ContainerAction_CONTAINER_ACTION_STOP,
		asterismv1.ContainerAction_CONTAINER_ACTION_RESTART,
		asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE,
		asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE,
	}
}

// ActionCommand builds the remote docker command for an action. containerID must
// be a validated hex id; it is additionally single-quoted.
func ActionCommand(action asterismv1.ContainerAction, containerID string, timeout time.Duration) (string, error) {
	if !dockercli.ValidContainerID(containerID) {
		return "", fmt.Errorf("refusing action on invalid container id %q", dockercli.Truncate(containerID, 80))
	}
	secs := int(timeout.Seconds())
	if secs < 0 {
		secs = 0
	}
	q := dockercli.ShellQuote(containerID)
	switch action {
	case asterismv1.ContainerAction_CONTAINER_ACTION_START:
		return "docker start " + q, nil
	case asterismv1.ContainerAction_CONTAINER_ACTION_STOP:
		return fmt.Sprintf("docker stop -t %d %s", secs, q), nil
	case asterismv1.ContainerAction_CONTAINER_ACTION_RESTART:
		return fmt.Sprintf("docker restart -t %d %s", secs, q), nil
	case asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE:
		return "docker pause " + q, nil
	case asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE:
		return "docker unpause " + q, nil
	default:
		return "", fmt.Errorf("unsupported action %v", action)
	}
}

// ContainerAction runs the docker command over SSH, re-inspects the container,
// updates the cached inventory, and schedules a full topology resync.
func (c *Collector) ContainerAction(ctx context.Context, action asterismv1.ContainerAction, containerID string, timeout time.Duration) (*asterismv1.Container, error) {
	cmd, err := ActionCommand(action, containerID, timeout)
	if err != nil {
		return nil, err
	}
	if _, err := c.runSSHTimeout(ctx, cmd, timeout+30*time.Second); err != nil {
		return nil, err
	}
	inspectCmd, err := dockercli.InspectCommand(containerID)
	if err != nil {
		return nil, err
	}
	out, err := c.runSSHTimeout(ctx, inspectCmd, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("action succeeded but re-inspect failed: %w", err)
	}
	fresh := dockercli.ParseInspect(out, time.Now())
	if len(fresh) == 0 {
		return nil, fmt.Errorf("action succeeded but container %s could not be re-inspected", containerID)
	}
	result := c.applyContainerUpdate(fresh[0])

	select {
	case c.refresh <- struct{}{}:
	default:
	}
	return result, nil
}

// applyContainerUpdate stores a freshly inspected container in the cached
// inventory, mirrors its state onto the topology workload, and returns a
// caller-owned copy.
func (c *Collector) applyContainerUpdate(updated *asterismv1.Container) *asterismv1.Container {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ctr := range c.containers {
		if ctr.ContainerId == updated.ContainerId {
			updated.Role = ctr.Role
			c.containers[i] = updated
		}
	}
	if wl, ok := c.workloads[updated.Id]; ok {
		health, state := parseDockerStatus(updated.Status)
		wl.State = state
		wl.Health = health
		wl.StartedAtMs = updated.StartedAtMs
		wl.RestartCount = updated.RestartCount
	}
	if updated.State != asterismv1.ContainerState_CONTAINER_STATE_RUNNING {
		delete(c.ctrStats, updated.ContainerId)
	}
	// Clone while still holding the lock: updated is now shared collector state.
	return proto.Clone(updated).(*asterismv1.Container)
}
