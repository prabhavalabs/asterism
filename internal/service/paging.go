package service

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Page size bounds shared by every List RPC (docs/BLUEPRINT.md §8.7).
const (
	DefaultPageSize = 25
	MinPageSize     = 10
	MaxPageSize     = 200
)

// pageBounds resolves a PageRequest into a 1-based page and a clamped size.
// A nil request means "first page, as many items as allowed" so callers that
// need a whole small list (topology maps, lookups) stay bounded.
func pageBounds(req *asterismv1.PageRequest) (page, size int) {
	if req == nil {
		return 1, MaxPageSize
	}
	size = int(req.PageSize)
	switch {
	case size <= 0:
		size = DefaultPageSize
	case size < MinPageSize:
		size = MinPageSize
	case size > MaxPageSize:
		size = MaxPageSize
	}
	page = int(req.Page)
	if page < 1 {
		page = 1
	}
	return page, size
}

// legacyPage turns an old-style limit into a PageRequest when no page was sent.
func legacyPage(req *asterismv1.PageRequest, limit uint32) *asterismv1.PageRequest {
	if req != nil || limit == 0 {
		return req
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	return &asterismv1.PageRequest{PageSize: int32(limit), Page: 1}
}

// paginate returns the requested page of items and its PageInfo. A page past
// the end yields no items while PageInfo still reports the real totals.
func paginate[T any](items []T, req *asterismv1.PageRequest) ([]T, *asterismv1.PageInfo) {
	page, size := pageBounds(req)
	total := len(items)
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	info := &asterismv1.PageInfo{
		Page:       int32(page),
		PageSize:   int32(size),
		TotalSize:  int32(total),
		TotalPages: int32(pages),
	}
	// int64 keeps (page-1)*size from overflowing for absurd page numbers.
	start := int64(page-1) * int64(size)
	if start >= int64(total) {
		return []T{}, info
	}
	end := min(start+int64(size), int64(total))
	return items[start:end], info
}

// sortField is one allow-listed order_by field.
type sortField[T any] struct {
	name string
	cmp  func(a, b T) int
}

// sorter is the per-resource order_by allow-list with a stable id tie-break.
type sorter[T any] struct {
	resource string
	fields   []sortField[T]
	id       func(T) string
	def      string
}

type sortTerm[T any] struct {
	field sortField[T]
	desc  bool
}

func newSorter[T any](resource, def string, id func(T) string, fields ...sortField[T]) sorter[T] {
	return sorter[T]{resource: resource, fields: fields, id: id, def: def}
}

func by[T any, K cmp.Ordered](name string, key func(T) K) sortField[T] {
	return sortField[T]{name: name, cmp: func(a, b T) int { return cmp.Compare(key(a), key(b)) }}
}

func byFold[T any](name string, key func(T) string) sortField[T] {
	return sortField[T]{name: name, cmp: func(a, b T) int {
		return cmp.Compare(strings.ToLower(key(a)), strings.ToLower(key(b)))
	}}
}

// allowed lists the accepted field names in declaration order.
func (s sorter[T]) allowed() []string {
	out := make([]string, len(s.fields))
	for i, f := range s.fields {
		out[i] = f.name
	}
	return out
}

// parse validates an AIP-132 order_by string such as "cpu desc, name".
// Field names are case-insensitive; an empty string selects the default.
func (s sorter[T]) parse(orderBy string) ([]sortTerm[T], error) {
	if strings.TrimSpace(orderBy) == "" {
		orderBy = s.def
	}
	var terms []sortTerm[T]
	seen := map[string]bool{}
	for _, raw := range strings.Split(orderBy, ",") {
		parts := strings.Fields(raw)
		if len(parts) == 0 {
			return nil, newErr(ErrInvalidArgument, "invalid order_by %q: empty field (use e.g. %q)", orderBy, s.def)
		}
		if len(parts) > 2 {
			return nil, newErr(ErrInvalidArgument, "invalid order_by term %q: use \"<field>\" or \"<field> desc\"", strings.TrimSpace(raw))
		}
		name := strings.ToLower(parts[0])
		desc := false
		if len(parts) == 2 {
			switch strings.ToLower(parts[1]) {
			case "desc":
				desc = true
			case "asc":
			default:
				return nil, newErr(ErrInvalidArgument, "invalid order_by direction %q for %q: use asc or desc", parts[1], name)
			}
		}
		if seen[name] {
			return nil, newErr(ErrInvalidArgument, "invalid order_by %q: field %q listed twice", orderBy, name)
		}
		seen[name] = true
		idx := slices.IndexFunc(s.fields, func(f sortField[T]) bool { return f.name == name })
		if idx < 0 {
			return nil, newErr(ErrInvalidArgument, "unknown %s order_by field %q; allowed: %s", s.resource, name, strings.Join(s.allowed(), ", "))
		}
		terms = append(terms, sortTerm[T]{field: s.fields[idx], desc: desc})
	}
	return terms, nil
}

// sort orders items in place by order_by, always breaking ties by id so pages
// are stable between requests.
func (s sorter[T]) sort(items []T, orderBy string) error {
	terms, err := s.parse(orderBy)
	if err != nil {
		return err
	}
	slices.SortStableFunc(items, func(a, b T) int {
		for _, t := range terms {
			c := t.field.cmp(a, b)
			if t.desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return cmp.Compare(s.id(a), s.id(b))
	})
	return nil
}

// filter is one named predicate. Facet counts for a filter are computed over
// the items matching every other filter.
type filter[T any] struct {
	facet string
	match func(T) bool
}

// matching returns the items that pass every filter except the one named skip.
func matching[T any](items []T, filters []filter[T], skip string) []T {
	out := make([]T, 0, len(items))
next:
	for _, it := range items {
		for _, f := range filters {
			if f.facet != "" && f.facet == skip {
				continue
			}
			if f.match != nil && !f.match(it) {
				continue next
			}
		}
		out = append(out, it)
	}
	return out
}

// countBy tallies a string facet, sorted by value.
func countBy[T any](items []T, key func(T) string) []*asterismv1.FacetCount {
	counts := map[string]uint32{}
	for _, it := range items {
		counts[key(it)]++
	}
	out := make([]*asterismv1.FacetCount, 0, len(counts))
	for v, n := range counts {
		out = append(out, &asterismv1.FacetCount{Value: v, Count: n})
	}
	slices.SortFunc(out, func(a, b *asterismv1.FacetCount) int { return cmp.Compare(a.Value, b.Value) })
	return out
}

// normQuery lower-cases and trims a search query.
func normQuery(q string) string { return strings.ToLower(strings.TrimSpace(q)) }

// containsAny reports whether q (already normalised) is a substring of any field.
func containsAny(q string, fields ...string) bool {
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// portQuery parses q as a port number; ok is false when q is not one.
func portQuery(q string) (uint32, bool) {
	n, err := strconv.ParseUint(q, 10, 16)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint32(n), true
}

// listPage sorts the filtered items and cuts the requested page.
func listPage[T any](items []T, s sorter[T], orderBy string, req *asterismv1.PageRequest) ([]T, *asterismv1.PageInfo, error) {
	if err := s.sort(items, orderBy); err != nil {
		return nil, nil, err
	}
	out, info := paginate(items, req)
	return out, info, nil
}
