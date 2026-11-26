package uplink

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func metricsFrame(seq uint64, values int) *v1.AgentFrame {
	row := &v1.MetricBatch{TsMs: int64(seq)}
	for i := range values {
		row.SeriesIds = append(row.SeriesIds, uint32(i+1))
		row.Values = append(row.Values, float32(i))
	}
	return &v1.AgentFrame{Seq: seq, Payload: &v1.AgentFrame_Metrics{Metrics: &v1.AgentMetrics{Rows: []*v1.MetricBatch{row}, RowGroups: []uint32{0}}}}
}

func eventsFrame(seq uint64, n int) *v1.AgentFrame {
	ev := &v1.AgentEvents{}
	for i := range n {
		ev.Events = append(ev.Events, &v1.Event{Id: fmt.Sprint(i)})
	}
	return &v1.AgentFrame{Seq: seq, Payload: &v1.AgentFrame_Events{Events: ev}}
}

func TestRingDropOldestPerKind(t *testing.T) {
	r := NewRing(1000, 5)
	seq := uint64(0)
	for range 20 {
		seq++
		r.Push(metricsFrame(seq, 20))
	}
	seq++
	r.Push(eventsFrame(seq, 3))
	seq++
	r.Push(eventsFrame(seq, 3)) // 6 events > 5: oldest event frame dropped
	if r.Bytes() > 1000+r.Bytes()-r.metricsBytes || r.metricsBytes > 1000 {
		t.Fatalf("metrics bytes %d over limit", r.metricsBytes)
	}
	if r.eventCount != 3 || r.DroppedEvents() != 3 {
		t.Fatalf("events %d dropped %d", r.eventCount, r.DroppedEvents())
	}
	if r.Dropped() == 0 {
		t.Fatal("dropped counter")
	}
	f, _, _ := r.NextUnsent()
	if f.GetSeq() == 1 {
		t.Fatal("oldest metrics frame should be gone")
	}
	// Order is preserved across kinds and ack frees a prefix.
	last := uint64(0)
	for _, e := range r.entries {
		if e.seq <= last {
			t.Fatalf("out of order: %d after %d", e.seq, last)
		}
		last = e.seq
	}
	n := r.Len()
	freed := r.Ack(r.entries[2].seq)
	if freed != 3 || r.Len() != n-3 {
		t.Fatalf("ack freed %d", freed)
	}
}

func TestRingResendAfterReconnect(t *testing.T) {
	r := NewRing(1<<20, 100)
	for s := uint64(1); s <= 3; s++ {
		r.Push(metricsFrame(s, 1))
	}
	for {
		f, _, ok := r.NextUnsent()
		if !ok {
			break
		}
		r.MarkSent(f.GetSeq())
	}
	r.Ack(1)
	r.ResetSent()
	f, _, ok := r.NextUnsent()
	if !ok || f.GetSeq() != 2 {
		t.Fatalf("replay should start at 2, got %v", f.GetSeq())
	}
}

func TestBackoffBoundsAndReset(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: 60 * time.Second, Rand: func() float64 { return 0.999999 }}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i, w := range want {
		if c := b.Ceiling(i); c != w*time.Second {
			t.Errorf("ceiling(%d) = %v want %v", i, c, w*time.Second)
		}
	}
	for i := range 100 {
		if d := b.Next(); d < 0 || d > 60*time.Second {
			t.Fatalf("delay %d out of range: %v", i, d)
		}
	}
	b.Reset()
	if d := b.Next(); d > time.Second {
		t.Fatalf("after reset delay %v", d)
	}
	zero := Backoff{Initial: time.Second, Max: time.Minute, Rand: func() float64 { return 0 }}
	if zero.Next() != 0 {
		t.Fatal("full jitter allows zero delay")
	}
}

func TestDedupeWindow(t *testing.T) {
	r := newRecentIDs()
	if !r.add("a") || r.add("a") {
		t.Fatal("dedupe")
	}
	for i := range DedupeWindow {
		r.add(fmt.Sprint(i))
	}
	if !r.add("a") {
		t.Fatal("a should have aged out")
	}
	if len(r.set) != DedupeWindow {
		t.Fatalf("set size %d", len(r.set))
	}
}
