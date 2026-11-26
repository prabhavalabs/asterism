package agentingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// FrameStream is the hub end of AgentService.Stream; connect's
// *BidiStream[AgentFrame, HubFrame] implements it.
type FrameStream interface {
	Receive() (*asterismv1.AgentFrame, error)
	Send(*asterismv1.HubFrame) error
}

// maxPushFrames bounds one unary Push.
const maxPushFrames = 4096

// ServeStream runs one bidi connection for host (the agent token's bound
// host) until the agent hangs up, the connection is superseded or goes
// silent, or ctx ends.
func (r *Registry) ServeStream(ctx context.Context, host string, s FrameStream, remote string) error {
	frames := make(chan *asterismv1.AgentFrame)
	errs := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			f, err := s.Receive()
			if err != nil {
				errs <- err
				return
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
		}
	}()

	hello := time.NewTimer(r.opts.HandshakeAfter)
	defer hello.Stop()
	var first *asterismv1.AgentFrame
	select {
	case first = <-frames:
	case err := <-errs:
		return quietEOF(err)
	case <-hello.C:
		return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("no hello within %s", r.opts.HandshakeAfter))
	case <-ctx.Done():
		return nil
	}
	hs, c, ack, err := r.attach(host, first, TransportBidi, remote)
	if err != nil {
		r.log.Warn("agent handshake refused", "host", host, "remote", remote, "err", err)
		return err
	}
	var endErr error
	defer func() { r.detach(hs, c, endErr) }()
	if err := s.Send(&asterismv1.HubFrame{Payload: &asterismv1.HubFrame_HelloAck{HelloAck: ack}}); err != nil {
		endErr = err
		return err
	}

	ackTick := time.NewTicker(r.opts.AckEvery)
	defer ackTick.Stop()
	for {
		select {
		case f := <-frames:
			replies, err := r.ingest(hs, c, f)
			if err != nil {
				endErr = err
				r.log.Warn("agent connection closed by hub", "host", host, "err", err)
				return err
			}
			for _, rf := range replies {
				if err := s.Send(rf); err != nil {
					endErr = err
					return err
				}
			}
		case err := <-errs:
			endErr = quietEOF(err)
			if endErr == nil {
				endErr = errors.New("agent closed the stream")
				return nil
			}
			return endErr
		case f := <-c.out:
			if err := s.Send(f); err != nil {
				endErr = err
				return err
			}
		case <-ackTick.C:
			if a := r.pendingAck(hs, c); a != nil {
				if err := s.Send(a); err != nil {
					endErr = err
					return err
				}
			}
		case <-c.done:
			endErr = c.err
			return connect.NewError(connect.CodeAborted, c.err)
		case <-ctx.Done():
			endErr = errors.New("hub shutting down")
			return nil
		}
	}
}

func quietEOF(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Push serves the unary fallback: a batch of frames, answered with the
// highest processed seq plus piggy-backed hub frames. The first Push of a
// connection carries exactly one hello.
func (r *Registry) Push(ctx context.Context, host string, req *asterismv1.PushRequest, remote string) (*asterismv1.PushResponse, error) {
	if len(req.GetFrames()) > maxPushFrames {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("push carries %d frames (limit %d)", len(req.GetFrames()), maxPushFrames))
	}
	resp := &asterismv1.PushResponse{}
	var hs *hostState
	var c *conn
	for _, f := range req.GetFrames() {
		if f.GetHello() != nil {
			var ack *asterismv1.HelloAck
			var err error
			hs, c, ack, err = r.attach(host, f, TransportUnary, remote)
			if err != nil {
				r.log.Warn("agent handshake refused", "host", host, "remote", remote, "err", err)
				return nil, err
			}
			if f.GetHello().GetSessionId() != req.GetSessionId() {
				r.detach(hs, c, errors.New("push session_id differs from hello.session_id"))
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("push session_id must equal hello.session_id"))
			}
			resp.Frames = append(resp.Frames, &asterismv1.HubFrame{Payload: &asterismv1.HubFrame_HelloAck{HelloAck: ack}})
			continue
		}
		if c == nil {
			var err error
			if hs, c, err = r.unaryConn(host, req.GetSessionId()); err != nil {
				return nil, err
			}
		}
		replies, err := r.ingest(hs, c, f)
		if err != nil {
			r.detach(hs, c, err)
			return nil, err
		}
		for _, rf := range replies {
			if rf.GetAck() == nil {
				resp.Frames = append(resp.Frames, rf)
			}
		}
	}
	if hs != nil {
		hs.mu.Lock()
		resp.AckedSeq = hs.lastSeq
		if c != nil {
			c.unacked, c.lastAcked = 0, hs.lastSeq
		}
		hs.mu.Unlock()
	}
	return resp, nil
}

// unaryConn finds the attached unary connection of a session.
func (r *Registry) unaryConn(host, session string) (*hostState, *conn, error) {
	hs, ok := r.hosts[host]
	if !ok {
		return nil, nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("agent token for %q is not enrolled on this hub", host))
	}
	hs.mu.Lock()
	c := hs.conn
	hs.mu.Unlock()
	if c == nil || c.transport != TransportUnary || c.sessionID != session {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no unary session for this session_id: push a hello first"))
	}
	return hs, c, nil
}

// Commands serves hub-to-agent frames for a unary session. The session ends
// when this stream ends.
func (r *Registry) Commands(ctx context.Context, host string, req *asterismv1.CommandsRequest, send func(*asterismv1.HubFrame) error) error {
	hs, c, err := r.unaryConn(host, req.GetSessionId())
	if err != nil {
		return err
	}
	var endErr error
	defer func() { r.detach(hs, c, endErr) }()
	// The first message flushes response headers so the agent's call returns.
	if err := send(&asterismv1.HubFrame{Payload: &asterismv1.HubFrame_Hints{Hints: r.Hints()}}); err != nil {
		endErr = err
		return err
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case f := <-c.out:
			if err := send(f); err != nil {
				endErr = err
				return err
			}
		case t := <-ping.C:
			if err := send(&asterismv1.HubFrame{Payload: &asterismv1.HubFrame_Ping{Ping: &asterismv1.Heartbeat{TsMs: t.UnixMilli()}}}); err != nil {
				endErr = err
				return err
			}
		case <-c.done:
			endErr = c.err
			return connect.NewError(connect.CodeAborted, c.err)
		case <-ctx.Done():
			endErr = errors.New("commands stream closed")
			return nil
		}
	}
}
