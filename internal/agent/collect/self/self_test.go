package self

import (
	"context"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

func TestSelf(t *testing.T) {
	s := New(time.Minute)
	if _, err := s.Init(context.Background(), state.Env{Root: "/does/not/matter"}); err != nil {
		t.Fatal(err)
	}
	sink := collecttest.New()
	if err := s.Tick(time.Unix(1, 0), sink); err != nil {
		t.Fatal(err)
	}
	if v, ok := sink.Value(Entity, "go.goroutines"); !ok || v < 1 {
		t.Fatalf("goroutines %v", v)
	}
	if v, ok := sink.Value(Entity, "mem.rss_bytes"); !ok || v <= 0 {
		t.Fatalf("rss %v %v", v, ok)
	}
	if v, _ := sink.Value(Entity, "go.heap_bytes"); v <= 0 {
		t.Fatalf("heap %v", v)
	}
}
