package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Stream identifiers in the multiplexed log format.
const (
	StreamStdout = 1
	StreamStderr = 2
)

// MaxLogLine caps one log line (longer lines are split).
const MaxLogLine = 16 << 10

// LogOptions selects container log output.
type LogOptions struct {
	Tail   int       // 0 = all; callers cap this (agent: ≤ 500)
	Since  time.Time // zero = from the beginning
	Follow bool
}

// Logs opens GET /containers/{id}/logs with timestamps. The caller closes
// the returned body. Use Demux for non-TTY containers (multiplexed) and
// SplitRaw for TTY containers.
func (c *Client) Logs(ctx context.Context, id string, o LogOptions) (io.ReadCloser, error) {
	if !validRef(id) {
		return nil, errors.New("dockerapi: invalid container reference")
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if o.Tail > 0 {
		q.Set("tail", strconv.Itoa(o.Tail))
	}
	if !o.Since.IsZero() {
		q.Set("since", strconv.FormatInt(o.Since.Unix(), 10))
	}
	hc := c.hc
	if o.Follow {
		q.Set("follow", "1")
		hc = c.stream
	}
	resp, err := c.do(ctx, hc, http.MethodGet, "/containers/"+id+"/logs", q)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Demux reads Docker's multiplexed stream (8-byte header: stream, 3 zero
// bytes, big-endian payload length) and calls fn once per line. A frame
// larger than MaxLogLine is split; a trailing partial line is flushed at EOF.
func Demux(r io.Reader, fn func(stream byte, line []byte) error) error {
	var hdr [8]byte
	pending := map[byte][]byte{}
	flush := func(s byte, final bool) error {
		buf := pending[s]
		for {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				break
			}
			if err := fn(s, buf[:i]); err != nil {
				return err
			}
			buf = buf[i+1:]
		}
		for len(buf) >= MaxLogLine {
			if err := fn(s, buf[:MaxLogLine]); err != nil {
				return err
			}
			buf = buf[MaxLogLine:]
		}
		if final && len(buf) > 0 {
			if err := fn(s, buf); err != nil {
				return err
			}
			buf = nil
		}
		pending[s] = append(pending[s][:0], buf...)
		return nil
	}
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				for _, s := range []byte{StreamStdout, StreamStderr} {
					if err := flush(s, true); err != nil {
						return err
					}
				}
				return nil
			}
			return err
		}
		s := hdr[0]
		if s != StreamStdout && s != StreamStderr {
			if s == 0 { // stdin echo; skip payload
				s = StreamStdout
			} else {
				return errors.New("dockerapi: not a multiplexed log stream (TTY container?)")
			}
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if n > 1<<20 {
			return errors.New("dockerapi: log frame too large")
		}
		start := len(pending[s])
		pending[s] = append(pending[s], make([]byte, n)...)
		if _, err := io.ReadFull(r, pending[s][start:]); err != nil {
			return err
		}
		if err := flush(s, false); err != nil {
			return err
		}
	}
}

// SplitRaw reads a TTY container's raw log stream line by line.
func SplitRaw(r io.Reader, fn func(line []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), MaxLogLine)
	for sc.Scan() {
		if err := fn(bytes.TrimRight(sc.Bytes(), "\r")); err != nil {
			return err
		}
	}
	return sc.Err()
}

// SplitTimestamp splits a "2026-11-24T22:03:10.000000000Z text" log line.
func SplitTimestamp(line []byte) (time.Time, []byte) {
	sp := bytes.IndexByte(line, ' ')
	if sp <= 0 {
		return time.Time{}, line
	}
	t, err := time.Parse(time.RFC3339Nano, string(line[:sp]))
	if err != nil {
		return time.Time{}, line
	}
	return t, line[sp+1:]
}
