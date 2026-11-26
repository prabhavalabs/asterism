package ring

import (
	"sync"
)

// Point is a timestamped float64 observation.
type Point struct {
	TSMs  int64
	Value float64
}

// Key uniquely identifies a metric time-series.
type Key struct {
	EntityID string
	Metric   string
}

type seriesBuf struct {
	points []Point
	head   int
	count  int
}

func newSeriesBuf(capacity int) *seriesBuf {
	return &seriesBuf{points: make([]Point, capacity)}
}

func (s *seriesBuf) push(p Point) {
	capLen := len(s.points)
	if capLen == 0 {
		return
	}
	idx := (s.head + s.count) % capLen
	s.points[idx] = p
	if s.count < capLen {
		s.count++
	} else {
		s.head = (s.head + 1) % capLen
	}
}

func (s *seriesBuf) query(fromMs, toMs int64) []Point {
	if s.count == 0 {
		return nil
	}
	capLen := len(s.points)
	out := make([]Point, 0, s.count)
	for i := 0; i < s.count; i++ {
		p := s.points[(s.head+i)%capLen]
		if (fromMs == 0 || p.TSMs >= fromMs) && (toMs == 0 || p.TSMs <= toMs) {
			out = append(out, p)
		}
	}
	return out
}

func (s *seriesBuf) latest() (Point, bool) {
	if s.count == 0 {
		return Point{}, false
	}
	idx := (s.head + s.count - 1) % len(s.points)
	return s.points[idx], true
}

// Store keeps hot per-second time-series in fixed-capacity in-memory ring buffers.
type Store struct {
	mu       sync.RWMutex
	capacity int
	series   map[Key]*seriesBuf
}

func New(capacity int) *Store {
	if capacity <= 0 {
		capacity = 900 // 15 minutes at 1 Hz
	}
	return &Store{
		capacity: capacity,
		series:   make(map[Key]*seriesBuf),
	}
}

func (s *Store) Append(entityID, metric string, tsMs int64, value float64) {
	k := Key{EntityID: entityID, Metric: metric}
	s.mu.Lock()
	buf, ok := s.series[k]
	if !ok {
		buf = newSeriesBuf(s.capacity)
		s.series[k] = buf
	}
	buf.push(Point{TSMs: tsMs, Value: value})
	s.mu.Unlock()
}

func (s *Store) Query(entityID, metric string, fromMs, toMs int64) []Point {
	k := Key{EntityID: entityID, Metric: metric}
	s.mu.RLock()
	buf, ok := s.series[k]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	out := buf.query(fromMs, toMs)
	s.mu.RUnlock()
	return out
}

func (s *Store) Latest(entityID, metric string) (Point, bool) {
	k := Key{EntityID: entityID, Metric: metric}
	s.mu.RLock()
	buf, ok := s.series[k]
	if !ok {
		s.mu.RUnlock()
		return Point{}, false
	}
	p, found := buf.latest()
	s.mu.RUnlock()
	return p, found
}
