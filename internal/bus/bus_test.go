package bus

import "testing"

func TestPublishDropsOldestWithoutBlocking(t *testing.T) {
	b := New(2)
	ch, unsub := b.Subscribe()
	defer unsub()

	for i := 0; i < 5; i++ {
		b.Publish(Message{
			Samples: []MetricSample{{EntityID: "host", Metric: "cpu.util", TSMs: int64(i), Value: float64(i)}},
		})
	}

	if b.Dropped() == 0 {
		t.Fatal("expected dropped messages for slow subscriber")
	}

	first := <-ch
	second := <-ch
	if first.Samples[0].TSMs != 3 || second.Samples[0].TSMs != 4 {
		t.Fatalf("expected newest samples (3, 4), got (%d, %d)", first.Samples[0].TSMs, second.Samples[0].TSMs)
	}
}
