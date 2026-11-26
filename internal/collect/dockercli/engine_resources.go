package dockercli

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// EngineInfoFromJSON converts GET /info.
func EngineInfoFromJSON(raw []byte, now time.Time) (*asterismv1.DockerEngine, error) {
	var d struct {
		ServerVersion     string `json:"ServerVersion"`
		OperatingSystem   string `json:"OperatingSystem"`
		KernelVersion     string `json:"KernelVersion"`
		Architecture      string `json:"Architecture"`
		Driver            string `json:"Driver"`
		CgroupDriver      string `json:"CgroupDriver"`
		CgroupVersion     string `json:"CgroupVersion"`
		LoggingDriver     string `json:"LoggingDriver"`
		DockerRootDir     string `json:"DockerRootDir"`
		NCPU              int    `json:"NCPU"`
		MemTotal          int64  `json:"MemTotal"`
		Containers        int    `json:"Containers"`
		ContainersRunning int    `json:"ContainersRunning"`
		ContainersPaused  int    `json:"ContainersPaused"`
		ContainersStopped int    `json:"ContainersStopped"`
		Images            int    `json:"Images"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("decode info: %w", err)
	}
	return &asterismv1.DockerEngine{
		Available:         d.ServerVersion != "",
		ServerVersion:     d.ServerVersion,
		OperatingSystem:   d.OperatingSystem,
		KernelVersion:     d.KernelVersion,
		Architecture:      d.Architecture,
		StorageDriver:     d.Driver,
		CgroupDriver:      d.CgroupDriver,
		CgroupVersion:     d.CgroupVersion,
		LoggingDriver:     d.LoggingDriver,
		DockerRootDir:     d.DockerRootDir,
		Ncpu:              uint32(max(d.NCPU, 0)),
		MemTotalBytes:     uint64(max(d.MemTotal, 0)),
		Containers:        uint32(max(d.Containers, 0)),
		ContainersRunning: uint32(max(d.ContainersRunning, 0)),
		ContainersPaused:  uint32(max(d.ContainersPaused, 0)),
		ContainersStopped: uint32(max(d.ContainersStopped, 0)),
		Images:            uint32(max(d.Images, 0)),
		CollectedAtMs:     now.UnixMilli(),
	}, nil
}

// ImagesFromEngine converts GET /images/json. Image labels are not kept.
func ImagesFromEngine(raw []byte) ([]*asterismv1.DockerImage, error) {
	var docs []struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Created  int64    `json:"Created"`
		Size     int64    `json:"Size"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	res := make([]*asterismv1.DockerImage, 0, len(docs))
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if d.ID == "" || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		img := &asterismv1.DockerImage{Id: ShortImageID(d.ID), SizeBytes: uint64(max(d.Size, 0))}
		if d.Created > 0 {
			img.CreatedAtMs = d.Created * 1000
		}
		for _, t := range d.RepoTags {
			if t != "" && t != "<none>:<none>" {
				img.RepoTags = append(img.RepoTags, t)
			}
		}
		sort.Strings(img.RepoTags)
		if len(img.RepoTags) == 0 {
			img.Dangling = true
			img.Repository, img.Tag = "<none>", "<none>"
		} else {
			img.Repository, img.Tag = SplitImageRef(img.RepoTags[0])
		}
		res = append(res, img)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Dangling != res[j].Dangling {
			return !res[i].Dangling
		}
		if res[i].Repository != res[j].Repository {
			return res[i].Repository < res[j].Repository
		}
		if res[i].Tag != res[j].Tag {
			return res[i].Tag < res[j].Tag
		}
		return res[i].Id < res[j].Id
	})
	return res, nil
}

// VolumesFromEngine converts GET /volumes. Volume options (which may carry
// credentials for remote drivers) are never decoded. Sizes start unknown
// (-1) unless the response includes UsageData.
func VolumesFromEngine(raw []byte) ([]*asterismv1.DockerVolume, error) {
	var d struct {
		Volumes []engineVolume `json:"Volumes"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("decode volumes: %w", err)
	}
	res := make([]*asterismv1.DockerVolume, 0, len(d.Volumes))
	for _, v := range d.Volumes {
		if v.Name == "" {
			continue
		}
		res = append(res, v.toProto())
	}
	sortVolumes(res)
	return res, nil
}

type engineVolume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Scope      string            `json:"Scope"`
	CreatedAt  string            `json:"CreatedAt"`
	Mountpoint string            `json:"Mountpoint"`
	Labels     map[string]string `json:"Labels"`
	UsageData  *struct {
		RefCount int64 `json:"RefCount"`
		Size     int64 `json:"Size"`
	} `json:"UsageData"`
}

func (v engineVolume) toProto() *asterismv1.DockerVolume {
	size := int64(-1)
	if v.UsageData != nil && v.UsageData.Size >= 0 {
		size = v.UsageData.Size
	}
	return &asterismv1.DockerVolume{
		Name:           v.Name,
		Driver:         v.Driver,
		Scope:          v.Scope,
		Mountpoint:     v.Mountpoint,
		CreatedAtMs:    parseDockerTime(v.CreatedAt),
		SizeBytes:      size,
		ComposeProject: v.Labels[LabelComposeProject],
		Anonymous:      hex64Re.MatchString(v.Name),
	}
}

func sortVolumes(res []*asterismv1.DockerVolume) {
	sort.Slice(res, func(i, j int) bool {
		if res[i].Anonymous != res[j].Anonymous {
			return !res[i].Anonymous
		}
		return res[i].Name < res[j].Name
	})
}

// NetworksFromEngine converts GET /networks. The list endpoint does not
// include attachments; callers fill Containers from container inventory
// (LinkNetworks). Network options are never decoded.
func NetworksFromEngine(raw []byte) ([]*asterismv1.DockerNetwork, error) {
	var docs []struct {
		ID         string `json:"Id"`
		Name       string `json:"Name"`
		Driver     string `json:"Driver"`
		Scope      string `json:"Scope"`
		Internal   bool   `json:"Internal"`
		Attachable bool   `json:"Attachable"`
		EnableIPv6 bool   `json:"EnableIPv6"`
		Created    string `json:"Created"`
		IPAM       struct {
			Config []struct {
				Subnet  string `json:"Subnet"`
				Gateway string `json:"Gateway"`
			} `json:"Config"`
		} `json:"IPAM"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	res := make([]*asterismv1.DockerNetwork, 0, len(docs))
	for _, d := range docs {
		if d.Name == "" {
			continue
		}
		n := &asterismv1.DockerNetwork{
			Id:             ShortID(d.ID),
			Name:           d.Name,
			Driver:         d.Driver,
			Scope:          d.Scope,
			Internal:       d.Internal,
			Attachable:     d.Attachable,
			Ipv6:           d.EnableIPv6,
			CreatedAtMs:    parseDockerTime(d.Created),
			ComposeProject: d.Labels[LabelComposeProject],
			Builtin:        d.Name == "bridge" || d.Name == "host" || d.Name == "none",
		}
		for _, cfg := range d.IPAM.Config {
			if cfg.Subnet != "" {
				n.Subnets = append(n.Subnets, cfg.Subnet)
			}
			if cfg.Gateway != "" {
				n.Gateways = append(n.Gateways, cfg.Gateway)
			}
		}
		res = append(res, n)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Builtin != res[j].Builtin {
			return !res[i].Builtin
		}
		return res[i].Name < res[j].Name
	})
	return res, nil
}

// LinkNetworks fills each network's attachments from container inventory
// (the Engine list endpoint omits them).
func LinkNetworks(containers []*asterismv1.Container, networks []*asterismv1.DockerNetwork) {
	byName := make(map[string]*asterismv1.DockerNetwork, len(networks))
	for _, n := range networks {
		n.Containers = nil
		byName[n.Name] = n
	}
	for _, c := range containers {
		for _, cn := range c.Networks {
			if n, ok := byName[cn.Name]; ok {
				n.Containers = append(n.Containers, &asterismv1.NetworkAttachment{
					ContainerId: c.ContainerId,
					Name:        c.Name,
					Ipv4Address: cn.IpAddress,
				})
			}
		}
	}
	for _, n := range networks {
		sort.Slice(n.Containers, func(i, j int) bool { return n.Containers[i].Name < n.Containers[j].Name })
	}
}

// DiskUsageFromEngine converts GET /system/df into the four `docker system
// df` categories (computed the way the Docker CLI does) and per-volume sizes.
func DiskUsageFromEngine(raw []byte, now time.Time) (*asterismv1.DockerDiskUsage, map[string]int64, error) {
	var d struct {
		LayersSize int64 `json:"LayersSize"`
		Images     []struct {
			Containers int64 `json:"Containers"`
			Size       int64 `json:"Size"`
			SharedSize int64 `json:"SharedSize"`
		} `json:"Images"`
		Containers []struct {
			SizeRw int64  `json:"SizeRw"`
			State  string `json:"State"`
		} `json:"Containers"`
		Volumes    []engineVolume `json:"Volumes"`
		BuildCache []struct {
			Size   int64 `json:"Size"`
			InUse  bool  `json:"InUse"`
			Shared bool  `json:"Shared"`
		} `json:"BuildCache"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, nil, fmt.Errorf("decode system df: %w", err)
	}
	pos := func(v int64) uint64 { return uint64(max(v, 0)) }
	res := &asterismv1.DockerDiskUsage{CollectedAtMs: now.UnixMilli()}
	add := func(c *asterismv1.DiskUsageCategory) {
		if c.ReclaimableBytes > c.SizeBytes {
			c.ReclaimableBytes = c.SizeBytes
		}
		res.Categories = append(res.Categories, c)
		res.TotalSizeBytes += c.SizeBytes
		res.TotalReclaimableBytes += c.ReclaimableBytes
	}

	img := &asterismv1.DiskUsageCategory{Kind: "images", TotalCount: uint32(len(d.Images)), SizeBytes: pos(d.LayersSize)}
	var used int64
	for _, i := range d.Images {
		if i.Containers > 0 {
			img.ActiveCount++
			if i.Size >= 0 && i.SharedSize >= 0 {
				used += i.Size - i.SharedSize
			}
		}
	}
	img.ReclaimableBytes = pos(d.LayersSize - used)
	add(img)

	ctr := &asterismv1.DiskUsageCategory{Kind: "containers", TotalCount: uint32(len(d.Containers))}
	for _, c := range d.Containers {
		ctr.SizeBytes += pos(c.SizeRw)
		if c.State == "running" || c.State == "paused" || c.State == "restarting" {
			ctr.ActiveCount++
		} else {
			ctr.ReclaimableBytes += pos(c.SizeRw)
		}
	}
	add(ctr)

	sizes := make(map[string]int64, len(d.Volumes))
	vol := &asterismv1.DiskUsageCategory{Kind: "volumes", TotalCount: uint32(len(d.Volumes))}
	for _, v := range d.Volumes {
		if v.UsageData == nil || v.UsageData.Size < 0 {
			continue
		}
		sizes[v.Name] = v.UsageData.Size
		vol.SizeBytes += uint64(v.UsageData.Size)
		if v.UsageData.RefCount > 0 {
			vol.ActiveCount++
		} else {
			vol.ReclaimableBytes += uint64(v.UsageData.Size)
		}
	}
	add(vol)

	bc := &asterismv1.DiskUsageCategory{Kind: "build_cache", TotalCount: uint32(len(d.BuildCache))}
	for _, b := range d.BuildCache {
		if b.InUse {
			bc.ActiveCount++
		}
		if !b.Shared {
			bc.SizeBytes += pos(b.Size)
		}
		if !b.InUse && !b.Shared {
			bc.ReclaimableBytes += pos(b.Size)
		}
	}
	add(bc)
	return res, sizes, nil
}

// ApplyVolumeSizes sets SizeBytes on volumes found in sizes.
func ApplyVolumeSizes(volumes []*asterismv1.DockerVolume, sizes map[string]int64) {
	for _, v := range volumes {
		if s, ok := sizes[v.Name]; ok {
			v.SizeBytes = s
		}
	}
}
