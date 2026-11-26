// Package sched runs agent Sources from a single goroutine and a timer heap.
//
// Every interval-driven source ticks sequentially on the scheduler goroutine
// (no goroutine per collector or per container). Event-driven sources that
// implement state.Runner get one goroutine each for their Run method.
// Intervals adapt to hub hints, always clamped to the local YAML bounds.
package sched

import (
	"container/heap"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

// SlowTick is the tick duration above which a warning is logged.
const SlowTick = 50 * time.Millisecond

// Options configures a Scheduler.
type Options struct {
	Clock     Clock
	Intervals config.Intervals
	Logger    *slog.Logger
	// Env is the base environment handed to Source.Init (Logger and Now are
	// filled in per source).
	Env state.Env
}

// Scheduler owns the collector loop.
type Scheduler struct {
	store *state.Store
	clock Clock
	iv    config.Intervals
	log   *slog.Logger
	env   state.Env

	entries []*entry
	hintsCh chan state.Hints
	hints   state.Hints

	mu     sync.Mutex
	errors map[string]uint32
}

type entry struct {
	src      state.Source
	sink     *state.Handle
	class    state.Class
	base     time.Duration
	interval time.Duration
	last     time.Time
	due      time.Time
	index    int
	disabled bool
	errLogs  uint32
}

// New returns a Scheduler writing into store.
func New(store *state.Store, opts Options) *Scheduler {
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Scheduler{
		store:   store,
		clock:   opts.Clock,
		iv:      opts.Intervals,
		log:     opts.Logger,
		env:     opts.Env,
		hintsCh: make(chan state.Hints, 1),
		errors:  map[string]uint32{},
	}
}

// Add registers a source. Call before Init.
func (s *Scheduler) Add(src state.Source) {
	e := &entry{src: src, sink: s.store.Sink(src.Name()), base: src.Interval()}
	if c, ok := src.(state.Classed); ok {
		e.class = c.Class()
	}
	s.entries = append(s.entries, e)
}

// Init calls Init on every source and returns the union of capabilities.
// A source whose Init fails is disabled and counted in CollectorErrors.
func (s *Scheduler) Init(ctx context.Context) []string {
	caps := map[string]struct{}{}
	for _, e := range s.entries {
		env := s.env
		env.Logger = s.log.With("source", e.src.Name())
		env.Now = s.clock.Now
		c, err := safeInit(ctx, e.src, env)
		if err != nil {
			e.disabled = true
			s.countError(e.src.Name())
			s.log.Warn("collector disabled", "source", e.src.Name(), "err", err)
			continue
		}
		for _, name := range c {
			caps[name] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(caps))
}

func safeInit(ctx context.Context, src state.Source, env state.Env) (caps []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("init panicked: %v", r)
		}
	}()
	return src.Init(ctx, env)
}

// SetHints updates the hub hints; the latest value wins. Safe from any
// goroutine; never blocks.
func (s *Scheduler) SetHints(h state.Hints) {
	for {
		select {
		case s.hintsCh <- h:
			return
		default:
		}
		select {
		case <-s.hintsCh:
		default:
		}
	}
}

// CollectorErrors returns per-source error counts.
func (s *Scheduler) CollectorErrors() map[string]uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.errors)
}

func (s *Scheduler) countError(name string) {
	s.mu.Lock()
	s.errors[name]++
	s.mu.Unlock()
}

// Run ticks sources until ctx is cancelled. Init must have been called.
func (s *Scheduler) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, e := range s.entries {
		if e.disabled {
			continue
		}
		if r, ok := e.src.(state.Runner); ok {
			sink := s.store.Sink(e.src.Name() + ".run")
			wg.Go(func() {
				if err := r.Run(ctx, sink); err != nil && ctx.Err() == nil {
					s.countError(e.src.Name())
					s.log.Warn("collector run loop stopped", "source", e.src.Name(), "err", err)
				}
			})
		}
	}
	defer wg.Wait()

	now := s.clock.Now()
	h := &entryHeap{}
	for _, e := range s.entries {
		if e.disabled || e.base <= 0 {
			continue
		}
		e.interval = EffectiveInterval(e.base, e.class, s.hints, s.iv)
		e.due = now // first tick immediately
		heap.Push(h, e)
	}
	timer := s.clock.NewTimer(s.wait(h, now))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case hints := <-s.hintsCh:
			s.applyHints(h, hints)
		case <-timer.C():
		}
		now = s.clock.Now()
		for h.Len() > 0 && !(*h)[0].due.After(now) {
			e := (*h)[0]
			s.tick(e, now)
			e.last = now
			e.due = e.due.Add(e.interval)
			if !e.due.After(now) {
				e.due = now.Add(e.interval)
			}
			heap.Fix(h, 0)
			if ctx.Err() != nil {
				return nil
			}
		}
		timer.Reset(s.wait(h, now))
	}
}

const idleWait = 24 * time.Hour

func (s *Scheduler) wait(h *entryHeap, now time.Time) time.Duration {
	if h.Len() == 0 {
		return idleWait
	}
	return max((*h)[0].due.Sub(now), 0)
}

func (s *Scheduler) applyHints(h *entryHeap, hints state.Hints) {
	s.hints = hints
	for _, e := range *h {
		if ha, ok := e.src.(state.HintAware); ok {
			ha.ApplyHints(hints)
		}
		next := EffectiveInterval(e.base, e.class, hints, s.iv)
		if next == e.interval {
			continue
		}
		e.interval = next
		if !e.last.IsZero() {
			if d := e.last.Add(next); d.Before(e.due) {
				e.due = d
			}
		}
	}
	heap.Init(h)
}

func (s *Scheduler) tick(e *entry, now time.Time) {
	start := s.clock.Now()
	err := safeTick(e.src, now, e.sink)
	e.sink.Commit(now, err == nil)
	if d := s.clock.Now().Sub(start); d > SlowTick {
		s.log.Warn("slow collector tick", "source", e.src.Name(), "duration", d)
	}
	if err != nil {
		s.countError(e.src.Name())
		e.errLogs++
		if e.errLogs == 1 || e.errLogs%100 == 0 {
			s.log.Warn("collector tick failed", "source", e.src.Name(), "err", err, "failures", e.errLogs)
		}
	}
}

func safeTick(src state.Source, now time.Time, sink state.Sink) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tick panicked: %v", r)
		}
	}()
	return src.Tick(now, sink)
}

// EffectiveInterval applies hub hints and idle stretching to a base
// interval, clamped to [iv.Min, iv.Max]. A base of 0 (event-driven) stays 0,
// and bases already above iv.Max (Docker resources, disk usage) are never
// changed.
func EffectiveInterval(base time.Duration, class state.Class, h state.Hints, iv config.Intervals) time.Duration {
	if base <= 0 || !iv.Adaptive {
		return base
	}
	lo, hi := iv.Min.D(), iv.Max.D()
	if base > hi {
		return base
	}
	clamp := func(d time.Duration) time.Duration { return min(max(d, lo), hi) }
	d := base
	if h.LiveViewers {
		switch class {
		case state.ClassHost:
			if h.HostInterval > 0 {
				d = clamp(h.HostInterval)
			}
		case state.ClassContainers:
			switch {
			case len(h.Focus) > 0:
				d = lo
			case h.ContainerInterval > 0:
				d = clamp(h.ContainerInterval)
			}
		}
		return d
	}
	if iv.IdleFactor > 1 {
		d = clamp(time.Duration(float64(d) * iv.IdleFactor))
	}
	return d
}

// FlushInterval is the uplink flush cadence for the given hints: the hub
// hint (clamped to [min, max]) while viewers are live, 2 s when live without
// a hint, otherwise the configured flush interval.
func FlushInterval(h state.Hints, iv config.Intervals) time.Duration {
	base := iv.Flush.D()
	if !h.LiveViewers || !iv.Adaptive {
		return base
	}
	if h.FlushInterval > 0 {
		return min(max(h.FlushInterval, iv.Min.D()), iv.Max.D())
	}
	return min(base, 2*time.Second)
}

type entryHeap []*entry

func (h entryHeap) Len() int           { return len(h) }
func (h entryHeap) Less(i, j int) bool { return h[i].due.Before(h[j].due) }
func (h entryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *entryHeap) Push(x any) {
	e := x.(*entry)
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *entryHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}
