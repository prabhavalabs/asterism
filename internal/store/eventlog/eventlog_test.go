package eventlog

import (
	"fmt"
	"testing"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestLogOrderingFilterAndOverflow(t *testing.T) {
	l := New(3)
	for i := 1; i <= 5; i++ {
		ent := "ctr:a"
		if i%2 == 0 {
			ent = "ctr:b"
		}
		l.Append(&asterismv1.Event{Id: fmt.Sprintf("e%d", i), TsMs: int64(i), EntityId: ent})
	}
	if l.Len() != 3 {
		t.Fatalf("expected 3 events after overflow, got %d", l.Len())
	}
	tests := []struct {
		name   string
		entity string
		limit  int
		want   []string
	}{
		{"all newest first", "", 0, []string{"e5", "e4", "e3"}},
		{"limited", "", 2, []string{"e5", "e4"}},
		{"by entity", "ctr:a", 0, []string{"e5", "e3"}},
		{"unknown entity", "ctr:z", 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := l.List(tc.entity, tc.limit)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d events, want %d", len(got), len(tc.want))
			}
			for i, e := range got {
				if e.Id != tc.want[i] {
					t.Fatalf("event %d = %s, want %s", i, e.Id, tc.want[i])
				}
			}
		})
	}
}
