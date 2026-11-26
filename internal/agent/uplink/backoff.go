package uplink

import (
	"math/rand/v2"
	"time"
)

// Backoff is full-jitter exponential backoff: the n-th delay is uniform in
// [0, min(Max, Initial*2^n)].
type Backoff struct {
	Initial time.Duration
	Max     time.Duration
	// Rand returns a float in [0, 1); nil uses math/rand/v2.
	Rand    func() float64
	attempt int
}

// Ceiling returns the upper bound of the delay for attempt n.
func (b *Backoff) Ceiling(n int) time.Duration {
	d := b.Initial
	for range n {
		d *= 2
		if d >= b.Max || d <= 0 {
			return b.Max
		}
	}
	return min(d, b.Max)
}

// Next returns the next delay and advances the attempt counter.
func (b *Backoff) Next() time.Duration {
	r := b.Rand
	if r == nil {
		r = rand.Float64
	}
	ceil := b.Ceiling(b.attempt)
	if b.attempt < 62 {
		b.attempt++
	}
	return time.Duration(r() * float64(ceil))
}

// Reset starts over from Initial (after a stable connection).
func (b *Backoff) Reset() { b.attempt = 0 }

// Attempt returns the number of delays handed out since the last Reset.
func (b *Backoff) Attempt() int { return b.attempt }
