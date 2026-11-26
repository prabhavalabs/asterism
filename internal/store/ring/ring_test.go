package ring

import "testing"

func TestRingStoreWrapAndQuery(t *testing.T) {
	s := New(3)
	for i := int64(1); i <= 5; i++ {
		s.Append("host", "cpu.util", i*1000, float64(i)/10.0)
	}

	pts := s.Query("host", "cpu.util", 0, 0)
	if len(pts) != 3 {
		t.Fatalf("expected 3 points after wrap, got %d", len(pts))
	}
	if pts[0].TSMs != 3000 || pts[2].TSMs != 5000 {
		t.Fatalf("unexpected timestamps: %+v", pts)
	}

	filtered := s.Query("host", "cpu.util", 4000, 5000)
	if len(filtered) != 2 || filtered[0].TSMs != 4000 {
		t.Fatalf("unexpected filtered points: %+v", filtered)
	}

	latest, ok := s.Latest("host", "cpu.util")
	if !ok || latest.TSMs != 5000 {
		t.Fatalf("unexpected latest point: %+v, ok=%v", latest, ok)
	}
}
