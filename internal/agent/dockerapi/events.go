package dockerapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// MaxEventBytes caps a single /events JSON message.
const MaxEventBytes = 256 << 10

// containerEvents are the lifecycle actions the agent subscribes to; exec_*
// and attach noise is filtered out server-side.
var containerEvents = []string{
	"create", "start", "restart", "stop", "die", "kill", "oom", "pause", "unpause",
	"destroy", "rename", "update", "health_status",
}

// EventsFilter is the JSON filters parameter for container lifecycle events.
func EventsFilter() string {
	b, _ := json.Marshal(map[string][]string{"type": {"container"}, "event": containerEvents})
	return string(b)
}

// Events opens GET /events (container lifecycle only) since the given time
// (zero = now) and calls fn with each raw JSON message until the stream ends,
// ctx is cancelled, or fn returns an error.
func (c *Client) Events(ctx context.Context, since time.Time, fn func(raw []byte) error) error {
	q := url.Values{"filters": {EventsFilter()}}
	if !since.IsZero() {
		q.Set("since", strconv.FormatInt(since.Unix(), 10))
	}
	resp, err := c.do(ctx, c.stream, http.MethodGet, "/events", q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 16<<10), MaxEventBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

// EventLoop keeps an event stream open with reconnects.
type EventLoop struct {
	Client *Client
	// OnEvent receives each raw message.
	OnEvent func(raw []byte)
	// OnReconnect runs before each reconnect attempt after a dropped
	// stream; the collector resyncs its inventory here to cover the gap.
	// The initial sync is the caller's job, before Run.
	OnReconnect func(ctx context.Context)
	// OnError reports a dropped stream (optional).
	OnError func(err error, attempt int)
	// After is time.After in production; tests inject a channel.
	After func(d time.Duration) <-chan time.Time
	// Now is time.Now in production.
	Now func() time.Time
	// Backoff bounds (default 1 s .. 60 s, full jitter).
	Initial, Max time.Duration
	// StableAfter resets the backoff once a stream lived this long (60 s).
	StableAfter time.Duration
}

// Run streams events until ctx is cancelled.
func (l *EventLoop) Run(ctx context.Context) error {
	after := l.After
	if after == nil {
		after = time.After
	}
	now := l.Now
	if now == nil {
		now = time.Now
	}
	initial, maxD, stable := l.Initial, l.Max, l.StableAfter
	if initial <= 0 {
		initial = time.Second
	}
	if maxD <= 0 {
		maxD = 60 * time.Second
	}
	if stable <= 0 {
		stable = 60 * time.Second
	}
	attempt := 0
	var since time.Time
	for {
		started := now()
		err := l.Client.Events(ctx, since, func(raw []byte) error {
			if l.OnEvent != nil {
				l.OnEvent(raw)
			}
			return nil
		})
		if ctx.Err() != nil {
			return nil
		}
		// Resume from where the stream stopped; the resync on reconnect
		// covers anything the engine did not replay.
		since = now()
		if now().Sub(started) >= stable {
			attempt = 0
		}
		attempt++
		if l.OnError != nil && err != nil && !errors.Is(err, context.Canceled) {
			l.OnError(err, attempt)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-after(Backoff(attempt, initial, maxD)):
		}
		if l.OnReconnect != nil {
			l.OnReconnect(ctx)
		}
	}
}

// Backoff returns a full-jitter exponential delay for attempt (1-based).
func Backoff(attempt int, initial, maxD time.Duration) time.Duration {
	d := initial
	for i := 1; i < attempt && d < maxD; i++ {
		d *= 2
	}
	d = min(d, maxD)
	return time.Duration(rand.Int64N(int64(d)) + 1)
}
