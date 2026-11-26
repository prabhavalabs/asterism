package procfs

import "time"

// Delta returns cur-prev for a monotonically increasing counter. A counter
// that went backwards is either a 32-bit wrap (some drivers and 32-bit
// kernels) or a reset (container restart, interface re-creation): a small
// wrapped delta is accepted, anything else yields 0 instead of a spike.
func Delta(prev, cur uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	if prev <= 1<<32-1 {
		if d := (1<<32 - prev) + cur; d < 1<<31 {
			return d
		}
	}
	return 0
}

// Counter turns successive samples of a cumulative counter into a per-second
// rate. The zero value is ready to use; the first sample yields ok=false.
type Counter struct {
	last uint64
	at   time.Time
	have bool
}

// Observe records v at now and returns the rate since the previous sample.
func (c *Counter) Observe(now time.Time, v uint64) (rate float64, ok bool) {
	if c.have {
		if dt := now.Sub(c.at).Seconds(); dt > 0 {
			rate, ok = float64(Delta(c.last, v))/dt, true
		}
	}
	c.last, c.at, c.have = v, now, true
	return rate, ok
}

// Reset forgets the previous sample.
func (c *Counter) Reset() { *c = Counter{} }
