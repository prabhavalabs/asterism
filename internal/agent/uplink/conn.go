package uplink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"google.golang.org/protobuf/proto"
)

// conn is one transport connection. send and handshake are called only from
// the uplink goroutine; recv and errc are fed by the receive goroutine.
type conn interface {
	name() string
	handshake(ctx context.Context, hello *v1.AgentFrame, timeout time.Duration) (*v1.HelloAck, []*v1.HubFrame, error)
	// send transmits frames. Unary returns hub frames carried in the
	// responses (including a synthetic Ack for acked_seq).
	send(ctx context.Context, frames []*v1.AgentFrame) ([]*v1.HubFrame, error)
	recv() <-chan *v1.HubFrame
	errc() <-chan error
	close()
}

// ErrHandshakeTimeout means the hub did not answer Hello in time (for bidi
// this is typical of a proxy that buffers request bodies).
var ErrHandshakeTimeout = errors.New("hub did not answer hello in time")

// receiver runs one receive loop and stops on close.
type receiver struct {
	frames chan *v1.HubFrame
	errs   chan error
	done   chan struct{}
	once   sync.Once
}

func newReceiver() *receiver {
	return &receiver{frames: make(chan *v1.HubFrame, 16), errs: make(chan error, 1), done: make(chan struct{})}
}

func (r *receiver) run(ctx context.Context, next func() (*v1.HubFrame, error)) {
	go func() {
		defer close(r.done)
		for {
			f, err := next()
			if err != nil {
				select {
				case r.errs <- err:
				default:
				}
				return
			}
			select {
			case r.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ---------------------------------------------------------------------------
// Bidi
// ---------------------------------------------------------------------------

type bidiConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream *connect.BidiStreamForClient[v1.AgentFrame, v1.HubFrame]
	rx     *receiver
	start  bool
}

func (c *Client) newBidi(ctx context.Context) conn {
	cctx, cancel := context.WithCancel(ctx)
	return &bidiConn{ctx: cctx, cancel: cancel, stream: c.bidiClient.Stream(cctx), rx: newReceiver()}
}

func (b *bidiConn) name() string { return TransportBidi }

func (b *bidiConn) handshake(_ context.Context, hello *v1.AgentFrame, timeout time.Duration) (*v1.HelloAck, []*v1.HubFrame, error) {
	timedOut := false
	var mu sync.Mutex
	t := time.AfterFunc(timeout, func() {
		mu.Lock()
		timedOut = true
		mu.Unlock()
		b.cancel()
	})
	defer t.Stop()
	if err := b.stream.Send(hello); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, b.handshakeErr(err, &mu, &timedOut)
	}
	f, err := b.stream.Receive()
	if err != nil {
		return nil, nil, b.handshakeErr(err, &mu, &timedOut)
	}
	if !t.Stop() {
		mu.Lock()
		to := timedOut
		mu.Unlock()
		if to {
			return nil, nil, ErrHandshakeTimeout
		}
	}
	ack := f.GetHelloAck()
	if ack == nil {
		return nil, nil, fmt.Errorf("hub answered hello with %T instead of hello_ack", f.GetPayload())
	}
	b.start = true
	b.rx.run(b.ctx, b.stream.Receive)
	return ack, nil, nil
}

func (b *bidiConn) handshakeErr(err error, mu *sync.Mutex, timedOut *bool) error {
	mu.Lock()
	defer mu.Unlock()
	if *timedOut {
		return ErrHandshakeTimeout
	}
	return err
}

func (b *bidiConn) send(_ context.Context, frames []*v1.AgentFrame) ([]*v1.HubFrame, error) {
	for _, f := range frames {
		if err := b.stream.Send(f); err != nil {
			if errors.Is(err, io.EOF) {
				// The server closed the stream; the real status is on Receive.
				select {
				case e := <-b.rx.errs:
					return nil, e
				default:
				}
			}
			return nil, err
		}
	}
	return nil, nil
}

func (b *bidiConn) recv() <-chan *v1.HubFrame { return b.rx.frames }
func (b *bidiConn) errc() <-chan error        { return b.rx.errs }

func (b *bidiConn) close() {
	b.rx.once.Do(func() {
		_ = b.stream.CloseRequest()
		b.cancel()
		_ = b.stream.CloseResponse()
		if b.start {
			<-b.rx.done
		}
	})
}

// ---------------------------------------------------------------------------
// Unary Push + Commands
// ---------------------------------------------------------------------------

type unaryConn struct {
	ctx       context.Context
	cancel    context.CancelFunc
	client    asterismv1connect.AgentServiceClient
	sessionID string
	maxBytes  int
	rx        *receiver
	started   bool
	stream    *connect.ServerStreamForClient[v1.HubFrame] // owned by the receive goroutine
}

func (c *Client) newUnary(ctx context.Context) conn {
	cctx, cancel := context.WithCancel(ctx)
	return &unaryConn{ctx: cctx, cancel: cancel, client: c.unaryCl, sessionID: c.sessionID, maxBytes: c.opts.MaxPushBytes, rx: newReceiver()}
}

func (u *unaryConn) name() string { return TransportUnary }

const pushTimeout = 30 * time.Second

func (u *unaryConn) push(ctx context.Context, frames []*v1.AgentFrame, timeout time.Duration) (*v1.PushResponse, error) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := u.client.Push(pctx, connect.NewRequest(&v1.PushRequest{SessionId: u.sessionID, Frames: frames}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (u *unaryConn) handshake(ctx context.Context, hello *v1.AgentFrame, timeout time.Duration) (*v1.HelloAck, []*v1.HubFrame, error) {
	resp, err := u.push(u.ctx, []*v1.AgentFrame{hello}, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, ErrHandshakeTimeout
		}
		return nil, nil, err
	}
	var ack *v1.HelloAck
	var rest []*v1.HubFrame
	for _, f := range resp.GetFrames() {
		if a := f.GetHelloAck(); a != nil && ack == nil {
			ack = a
			continue
		}
		rest = append(rest, f)
	}
	if ack == nil {
		return nil, nil, errors.New("hub answered the hello push without a hello_ack")
	}
	// Commands blocks until the hub sends response headers (connect handlers
	// flush them with the first message), so open it on the receive goroutine.
	u.started = true
	u.rx.run(u.ctx, func() (*v1.HubFrame, error) {
		if u.stream == nil {
			stream, err := u.client.Commands(u.ctx, connect.NewRequest(&v1.CommandsRequest{SessionId: u.sessionID}))
			if err != nil {
				return nil, fmt.Errorf("open commands stream: %w", err)
			}
			u.stream = stream
		}
		if u.stream.Receive() {
			return u.stream.Msg(), nil
		}
		if err := u.stream.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("hub closed the commands stream")
	})
	return ack, rest, nil
}

func (u *unaryConn) send(ctx context.Context, frames []*v1.AgentFrame) ([]*v1.HubFrame, error) {
	var back []*v1.HubFrame
	for len(frames) > 0 {
		n, size := 0, 0
		for n < len(frames) {
			s := proto.Size(frames[n])
			if n > 0 && size+s > u.maxBytes {
				break
			}
			size += s
			n++
		}
		resp, err := u.push(u.ctx, frames[:n], pushTimeout)
		if err != nil {
			return back, err
		}
		if a := resp.GetAckedSeq(); a > 0 {
			back = append(back, &v1.HubFrame{Payload: &v1.HubFrame_Ack{Ack: &v1.Ack{UptoSeq: a}}})
		}
		back = append(back, resp.GetFrames()...)
		frames = frames[n:]
	}
	return back, nil
}

func (u *unaryConn) recv() <-chan *v1.HubFrame { return u.rx.frames }
func (u *unaryConn) errc() <-chan error        { return u.rx.errs }

func (u *unaryConn) close() {
	u.rx.once.Do(func() {
		u.cancel()
		if u.started {
			<-u.rx.done
			if u.stream != nil {
				_ = u.stream.Close()
			}
		}
	})
}
