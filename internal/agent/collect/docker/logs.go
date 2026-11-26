package docker

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/dockerapi"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// logBatch is the number of lines per LogChunk.
const logBatch = 50

// Logs serves an on-demand LogRequest for a container workload
// ("ctr:<name>"). It implements the uplink's log handler contract: tail is
// capped at sources.logs.max_lines, follow at sources.logs.max_follow,
// output at sources.logs.rate_limit, and every line is secret-redacted on
// the agent before it is sent.
func (s *Source) Logs(ctx context.Context, req *v1.LogRequest, send func(*v1.LogChunk) bool) {
	rid := req.GetRequestId()
	fail := func(msg string) { send(&v1.LogChunk{RequestId: rid, Eof: true, Error: msg}) }
	if !s.opts.Logs.Docker {
		fail("docker logs are disabled on this agent (sources.logs.docker)")
		return
	}
	name, ok := strings.CutPrefix(req.GetWorkloadId(), "ctr:")
	if !ok {
		fail("not a container workload")
		return
	}
	s.mu.Lock()
	var fullID string
	var tty bool
	for id, c := range s.byID {
		if c.Name == name {
			fullID, tty = id, s.tty[id]
			break
		}
	}
	s.mu.Unlock()
	if fullID == "" {
		fail("container not found on this agent")
		return
	}
	maxLines := s.opts.Logs.MaxLines
	if maxLines <= 0 {
		maxLines = 500
	}
	tail := int(req.GetTail())
	if tail <= 0 || tail > maxLines {
		tail = maxLines
	}
	o := dockerapi.LogOptions{Tail: tail, Follow: req.GetFollow()}
	if ms := req.GetSinceMs(); ms > 0 {
		o.Since = time.UnixMilli(ms)
	}
	if o.Follow {
		limit := s.opts.Logs.MaxFollow.D()
		if limit <= 0 {
			limit = 10 * time.Minute
		}
		if sec := req.GetMaxSeconds(); sec > 0 && time.Duration(sec)*time.Second < limit {
			limit = time.Duration(sec) * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	body, err := s.opts.Client.Logs(ctx, fullID, o)
	if err != nil {
		fail("docker logs: " + err.Error())
		return
	}
	defer body.Close()
	stop := context.AfterFunc(ctx, func() { body.Close() })
	defer stop()

	grep := req.GetGrep()
	rate := float64(s.opts.Logs.RateLimit)
	if rate <= 0 {
		rate = 64 << 10
	}
	start := time.Now()
	var sent float64
	var batch []*v1.LogLine
	open := true
	flush := func(eof bool) {
		if !open || (len(batch) == 0 && !eof) {
			return
		}
		if !send(&v1.LogChunk{RequestId: rid, Lines: batch, Eof: eof}) {
			open = false
		}
		batch = nil
	}
	emit := func(stream string, line []byte) error {
		ts, text := dockerapi.SplitTimestamp(line)
		t := dockercli.RedactText(string(text))
		if grep != "" && !strings.Contains(t, grep) {
			return nil
		}
		sent += float64(len(t))
		// Rate limit: stop once the budget for the elapsed time is spent.
		if budget := rate * max(time.Since(start).Seconds(), 1); sent > budget {
			flush(false)
			return io.EOF
		}
		ll := &v1.LogLine{TsMs: ts.UnixMilli(), WorkloadId: req.GetWorkloadId(), Stream: stream, Text: t}
		batch = append(batch, ll)
		if len(batch) >= logBatch {
			flush(false)
		}
		if !open {
			return io.EOF
		}
		return nil
	}
	if tty {
		err = dockerapi.SplitRaw(body, func(l []byte) error { return emit("stdout", l) })
	} else {
		err = dockerapi.Demux(body, func(st byte, l []byte) error {
			name := "stdout"
			if st == dockerapi.StreamStderr {
				name = "stderr"
			}
			return emit(name, l)
		})
	}
	if err != nil && err != io.EOF && ctx.Err() == nil {
		flush(false)
		send(&v1.LogChunk{RequestId: rid, Eof: true, Error: "log stream: " + err.Error()})
		return
	}
	flush(true)
}
