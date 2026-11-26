package uplink

import (
	"context"
	"sync/atomic"
	"time"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// CommandHandler executes one hub command (actions.Service implements it).
type CommandHandler interface {
	Handle(ctx context.Context, req *v1.CommandRequest) *v1.CommandResult
}

// LogHandler serves one on-demand log request. It calls send for every
// chunk (send returns false once the request was cancelled or the connection
// dropped) and returns when done. The final chunk should set eof.
type LogHandler interface {
	Logs(ctx context.Context, req *v1.LogRequest, send func(*v1.LogChunk) bool)
}

// DedupeWindow is how many recent request ids the agent remembers.
const DedupeWindow = 64

// recentIDs is a fixed-size FIFO set of request ids.
type recentIDs struct {
	ids  [DedupeWindow]string
	next int
	set  map[string]struct{}
}

func newRecentIDs() *recentIDs { return &recentIDs{set: make(map[string]struct{}, DedupeWindow)} }

// add records id and reports whether it was new.
func (r *recentIDs) add(id string) bool {
	if _, ok := r.set[id]; ok {
		return false
	}
	if old := r.ids[r.next]; old != "" {
		delete(r.set, old)
	}
	r.ids[r.next] = id
	r.next = (r.next + 1) % DedupeWindow
	r.set[id] = struct{}{}
	return true
}

// outMsg is a frame produced off the uplink goroutine for the connection
// generation gen. Frames for an older generation are dropped: command
// results and log chunks are never replayed on a later connection.
type outMsg struct {
	gen   uint64
	frame *v1.AgentFrame
}

type job struct {
	gen uint64
	req *v1.CommandRequest
}

// dispatcher runs commands one at a time on a worker goroutine.
type dispatcher struct {
	handler CommandHandler
	seen    *recentIDs
	queue   chan job
	out     chan<- outMsg
	gen     *atomic.Uint64
	now     func() time.Time
	onStart func(*v1.CommandRequest)
}

const commandQueue = 8

func newDispatcher(h CommandHandler, out chan<- outMsg, gen *atomic.Uint64, now func() time.Time) *dispatcher {
	return &dispatcher{handler: h, seen: newRecentIDs(), queue: make(chan job, commandQueue), out: out, gen: gen, now: now}
}

// submit is called on the uplink goroutine. Refusals are answered at once.
func (d *dispatcher) submit(gen uint64, req *v1.CommandRequest) *v1.CommandResult {
	refuse := func(code, msg string) *v1.CommandResult {
		return &v1.CommandResult{RequestId: req.GetRequestId(), Code: code, Error: msg}
	}
	switch {
	case req.GetRequestId() == "":
		return refuse("invalid_argument", "command has no request_id")
	case !d.seen.add(req.GetRequestId()):
		return refuse("already_exists", "duplicate request_id: the command already ran or is running")
	case req.GetDeadlineMs() != 0 && d.now().UnixMilli() > req.GetDeadlineMs():
		return refuse("deadline_exceeded", "command deadline passed before the agent received it")
	case d.handler == nil:
		return refuse("unimplemented", "this agent does not execute commands")
	}
	select {
	case d.queue <- job{gen: gen, req: req}:
		return nil
	default:
		return refuse("resource_exhausted", "too many commands in flight; retry later")
	}
}

// run executes queued commands until ctx is done. A command queued on a
// connection that has since dropped is not started.
func (d *dispatcher) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.queue:
			var res *v1.CommandResult
			switch {
			case j.gen != d.gen.Load():
				continue
			case j.req.GetDeadlineMs() != 0 && d.now().UnixMilli() > j.req.GetDeadlineMs():
				res = &v1.CommandResult{RequestId: j.req.GetRequestId(), Code: "deadline_exceeded", Error: "command deadline passed while queued"}
			default:
				if d.onStart != nil {
					d.onStart(j.req)
				}
				res = d.handler.Handle(ctx, j.req)
			}
			f := &v1.AgentFrame{SentAtMs: d.now().UnixMilli(), Payload: &v1.AgentFrame_CommandResult{CommandResult: res}}
			select {
			case d.out <- outMsg{gen: j.gen, frame: f}:
			case <-ctx.Done():
				return
			}
		}
	}
}
