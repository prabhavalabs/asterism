package sched

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

type tickRec struct {
	name string
	at   time.Time
}

type fakeSource struct {
	name     string
	interval time.Duration
	class    state.Class
	ticks    chan<- tickRec
	initErr  error
	tickErr  error
	panics   bool
	series   state.SeriesID
	hints    chan state.Hints
}

func (f *fakeSource) Name() string            { return f.name }
func (f *fakeSource) Interval() time.Duration { return f.interval }
func (f *fakeSource) Class() state.Class      { return f.class }
func (f *fakeSource) Init(context.Context, state.Env) ([]string, error) {
	return []string{"cap_" + f.name}, f.initErr
}
func (f *fakeSource) Tick(now time.Time, s state.Sink) error {
	if f.panics {
		panic("boom")
	}
	if f.series == 0 {
		f.series = s.Series("host", f.name)
	}
	s.Set(f.series, 1)
	f.ticks <- tickRec{f.name, now}
	return f.tickErr
}
func (f *fakeSource) ApplyHints(h state.Hints) {
	if f.hints != nil {
		f.hints <- h
	}
}

func fixedIntervals() config.Intervals {
	iv := config.Default().Intervals
	iv.Adaptive = false
	return iv
}

type harness struct {
	t      *testing.T
	clk    *FakeClock
	ticks  chan tickRec
	cancel context.CancelFunc
	done   chan struct{}
	s      *Scheduler
}

func start(t *testing.T, iv config.Intervals, srcs ...*fakeSource) *harness {
	t.Helper()
	clk := NewFakeClock(time.Unix(1000, 0))
	ticks := make(chan tickRec, 64)
	s := New(state.NewStore(state.Options{}), Options{Clock: clk, Intervals: iv})
	for _, f := range srcs {
		f.ticks = ticks
		s.Add(f)
	}
	s.Init(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, clk: clk, ticks: ticks, cancel: cancel, done: make(chan struct{}), s: s}
	go func() {
		defer close(h.done)
		_ = s.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	return h
}

func waitTimeout() <-chan time.Time { return time.After(5 * time.Second) }

// expect receives exactly the named ticks (in order), then waits until the
// scheduler re-arms its timer, and checks no other tick happened.
func (h *harness) expect(names ...string) {
	h.t.Helper()
	for _, n := range names {
		select {
		case r := <-h.ticks:
			if r.name != n {
				h.t.Fatalf("tick %q, want %q", r.name, n)
			}
		case <-waitTimeout():
			h.t.Fatalf("timed out waiting for tick %q", n)
		}
	}
	h.armed()
	select {
	case r := <-h.ticks:
		h.t.Fatalf("unexpected tick %q", r.name)
	default:
	}
}

func (h *harness) armed() {
	h.t.Helper()
	select {
	case <-h.clk.Armed():
	case <-waitTimeout():
		h.t.Fatal("scheduler never re-armed its timer")
	}
}

func TestSequentialTimerHeap(t *testing.T) {
	h := start(t, fixedIntervals(),
		&fakeSource{name: "host", interval: 2 * time.Second},
		&fakeSource{name: "containers", interval: 5 * time.Second},
		&fakeSource{name: "events", interval: 0}, // event-driven: never ticked
	)
	h.armed() // initial NewTimer
	h.expect("host", "containers")
	h.clk.Advance(2 * time.Second)
	h.expect("host")
	h.clk.Advance(2 * time.Second)
	h.expect("host")
	h.clk.Advance(1 * time.Second)
	h.expect("containers")
	h.clk.Advance(1 * time.Second)
	h.expect("host")
	// A long stall skips missed ticks instead of bursting.
	h.clk.Advance(30 * time.Second)
	h.expect("host", "containers")
}

func TestHintsRescheduleAndClamp(t *testing.T) {
	iv := config.Default().Intervals // adaptive, idle x3, min 1s, max 60s
	hints := make(chan state.Hints, 4)
	h := start(t, iv, &fakeSource{name: "cgroup", interval: 5 * time.Second, class: state.ClassContainers, hints: hints})
	h.armed()
	h.expect("cgroup")
	h.clk.Advance(5 * time.Second) // idle: stretched to 15s
	select {
	case r := <-h.ticks:
		t.Fatalf("idle interval should be 15s, ticked %v", r)
	default:
	}
	h.clk.Advance(10 * time.Second)
	h.expect("cgroup")
	// A focused container: sample at intervals.min (1s), not the hub's 100ms.
	h.s.SetHints(state.Hints{LiveViewers: true, ContainerInterval: 100 * time.Millisecond, Focus: []string{"ctr:a"}})
	h.armed()
	if got := <-hints; !got.LiveViewers {
		t.Fatalf("hints not delivered: %+v", got)
	}
	h.clk.Advance(time.Second)
	h.expect("cgroup")
	h.clk.Advance(time.Second)
	h.expect("cgroup")
}

func TestErrorsAndPanicsAreCounted(t *testing.T) {
	h := start(t, fixedIntervals(),
		&fakeSource{name: "broken", interval: time.Second, initErr: errors.New("no cgroup v2")},
		&fakeSource{name: "flaky", interval: time.Second, tickErr: errors.New("read failed")},
		&fakeSource{name: "panicky", interval: time.Second, panics: true},
	)
	h.armed()
	h.expect("flaky")
	h.clk.Advance(time.Second)
	h.expect("flaky")
	errs := h.s.CollectorErrors()
	if errs["broken"] != 1 || errs["flaky"] != 2 || errs["panicky"] != 2 {
		t.Fatalf("errors %v", errs)
	}
}

func TestEffectiveInterval(t *testing.T) {
	iv := config.Default().Intervals
	s := time.Second
	cases := []struct {
		name  string
		base  time.Duration
		class state.Class
		h     state.Hints
		want  time.Duration
	}{
		{"event driven", 0, state.ClassOther, state.Hints{}, 0},
		{"idle stretch", 2 * s, state.ClassHost, state.Hints{}, 6 * s},
		{"idle capped at max", 30 * s, state.ClassOther, state.Hints{}, 60 * s},
		{"above max untouched", 5 * time.Minute, state.ClassOther, state.Hints{}, 5 * time.Minute},
		{"live keeps base", 5 * s, state.ClassOther, state.Hints{LiveViewers: true}, 5 * s},
		{"host hint", 2 * s, state.ClassHost, state.Hints{LiveViewers: true, HostInterval: s}, s},
		{"host hint clamped low", 2 * s, state.ClassHost, state.Hints{LiveViewers: true, HostInterval: 10 * time.Millisecond}, s},
		{"container hint clamped high", 5 * s, state.ClassContainers, state.Hints{LiveViewers: true, ContainerInterval: time.Hour}, 60 * s},
		{"focus uses min", 5 * s, state.ClassContainers, state.Hints{LiveViewers: true, Focus: []string{"ctr:x"}}, s},
		{"focus ignored for host", 2 * s, state.ClassHost, state.Hints{LiveViewers: true, Focus: []string{"ctr:x"}}, 2 * s},
	}
	for _, c := range cases {
		if got := EffectiveInterval(c.base, c.class, c.h, iv); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	iv.Adaptive = false
	if got := EffectiveInterval(2*s, state.ClassHost, state.Hints{LiveViewers: true, HostInterval: s}, iv); got != 2*s {
		t.Errorf("non-adaptive must ignore hints: %v", got)
	}
}

func TestFlushInterval(t *testing.T) {
	iv := config.Default().Intervals
	if d := FlushInterval(state.Hints{}, iv); d != 5*time.Second {
		t.Errorf("idle flush %v", d)
	}
	if d := FlushInterval(state.Hints{LiveViewers: true}, iv); d != 2*time.Second {
		t.Errorf("live flush %v", d)
	}
	if d := FlushInterval(state.Hints{LiveViewers: true, FlushInterval: 100 * time.Millisecond}, iv); d != time.Second {
		t.Errorf("clamped flush %v", d)
	}
}
