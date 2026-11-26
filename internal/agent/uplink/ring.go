package uplink

import (
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"google.golang.org/protobuf/proto"
)

// frameKind classifies replayable frames for the per-kind ring limits.
type frameKind uint8

const (
	kindMetrics frameKind = iota
	kindEvents
)

type ringEntry struct {
	seq    uint64
	kind   frameKind
	size   int // proto.Size of the frame
	events int // events carried (kindEvents)
	frame  *v1.AgentFrame
	sent   bool // sent on the current connection
}

// Ring buffers replayable frames until the hub acks them. Metrics frames are
// bounded by total encoded bytes and event frames by total event count; on
// overflow the oldest frame of that kind is dropped (golden rule 5). It is
// owned by the uplink goroutine and not safe for concurrent use.
type Ring struct {
	maxBytes  int
	maxEvents int

	entries       []*ringEntry // ordered by seq
	metricsBytes  int
	eventCount    int
	dropped       uint64
	droppedEvents uint64
}

// NewRing returns a ring with the given limits.
func NewRing(maxBytes, maxEvents int) *Ring {
	return &Ring{maxBytes: maxBytes, maxEvents: maxEvents}
}

// Push appends f (which must already carry its seq) and returns how many
// frames were dropped to make room.
func (r *Ring) Push(f *v1.AgentFrame) int {
	e := &ringEntry{seq: f.GetSeq(), frame: f, size: proto.Size(f)}
	if ev := f.GetEvents(); ev != nil {
		e.kind = kindEvents
		e.events = len(ev.GetEvents())
	}
	r.entries = append(r.entries, e)
	if e.kind == kindMetrics {
		r.metricsBytes += e.size
	} else {
		r.eventCount += e.events
	}
	n := 0
	for r.metricsBytes > r.maxBytes && r.dropOldest(kindMetrics) {
		n++
	}
	for r.eventCount > r.maxEvents && r.dropOldest(kindEvents) {
		n++
	}
	return n
}

func (r *Ring) dropOldest(k frameKind) bool {
	for i, e := range r.entries {
		if e.kind != k {
			continue
		}
		r.remove(i)
		r.dropped++
		if k == kindEvents {
			r.droppedEvents += uint64(e.events)
		}
		return true
	}
	return false
}

func (r *Ring) remove(i int) {
	e := r.entries[i]
	if e.kind == kindMetrics {
		r.metricsBytes -= e.size
	} else {
		r.eventCount -= e.events
	}
	copy(r.entries[i:], r.entries[i+1:])
	r.entries[len(r.entries)-1] = nil
	r.entries = r.entries[:len(r.entries)-1]
}

// Ack frees every frame with seq <= upto and returns how many were freed.
func (r *Ring) Ack(upto uint64) int {
	n := 0
	for len(r.entries) > 0 && r.entries[0].seq <= upto {
		r.remove(0)
		n++
	}
	return n
}

// ResetSent marks every buffered frame unsent (new connection: replay).
func (r *Ring) ResetSent() {
	for _, e := range r.entries {
		e.sent = false
	}
}

// NextUnsent returns the oldest frame not yet sent on this connection.
func (r *Ring) NextUnsent() (*v1.AgentFrame, int, bool) {
	for _, e := range r.entries {
		if !e.sent {
			return e.frame, e.size, true
		}
	}
	return nil, 0, false
}

// MarkSent marks the frame with seq as sent.
func (r *Ring) MarkSent(seq uint64) {
	for _, e := range r.entries {
		if e.seq == seq {
			e.sent = true
			return
		}
	}
}

// Len returns the number of buffered frames.
func (r *Ring) Len() int { return len(r.entries) }

// Bytes returns the encoded size of every buffered frame.
func (r *Ring) Bytes() int {
	n := 0
	for _, e := range r.entries {
		n += e.size
	}
	return n
}

// Dropped returns the number of frames dropped on overflow.
func (r *Ring) Dropped() uint64 { return r.dropped }

// DroppedEvents returns the number of events lost with dropped frames.
func (r *Ring) DroppedEvents() uint64 { return r.droppedEvents }
