package sched

import (
	"sync"
	"time"
)

// Clock abstracts time for the scheduler (fake in tests).
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is the subset of time.Timer the scheduler uses.
type Timer interface {
	C() <-chan time.Time
	Reset(d time.Duration)
	Stop()
}

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// NewTimer implements Clock.
func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time   { return r.t.C }
func (r realTimer) Reset(d time.Duration) { r.t.Reset(d) }
func (r realTimer) Stop()                 { r.t.Stop() }

// FakeClock is a manually advanced clock. Timers fire when Advance moves
// the clock past their deadline. Armed receives a value every time a timer
// is created or reset, so tests can wait until the code under test is idle
// without sleeping.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	armed  chan time.Duration
}

// NewFakeClock returns a FakeClock at t.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t, armed: make(chan time.Duration, 1024)}
}

// Armed delivers the duration of every timer creation or reset.
func (f *FakeClock) Armed() <-chan time.Duration { return f.armed }

// Now implements Clock.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// NewTimer implements Clock.
func (f *FakeClock) NewTimer(d time.Duration) Timer {
	t := &fakeTimer{clk: f, c: make(chan time.Time, 1)}
	t.Reset(d)
	return t
}

// Advance moves the clock forward and fires due timers.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	var fire []*fakeTimer
	for _, t := range f.timers {
		if t.active && !t.deadline.After(now) {
			t.active = false
			fire = append(fire, t)
		}
	}
	f.mu.Unlock()
	for _, t := range fire {
		select {
		case t.c <- now:
		default:
		}
	}
}

type fakeTimer struct {
	clk      *FakeClock
	c        chan time.Time
	deadline time.Time
	active   bool
	added    bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Reset(d time.Duration) {
	f := t.clk
	f.mu.Lock()
	t.deadline = f.now.Add(d)
	t.active = true
	if !t.added {
		f.timers = append(f.timers, t)
		t.added = true
	}
	fireNow := d <= 0
	if fireNow {
		t.active = false
	}
	now := f.now
	f.mu.Unlock()
	if fireNow {
		select {
		case t.c <- now:
		default:
		}
	}
	select {
	case f.armed <- d:
	default:
	}
}

func (t *fakeTimer) Stop() {
	t.clk.mu.Lock()
	t.active = false
	t.clk.mu.Unlock()
	select {
	case <-t.c:
	default:
	}
}
