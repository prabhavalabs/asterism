package dockercli

import (
	"bufio"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const infoTemplate = `{"ServerVersion":{{json .ServerVersion}},"OperatingSystem":{{json .OperatingSystem}},"KernelVersion":{{json .KernelVersion}},"Architecture":{{json .Architecture}},` +
	`"Driver":{{json .Driver}},"CgroupDriver":{{json .CgroupDriver}},"CgroupVersion":{{json .CgroupVersion}},"LoggingDriver":{{json .LoggingDriver}},"DockerRootDir":{{json .DockerRootDir}},` +
	`"NCPU":{{json .NCPU}},"MemTotal":{{json .MemTotal}},"Containers":{{json .Containers}},"ContainersRunning":{{json .ContainersRunning}},"ContainersPaused":{{json .ContainersPaused}},` +
	`"ContainersStopped":{{json .ContainersStopped}},"Images":{{json .Images}}}`

const imageTemplate = `{"Id":{{json .Id}},"RepoTags":{{json .RepoTags}},"Created":{{json .Created}},"Size":{{json .Size}}}`

const volumeTemplate = `{"Name":{{json .Name}},"Driver":{{json .Driver}},"Scope":{{json .Scope}},"CreatedAt":{{json .CreatedAt}},"Mountpoint":{{json .Mountpoint}},"Labels":{{json .Labels}}}`

const networkTemplate = `{"Id":{{json .Id}},"Name":{{json .Name}},"Driver":{{json .Driver}},"Scope":{{json .Scope}},"Internal":{{json .Internal}},"Attachable":{{json .Attachable}},` +
	`"EnableIPv6":{{json .EnableIPv6}},"Created":{{json .Created}},"IPAM":{{json .IPAM}},"Containers":{{json .Containers}},"Labels":{{json .Labels}}}`

// ResourcesScript collects engine info, images, volumes, and networks (sections
// @@INFO@@, @@IMAGES@@, @@VOLUMES@@, @@NETWORKS@@). Volume options and network
// options are excluded by the templates because they can carry credentials.
const ResourcesScript = `
echo "@@INFO@@"
docker info --format '` + infoTemplate + `' 2>/dev/null || true
echo "@@IMAGES@@"
ids=$(docker image ls -q --no-trunc 2>/dev/null | sort -u); if [ -n "$ids" ]; then docker image inspect --format '` + imageTemplate + `' $ids 2>/dev/null; fi
echo "@@VOLUMES@@"
ids=$(docker volume ls -q 2>/dev/null); if [ -n "$ids" ]; then docker volume inspect --format '` + volumeTemplate + `' $ids 2>/dev/null; fi
echo "@@NETWORKS@@"
ids=$(docker network ls -q --no-trunc 2>/dev/null); if [ -n "$ids" ]; then docker network inspect --format '` + networkTemplate + `' $ids 2>/dev/null; fi
`

// DiskUsageScript runs `docker system df` (summary + verbose for volume sizes).
// It walks image layers and volume directories, so callers poll it slowly.
const DiskUsageScript = `
echo "@@DF@@"
docker system df --format '{{json .}}' 2>/dev/null || true
echo "@@DFV@@"
docker system df -v --format '{{json .}}' 2>/dev/null || true
`

func jsonLines(out string, fn func([]byte)) {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		fn([]byte(line))
	}
}

// ParseInfo parses the @@INFO@@ section.
func ParseInfo(out string, now time.Time) *asterismv1.DockerEngine {
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
	found := false
	jsonLines(out, func(b []byte) {
		if found {
			return
		}
		if json.Unmarshal(b, &d) == nil && d.ServerVersion != "" {
			found = true
		}
	})
	if !found {
		return &asterismv1.DockerEngine{Available: false, UnavailableReason: "docker info returned no data (is Docker installed and is the SSH user in the docker group?)", CollectedAtMs: now.UnixMilli()}
	}
	return &asterismv1.DockerEngine{
		Available:         true,
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
	}
}

// ParseImages parses the @@IMAGES@@ section.
func ParseImages(out string) []*asterismv1.DockerImage {
	var res []*asterismv1.DockerImage
	seen := make(map[string]bool)
	jsonLines(out, func(b []byte) {
		var d struct {
			ID       string   `json:"Id"`
			RepoTags []string `json:"RepoTags"`
			Created  string   `json:"Created"`
			Size     int64    `json:"Size"`
		}
		if json.Unmarshal(b, &d) != nil || d.ID == "" || seen[d.ID] {
			return
		}
		seen[d.ID] = true
		img := &asterismv1.DockerImage{
			Id:          ShortImageID(d.ID),
			CreatedAtMs: parseDockerTime(d.Created),
			SizeBytes:   uint64(max(d.Size, 0)),
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
	})
	sort.Slice(res, func(i, j int) bool {
		if res[i].Dangling != res[j].Dangling {
			return !res[i].Dangling
		}
		if res[i].Repository != res[j].Repository {
			return res[i].Repository < res[j].Repository
		}
		return res[i].Tag < res[j].Tag
	})
	return res
}

var hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseVolumes parses the @@VOLUMES@@ section. Sizes start unknown (-1).
func ParseVolumes(out string) []*asterismv1.DockerVolume {
	var res []*asterismv1.DockerVolume
	jsonLines(out, func(b []byte) {
		var d struct {
			Name       string            `json:"Name"`
			Driver     string            `json:"Driver"`
			Scope      string            `json:"Scope"`
			CreatedAt  string            `json:"CreatedAt"`
			Mountpoint string            `json:"Mountpoint"`
			Labels     map[string]string `json:"Labels"`
		}
		if json.Unmarshal(b, &d) != nil || d.Name == "" {
			return
		}
		res = append(res, &asterismv1.DockerVolume{
			Name:           d.Name,
			Driver:         d.Driver,
			Scope:          d.Scope,
			Mountpoint:     d.Mountpoint,
			CreatedAtMs:    parseDockerTime(d.CreatedAt),
			SizeBytes:      -1,
			ComposeProject: d.Labels[LabelComposeProject],
			Anonymous:      hex64Re.MatchString(d.Name),
		})
	})
	sort.Slice(res, func(i, j int) bool {
		if res[i].Anonymous != res[j].Anonymous {
			return !res[i].Anonymous
		}
		return res[i].Name < res[j].Name
	})
	return res
}

// ParseNetworks parses the @@NETWORKS@@ section.
func ParseNetworks(out string) []*asterismv1.DockerNetwork {
	var res []*asterismv1.DockerNetwork
	jsonLines(out, func(b []byte) {
		var d struct {
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
			Containers map[string]struct {
				Name        string `json:"Name"`
				IPv4Address string `json:"IPv4Address"`
				IPv6Address string `json:"IPv6Address"`
				MacAddress  string `json:"MacAddress"`
			} `json:"Containers"`
			Labels map[string]string `json:"Labels"`
		}
		if json.Unmarshal(b, &d) != nil || d.Name == "" {
			return
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
		for _, id := range SortedKeys(d.Containers) {
			c := d.Containers[id]
			n.Containers = append(n.Containers, &asterismv1.NetworkAttachment{
				ContainerId: ShortID(id),
				Name:        c.Name,
				Ipv4Address: c.IPv4Address,
				Ipv6Address: c.IPv6Address,
				MacAddress:  c.MacAddress,
			})
		}
		sort.Slice(n.Containers, func(i, j int) bool { return n.Containers[i].Name < n.Containers[j].Name })
		res = append(res, n)
	})
	sort.Slice(res, func(i, j int) bool {
		if res[i].Builtin != res[j].Builtin {
			return !res[i].Builtin
		}
		return res[i].Name < res[j].Name
	})
	return res
}

// ParseDiskUsage parses `docker system df --format '{{json .}}'` lines.
func ParseDiskUsage(out string, now time.Time) *asterismv1.DockerDiskUsage {
	res := &asterismv1.DockerDiskUsage{CollectedAtMs: now.UnixMilli()}
	jsonLines(out, func(b []byte) {
		var d struct {
			Type        string `json:"Type"`
			TotalCount  string `json:"TotalCount"`
			Active      string `json:"Active"`
			Size        string `json:"Size"`
			Reclaimable string `json:"Reclaimable"`
		}
		if json.Unmarshal(b, &d) != nil || d.Type == "" {
			return
		}
		kind := ""
		switch strings.ToLower(d.Type) {
		case "images":
			kind = "images"
		case "containers":
			kind = "containers"
		case "local volumes", "volumes":
			kind = "volumes"
		case "build cache":
			kind = "build_cache"
		default:
			return
		}
		total, _ := strconv.ParseUint(strings.TrimSpace(d.TotalCount), 10, 32)
		active, _ := strconv.ParseUint(strings.TrimSpace(d.Active), 10, 32)
		recl := d.Reclaimable
		if p := strings.IndexByte(recl, '('); p >= 0 {
			recl = recl[:p]
		}
		cat := &asterismv1.DiskUsageCategory{
			Kind:             kind,
			TotalCount:       uint32(total),
			ActiveCount:      uint32(active),
			SizeBytes:        ParseHumanBytes(d.Size),
			ReclaimableBytes: ParseHumanBytes(recl),
		}
		res.Categories = append(res.Categories, cat)
		res.TotalSizeBytes += cat.SizeBytes
		res.TotalReclaimableBytes += cat.ReclaimableBytes
	})
	return res
}

// ParseVolumeSizes extracts per-volume sizes from `docker system df -v --format '{{json .}}'`.
// It returns nil when the output is missing or in an unexpected shape.
func ParseVolumeSizes(out string) map[string]int64 {
	var sizes map[string]int64
	jsonLines(out, func(b []byte) {
		var d struct {
			Volumes []struct {
				Name string `json:"Name"`
				Size string `json:"Size"`
			} `json:"Volumes"`
		}
		if json.Unmarshal(b, &d) != nil || len(d.Volumes) == 0 {
			return
		}
		if sizes == nil {
			sizes = make(map[string]int64, len(d.Volumes))
		}
		for _, v := range d.Volumes {
			if v.Name == "" || v.Size == "" || strings.EqualFold(v.Size, "N/A") {
				continue
			}
			sizes[v.Name] = int64(ParseHumanBytes(v.Size))
		}
	})
	return sizes
}

// LinkUsage fills in-use counts on images and volumes from container references.
func LinkUsage(containers []*asterismv1.Container, images []*asterismv1.DockerImage, volumes []*asterismv1.DockerVolume) {
	const maxUsedBy = 20
	imgByID := make(map[string]*asterismv1.DockerImage, len(images))
	for _, img := range images {
		img.Containers = 0
		img.UsedBy = nil
		imgByID[img.Id] = img
	}
	volByName := make(map[string]*asterismv1.DockerVolume, len(volumes))
	for _, v := range volumes {
		v.Containers = 0
		v.UsedBy = nil
		volByName[v.Name] = v
	}
	for _, c := range containers {
		if img, ok := imgByID[c.ImageId]; ok {
			img.Containers++
			if len(img.UsedBy) < maxUsedBy {
				img.UsedBy = append(img.UsedBy, c.Name)
			}
		}
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Name == "" {
				continue
			}
			if v, ok := volByName[m.Name]; ok {
				v.Containers++
				if len(v.UsedBy) < maxUsedBy {
					v.UsedBy = append(v.UsedBy, c.Name)
				}
			}
		}
	}
}
