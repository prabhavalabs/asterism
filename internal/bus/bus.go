package bus

import (
	"sync"
	"sync/atomic"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// MetricSample is a single point emitted by a collector.
type MetricSample struct {
	EntityID string
	Metric   string
	TSMs     int64
	Value    float64
}

// Message is the typed envelope published on the in-process bus.
type Message struct {
	Samples []MetricSample
	// Events are lifecycle, alert, and audit events pushed to live subscribers.
	Events []*asterismv1.Event
}

type subscriber struct {
	ch chan Message
}

// Bus fans out collector messages to bounded subscriber channels without ever blocking publishers.
type Bus struct {
	mu      sync.RWMutex
	subs    map[uint64]*subscriber
	nextID  uint64
	bufSize int
	dropped atomic.Uint64
}

func New(bufSize int) *Bus {
	if bufSize <= 0 {
		bufSize = 64
	}
	return &Bus{
		subs:    make(map[uint64]*subscriber),
		bufSize: bufSize,
	}
}

// Subscribe returns a receive channel and an unsubscribe callback.
func (b *Bus) Subscribe() (<-chan Message, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	sub := &subscriber{ch: make(chan Message, b.bufSize)}
	b.subs[id] = sub

	return sub.ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// Publish delivers a message to every subscriber. Slow subscribers drop the oldest queued message.
func (b *Bus) Publish(msg Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, sub := range b.subs {
		select {
		case sub.ch <- msg:
		default:
			// Drop oldest message to guarantee non-blocking collector delivery.
			select {
			case <-sub.ch:
				b.dropped.Add(1)
			default:
			}
			select {
			case sub.ch <- msg:
			default:
				b.dropped.Add(1)
			}
		}
	}
}

// Dropped returns the cumulative count of dropped messages due to slow subscribers.
func (b *Bus) Dropped() uint64 {
	return b.dropped.Load()
}
