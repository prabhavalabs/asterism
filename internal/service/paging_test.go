package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func TestPageBounds(t *testing.T) {
	tests := []struct {
		name     string
		req      *asterismv1.PageRequest
		wantPage int
		wantSize int
	}{
		{"nil request is first page at max size", nil, 1, MaxPageSize},
		{"zero values use defaults", &asterismv1.PageRequest{}, 1, DefaultPageSize},
		{"size below minimum is raised", &asterismv1.PageRequest{PageSize: 3}, 1, MinPageSize},
		{"size above maximum is clamped", &asterismv1.PageRequest{PageSize: 5000}, 1, MaxPageSize},
		{"negative size uses default", &asterismv1.PageRequest{PageSize: -4}, 1, DefaultPageSize},
		{"negative page is first page", &asterismv1.PageRequest{Page: -2, PageSize: 50}, 1, 50},
		{"in-range values pass through", &asterismv1.PageRequest{Page: 7, PageSize: 100}, 7, 100},
		{"exact bounds pass through", &asterismv1.PageRequest{Page: 1, PageSize: MinPageSize}, 1, MinPageSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, size := pageBounds(tt.req)
			if page != tt.wantPage || size != tt.wantSize {
				t.Fatalf("pageBounds = (%d, %d), want (%d, %d)", page, size, tt.wantPage, tt.wantSize)
			}
		})
	}
}

func TestPaginate(t *testing.T) {
	tests := []struct {
		name  string
		total int
		req   *asterismv1.PageRequest
		want  []int
		info  *asterismv1.PageInfo
	}{
		{"first page", 137, &asterismv1.PageRequest{PageSize: 25, Page: 1}, seq(25),
			&asterismv1.PageInfo{Page: 1, PageSize: 25, TotalSize: 137, TotalPages: 6}},
		{"middle page", 137, &asterismv1.PageRequest{PageSize: 25, Page: 2}, seq(50)[25:],
			&asterismv1.PageInfo{Page: 2, PageSize: 25, TotalSize: 137, TotalPages: 6}},
		{"short last page", 137, &asterismv1.PageRequest{PageSize: 25, Page: 6}, seq(137)[125:],
			&asterismv1.PageInfo{Page: 6, PageSize: 25, TotalSize: 137, TotalPages: 6}},
		{"exact multiple", 50, &asterismv1.PageRequest{PageSize: 25, Page: 2}, seq(50)[25:],
			&asterismv1.PageInfo{Page: 2, PageSize: 25, TotalSize: 50, TotalPages: 2}},
		{"past the end is empty with real totals", 30, &asterismv1.PageRequest{PageSize: 25, Page: 9}, []int{},
			&asterismv1.PageInfo{Page: 9, PageSize: 25, TotalSize: 30, TotalPages: 2}},
		{"huge page number does not overflow", 30, &asterismv1.PageRequest{PageSize: 200, Page: 2147483647}, []int{},
			&asterismv1.PageInfo{Page: 2147483647, PageSize: 200, TotalSize: 30, TotalPages: 1}},
		{"empty list still has one page", 0, &asterismv1.PageRequest{}, []int{},
			&asterismv1.PageInfo{Page: 1, PageSize: 25, TotalSize: 0, TotalPages: 1}},
		{"clamped size is reported", 15, &asterismv1.PageRequest{PageSize: 1}, seq(10),
			&asterismv1.PageInfo{Page: 1, PageSize: 10, TotalSize: 15, TotalPages: 2}},
		{"nil request returns up to the maximum", 250, nil, seq(200),
			&asterismv1.PageInfo{Page: 1, PageSize: 200, TotalSize: 250, TotalPages: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, info := paginate(seq(tt.total), tt.req)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("items = %v, want %v", got, tt.want)
			}
			if got == nil {
				t.Fatal("items must be non-nil")
			}
			if info.Page != tt.info.Page || info.PageSize != tt.info.PageSize || info.TotalSize != tt.info.TotalSize || info.TotalPages != tt.info.TotalPages {
				t.Fatalf("info = %+v, want %+v", info, tt.info)
			}
		})
	}
}

func TestLegacyPage(t *testing.T) {
	explicit := &asterismv1.PageRequest{Page: 2, PageSize: 10}
	tests := []struct {
		name  string
		req   *asterismv1.PageRequest
		limit uint32
		want  *asterismv1.PageRequest
	}{
		{"explicit page wins over limit", explicit, 100, explicit},
		{"no page and no limit stays nil", nil, 0, nil},
		{"limit becomes the page size", nil, 100, &asterismv1.PageRequest{Page: 1, PageSize: 100}},
		{"limit is capped at the maximum", nil, 5000, &asterismv1.PageRequest{Page: 1, PageSize: MaxPageSize}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := legacyPage(tt.req, tt.limit)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got.GetPage() != tt.want.Page || got.GetPageSize() != tt.want.PageSize {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

type row struct {
	id   string
	name string
	mem  uint64
}

var rowSorter = newSorter("row", "mem desc",
	func(r row) string { return r.id },
	byFold("name", func(r row) string { return r.name }),
	by("mem", func(r row) uint64 { return r.mem }),
)

func rowIDs(rs []row) string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.id
	}
	return strings.Join(ids, ",")
}

func TestSorterOrderBy(t *testing.T) {
	rows := []row{
		{"d", "beta", 10},
		{"a", "Alpha", 30},
		{"c", "beta", 10},
		{"b", "gamma", 20},
		{"e", "alpha", 10},
	}
	tests := []struct {
		orderBy string
		want    string
	}{
		{"", "a,b,c,d,e"}, // default "mem desc", ties by id
		{"mem desc", "a,b,c,d,e"},
		{"mem", "c,d,e,b,a"},
		{"mem asc", "c,d,e,b,a"},
		{"name", "a,e,c,d,b"},      // case-insensitive, ties by id
		{"name desc", "b,c,d,a,e"}, // desc reverses name only; id tie-break stays ascending
		{"mem desc, name", "a,b,e,c,d"},
		{"  NAME   DESC ,  mem  ", "b,c,d,e,a"}, // whitespace and case tolerant
		{"mem, name desc", "c,d,e,b,a"},
	}
	for _, tt := range tests {
		t.Run(tt.orderBy, func(t *testing.T) {
			got := slices.Clone(rows)
			if err := rowSorter.sort(got, tt.orderBy); err != nil {
				t.Fatalf("sort: %v", err)
			}
			if ids := rowIDs(got); ids != tt.want {
				t.Fatalf("order = %s, want %s", ids, tt.want)
			}
		})
	}
}

func TestSorterStableAcrossInputOrder(t *testing.T) {
	// Equal keys must come out in the same (id) order whatever the input order,
	// otherwise items would jump between pages on refetch.
	a := []row{{"3", "x", 1}, {"1", "x", 1}, {"2", "x", 1}, {"0", "y", 1}}
	b := []row{{"2", "x", 1}, {"0", "y", 1}, {"3", "x", 1}, {"1", "x", 1}}
	for _, ob := range []string{"", "name", "mem desc", "name desc, mem"} {
		x, y := slices.Clone(a), slices.Clone(b)
		if err := rowSorter.sort(x, ob); err != nil {
			t.Fatal(err)
		}
		if err := rowSorter.sort(y, ob); err != nil {
			t.Fatal(err)
		}
		if rowIDs(x) != rowIDs(y) {
			t.Fatalf("order_by %q not stable: %s vs %s", ob, rowIDs(x), rowIDs(y))
		}
	}
}

func TestSorterParseErrors(t *testing.T) {
	tests := []struct {
		orderBy string
		wantMsg string
	}{
		{"size", `unknown row order_by field "size"; allowed: name, mem`},
		{"name sideways", `invalid order_by direction "sideways"`},
		{"name desc extra", `use "<field>" or "<field> desc"`},
		{"name,", "empty field"},
		{",name", "empty field"},
		{"name,,mem", "empty field"},
		{"name, NAME desc", `field "name" listed twice`},
		{"name;drop", `unknown row order_by field "name;drop"`},
	}
	for _, tt := range tests {
		t.Run(tt.orderBy, func(t *testing.T) {
			_, err := rowSorter.parse(tt.orderBy)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("err = %q, want it to contain %q", err, tt.wantMsg)
			}
			// sort must refuse too and leave the slice untouched.
			rows := []row{{"b", "b", 1}, {"a", "a", 2}}
			if err := rowSorter.sort(rows, tt.orderBy); err == nil || rowIDs(rows) != "b,a" {
				t.Fatalf("sort accepted %q or reordered: %v %s", tt.orderBy, err, rowIDs(rows))
			}
		})
	}
}

func TestMatchingAndCountBy(t *testing.T) {
	type item struct{ cat, state string }
	items := []item{
		{"prod", "up"}, {"prod", "down"}, {"staging", "up"}, {"infra", "up"}, {"prod", "up"},
	}
	cat, state := "prod", "up"
	filters := []filter[item]{
		{facet: "category", match: func(i item) bool { return i.cat == cat }},
		{facet: "state", match: func(i item) bool { return i.state == state }},
		{match: func(item) bool { return true }}, // query: never skipped
	}
	if got := matching(items, filters, ""); len(got) != 2 {
		t.Fatalf("all filters: %d items, want 2", len(got))
	}
	cats := countBy(matching(items, filters, "category"), func(i item) string { return i.cat })
	want := []string{"infra=1", "prod=2", "staging=1"}
	if got := facetStrings(cats); !slices.Equal(got, want) {
		t.Fatalf("category facet = %v, want %v", got, want)
	}
	states := countBy(matching(items, filters, "state"), func(i item) string { return i.state })
	if got := facetStrings(states); !slices.Equal(got, []string{"down=1", "up=2"}) {
		t.Fatalf("state facet = %v", got)
	}
	// An unnamed filter is never skipped, even when skip is "".
	filters = append(filters, filter[item]{match: func(i item) bool { return i.state != "up" }})
	if got := matching(items, filters, "state"); len(got) != 1 {
		t.Fatalf("unnamed filter skipped: %d", len(got))
	}
}

func facetStrings(fs []*asterismv1.FacetCount) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = fmt.Sprintf("%s=%d", f.Value, f.Count)
	}
	return out
}

func TestQueryHelpers(t *testing.T) {
	if !containsAny("", "anything") || !containsAny("pg", "Postgres-PG") || containsAny("x", "abc", "def") {
		t.Fatal("containsAny")
	}
	tests := []struct {
		q    string
		port uint32
		ok   bool
	}{
		{"5432", 5432, true}, {"0", 0, false}, {"70000", 0, false}, {"54a", 0, false}, {"", 0, false},
	}
	for _, tt := range tests {
		if p, ok := portQuery(tt.q); p != tt.port || ok != tt.ok {
			t.Errorf("portQuery(%q) = %d, %v", tt.q, p, ok)
		}
	}
}
