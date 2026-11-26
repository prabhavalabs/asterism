// Package eventlog keeps a bounded, in-memory log of service-generated events
// (container lifecycle transitions and action audit records).
package eventlog

import (
	"sync"

	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Log is a fixed-capacity ring of events; the oldest entry is dropped on overflow.
type Log struct {
	mu       sync.RWMutex
	capacity int
	items    []*asterismv1.Event
	head     int
	count    int
}

// New returns a log holding at most capacity events.
func New(capacity int) *Log {
	if capacity <= 0 {
		capacity = 1000
	}
	return &Log{capacity: capacity, items: make([]*asterismv1.Event, capacity)}
}

// Append stores a copy of the event.
func (l *Log) Append(e *asterismv1.Event) {
	if e == nil {
		return
	}
	c := proto.Clone(e).(*asterismv1.Event)
	l.mu.Lock()
	idx := (l.head + l.count) % l.capacity
	l.items[idx] = c
	if l.count < l.capacity {
		l.count++
	} else {
		l.head = (l.head + 1) % l.capacity
	}
	l.mu.Unlock()
}

// List returns events newest first, optionally filtered by entity id and capped at limit (0 = all).
func (l *Log) List(entityID string, limit int) []*asterismv1.Event {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*asterismv1.Event, 0, min(l.count, max(limit, 16)))
	for i := l.count - 1; i >= 0; i-- {
		e := l.items[(l.head+i)%l.capacity]
		if entityID != "" && e.EntityId != entityID {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Len returns the number of stored events.
func (l *Log) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.count
}
