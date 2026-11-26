package service

import (
	"hash/fnv"
	"sort"
	"strings"
	"unicode"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Compose labels that set a project's icon from any of its containers.
const (
	LabelIcon  = "asterism.icon"
	LabelColor = "asterism.color"
)

// ProjectDisplay is the configured icon and colour for one project
// ([projects."<name>"] in asterism.toml).
type ProjectDisplay struct {
	Icon  string
	Color string
}

// iconColorTokens are the design tokens a project icon may be tinted with.
// The monogram palette cycles through them in this order.
var iconColorTokens = []string{
	"chart-1", "chart-3", "chart-5", "role-db", "role-cache",
	"chart-2", "chart-4", "role-worker", "role-proxy", "role-app",
}

// validColorToken reports whether name is an allowed icon colour token.
// "primary" is accepted in addition to the palette.
func validColorToken(name string) bool {
	if name == "primary" {
		return true
	}
	for _, t := range iconColorTokens {
		if t == name {
			return true
		}
	}
	return false
}

// normalizeColor accepts "chart-3", "--chart-3" or "var(--chart-3)".
func normalizeColor(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(strings.TrimPrefix(s, "var("), ")")
	s = strings.TrimPrefix(s, "--")
	if validColorToken(s) {
		return s
	}
	return ""
}

// Monogram returns one or two uppercase letters for a project name:
// the initials of the first two words, or the first two letters of a single word.
func Monogram(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return "?"
	}
	if len(words) == 1 {
		rs := []rune(words[0])
		if len(rs) > 2 {
			rs = rs[:2]
		}
		return strings.ToUpper(string(rs))
	}
	return strings.ToUpper(string([]rune(words[0])[:1]) + string([]rune(words[1])[:1]))
}

// MonogramColor picks a deterministic palette token for a project name.
func MonogramColor(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(name)))
	return iconColorTokens[h.Sum32()%uint32(len(iconColorTokens))]
}

// iconSource is one container's view of a project: its image and labels.
type iconSource struct {
	id     string
	image  string
	labels map[string]string
	mem    uint64
}

// ResolveProjectIcon picks a project's icon. Precedence: configuration, then
// the asterism.icon compose label, then the dominant detected technology, then
// a monogram. Colours follow the same precedence, falling back to the
// technology's category colour or the monogram palette.
func ResolveProjectIcon(name string, cfg ProjectDisplay, sources []iconSource) *asterismv1.ProjectIcon {
	icon := &asterismv1.ProjectIcon{Monogram: Monogram(name)}
	color := normalizeColor(cfg.Color)

	sort.SliceStable(sources, func(i, j int) bool { return sources[i].id < sources[j].id })
	var labelIcon, labelColor string
	for _, s := range sources {
		if labelIcon == "" {
			if _, ok := LookupTech(s.labels[LabelIcon]); ok {
				labelIcon = strings.ToLower(strings.TrimSpace(s.labels[LabelIcon]))
			}
		}
		if labelColor == "" {
			labelColor = normalizeColor(s.labels[LabelColor])
		}
	}
	if color == "" {
		color = labelColor
	}

	setTech := func(kind asterismv1.ProjectIconKind, slug string) bool {
		t, ok := LookupTech(slug)
		if !ok {
			return false
		}
		icon.Kind = kind
		icon.Slug = t.Slug
		icon.Label = t.Label
		if color == "" {
			color = t.Color()
		}
		return true
	}

	switch {
	case setTech(asterismv1.ProjectIconKind_PROJECT_ICON_KIND_CONFIGURED, cfg.Icon):
	case setTech(asterismv1.ProjectIconKind_PROJECT_ICON_KIND_LABEL, labelIcon):
	case setTech(asterismv1.ProjectIconKind_PROJECT_ICON_KIND_DETECTED, dominantTech(name, sources)):
	default:
		icon.Kind = asterismv1.ProjectIconKind_PROJECT_ICON_KIND_MONOGRAM
		if color == "" {
			color = MonogramColor(name)
		}
	}
	icon.ColorToken = color
	return icon
}

// dominantTech returns the technology that best describes a project: a
// project named after a technology wins; otherwise the detected technology
// with the highest category priority, then the most containers, then the most
// memory, then the slug (for stable output).
func dominantTech(projectName string, sources []iconSource) string {
	if t, ok := LookupTech(projectName); ok {
		return t.Slug
	}
	if slug := exactTech(projectName); slug != "" {
		return slug
	}
	type vote struct {
		slug  string
		prio  int
		count int
		mem   uint64
	}
	votes := map[string]*vote{}
	for _, s := range sources {
		slug := DetectTech(s.image)
		if slug == "" {
			continue
		}
		v, ok := votes[slug]
		if !ok {
			t, _ := LookupTech(slug)
			v = &vote{slug: slug, prio: categoryPriority[t.Category]}
			votes[slug] = v
		}
		v.count++
		v.mem += s.mem
	}
	if len(votes) == 0 {
		return ""
	}
	list := make([]*vote, 0, len(votes))
	for _, v := range votes {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.prio != b.prio {
			return a.prio > b.prio
		}
		if a.count != b.count {
			return a.count > b.count
		}
		if a.mem != b.mem {
			return a.mem > b.mem
		}
		return a.slug < b.slug
	})
	return list[0].slug
}

// exactTech matches a project name exactly against catalogue image names
// (e.g. a project called "uptime-kuma").
func exactTech(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, t := range techCatalog {
		for _, tn := range t.Names {
			if n == tn {
				return t.Slug
			}
		}
	}
	return ""
}
