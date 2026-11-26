package dockerapi_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/actions"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi/dockertest"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const fixtures = "../../collect/dockercli/testdata/engine"

const idA = "317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25"

func newClient(t *testing.T) (*dockerapi.Client, *dockertest.Engine) {
	t.Helper()
	eng := dockertest.New(t, fixtures, dockerapi.Allowed)
	c, err := dockerapi.New(eng.Endpoint, dockerapi.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, eng
}

func TestAllowed(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/_ping", true},
		{"GET", "/v1.51/containers/json", true},
		{"GET", "/v1.51/containers/" + idA + "/json", true},
		{"GET", "/v1.51/containers/shop-api-1/logs", true},
		{"GET", "/v1.51/events", true},
		{"GET", "/v1.51/images/json", true},
		{"GET", "/v1.51/volumes", true},
		{"GET", "/v1.51/networks", true},
		{"GET", "/v1.51/info", true},
		{"GET", "/v1.51/system/df", true},
		{"POST", "/v1.51/containers/abc123/restart", true},
		{"POST", "/v1.51/containers/abc123/unpause", true},
		{"POST", "/v1.51/containers/abc123/exec", false},
		{"POST", "/v1.51/containers/create", false},
		{"POST", "/v1.51/containers/abc123/kill", false},
		{"DELETE", "/v1.51/containers/abc123", false},
		{"POST", "/v1.51/exec/abc/start", false},
		{"POST", "/v1.51/build", false},
		{"GET", "/v1.51/containers/abc123/archive", false},
		{"GET", "/v1.51/containers/abc123/export", false},
		{"GET", "/v1.51/containers/../images/json", false},
		{"GET", "/v1.51/networks/abc", false},
		{"GET", "/v1.51/volumes/abc", false},
		{"POST", "/v1.51/images/create", false},
		{"GET", "/v1.51/secrets", false},
		{"PUT", "/v1.51/containers/abc/archive", false},
		{"GET", "/containers/json", true},
	}
	for _, c := range cases {
		if got := dockerapi.Allowed(c.method, c.path); got != c.want {
			t.Errorf("%s %s = %v want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestClientEndpoints(t *testing.T) {
	c, eng := newClient(t)
	ctx := context.Background()
	if c.APIVersion() != dockertest.APIVersion {
		t.Fatalf("negotiated %q", c.APIVersion())
	}
	for name, fn := range map[string]func(context.Context) ([]byte, error){
		"info": c.Info, "list": c.ListContainers, "images": c.Images, "volumes": c.Volumes, "networks": c.Networks, "df": c.SystemDF,
	} {
		b, err := fn(ctx)
		if err != nil || len(b) == 0 {
			t.Fatalf("%s: %v (%d bytes)", name, err, len(b))
		}
	}
	raw, err := c.InspectContainer(ctx, "shop-api-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dockercli.ContainerFromEngine(raw, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InspectContainer(ctx, "missing"); !dockerapi.IsStatus(err, http.StatusNotFound) {
		t.Fatalf("missing container: %v", err)
	}
	if _, err := c.InspectContainer(ctx, "../../exec"); err == nil {
		t.Fatal("path traversal ref accepted")
	}
	if n := eng.Count("GET", `^/v1\.51/containers/json$`); n != 1 {
		t.Fatalf("list hits %d", n)
	}
	if !strings.Contains(eng.Hits()[1].Path, "/v1.51/") {
		t.Fatalf("version prefix not used: %+v", eng.Hits())
	}
	if v := eng.Violations(); len(v) != 0 {
		t.Fatalf("non allow-listed endpoints hit: %+v", v)
	}
}

func TestResponseCap(t *testing.T) {
	c, eng := newClient(t)
	eng.SetBody("/images/json", bytes.Repeat([]byte("x"), dockerapi.MaxResponseBytes+10))
	if _, err := c.Images(context.Background()); !errors.Is(err, dockerapi.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestContainerActionExecutor(t *testing.T) {
	c, eng := newClient(t)
	ctx := context.Background()
	var post int
	c.PostProcess = func(*v1.Container) { post++ }
	got, err := c.ContainerAction(ctx, idA[:12], v1.ContainerAction_CONTAINER_ACTION_RESTART, 3*time.Second)
	if err != nil || got == nil || got.Name != "shop-api-1" || post != 1 {
		t.Fatalf("restart: %v %v post=%d", got, err, post)
	}
	if h := eng.Hits(); !strings.Contains(h[len(h)-2].Query, "t=3") {
		t.Fatalf("timeout not passed: %+v", h[len(h)-2])
	}
	eng.SetStatus("POST /containers/"+idA[:12]+"/pause", http.StatusConflict)
	if _, err := c.ContainerAction(ctx, idA[:12], v1.ContainerAction_CONTAINER_ACTION_PAUSE, 0); !errors.Is(err, actions.ErrConflict) {
		t.Fatalf("409: %v", err)
	}
	eng.SetStatus("POST /containers/"+idA[:12]+"/start", http.StatusNotModified)
	if _, err := c.ContainerAction(ctx, idA[:12], v1.ContainerAction_CONTAINER_ACTION_START, 0); !errors.Is(err, actions.ErrConflict) {
		t.Fatalf("304: %v", err)
	}
	if _, err := c.ContainerAction(ctx, "deadbeefdead", v1.ContainerAction_CONTAINER_ACTION_STOP, 0); !errors.Is(err, actions.ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	for _, bad := range []string{"shop-api-1", "../x", "", strings.Repeat("a", 65)} {
		if _, err := c.ContainerAction(ctx, bad, v1.ContainerAction_CONTAINER_ACTION_STOP, 0); !errors.Is(err, actions.ErrNotFound) {
			t.Fatalf("invalid id %q: %v", bad, err)
		}
	}
	if _, err := c.ContainerAction(ctx, idA, v1.ContainerAction_CONTAINER_ACTION_UNSPECIFIED, 0); err == nil {
		t.Fatal("unspecified action accepted")
	}
	if v := eng.Violations(); len(v) != 0 {
		t.Fatalf("violations: %+v", v)
	}
}

func TestEventsStreamAndReconnect(t *testing.T) {
	c, eng := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 16)
	reconnects := make(chan struct{}, 4)
	backoff := make(chan time.Time)
	loop := &dockerapi.EventLoop{
		Client:      c,
		OnEvent:     func(raw []byte) { got <- string(raw) },
		OnReconnect: func(context.Context) { reconnects <- struct{}{} },
		After:       func(time.Duration) <-chan time.Time { return backoff },
	}
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	<-eng.EventsConnected()
	eng.PushEvent([]byte(`{"Type":"container","Action":"start"}`))
	if s := <-got; !strings.Contains(s, "start") {
		t.Fatalf("event %q", s)
	}
	eng.PushEvent(nil) // drop the stream
	backoff <- time.Now()
	<-reconnects
	<-eng.EventsConnected()
	eng.PushEvent([]byte(`{"Type":"container","Action":"die"}`))
	if s := <-got; !strings.Contains(s, "die") {
		t.Fatalf("event after reconnect %q", s)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	q := eng.Hits()
	for _, h := range q {
		if strings.HasSuffix(h.Path, "/events") && !strings.Contains(h.Query, "filters=") {
			t.Fatalf("events without filters: %+v", h)
		}
	}
	if v := eng.Violations(); len(v) != 0 {
		t.Fatalf("violations: %+v", v)
	}
}

func TestBackoffBounds(t *testing.T) {
	for attempt := 1; attempt < 20; attempt++ {
		d := dockerapi.Backoff(attempt, time.Second, 60*time.Second)
		if d <= 0 || d > 60*time.Second {
			t.Fatalf("attempt %d: %v", attempt, d)
		}
	}
}

func TestLogsDemux(t *testing.T) {
	c, _ := newClient(t)
	body, err := c.Logs(context.Background(), "shop-api-1", dockerapi.LogOptions{Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	var lines []string
	err = dockerapi.Demux(body, func(s byte, line []byte) error {
		ts, text := dockerapi.SplitTimestamp(line)
		if ts.IsZero() {
			t.Errorf("no timestamp in %q", line)
		}
		lines = append(lines, string([]byte{'0' + s})+":"+dockercli.RedactText(string(text)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1:listening on :8080", "2:warn: password=[REDACTED] rejected", "1:partial line without newline"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", lines)
	}
	raw, _ := os.ReadFile(fixtures + "/logs-multiplexed.bin")
	if err := dockerapi.Demux(bytes.NewReader(append([]byte{7, 0, 0, 0, 0, 0, 0, 1}, raw...)), func(byte, []byte) error { return nil }); err == nil {
		t.Fatal("bad stream id accepted")
	}
	var tty []string
	_ = dockerapi.SplitRaw(strings.NewReader("a\r\nb\n"), func(l []byte) error { tty = append(tty, string(l)); return nil })
	if strings.Join(tty, ",") != "a,b" {
		t.Fatalf("tty %q", tty)
	}
}

func TestNewEndpoints(t *testing.T) {
	for _, ep := range []string{"unix:///var/run/docker.sock", "tcp://socket-proxy:2375", "http://127.0.0.1:2375"} {
		if _, err := dockerapi.New(ep, dockerapi.Options{}); err != nil {
			t.Errorf("%s: %v", ep, err)
		}
	}
	for _, ep := range []string{"", "unix://", "ssh://host", "tcp://"} {
		if _, err := dockerapi.New(ep, dockerapi.Options{}); err == nil {
			t.Errorf("%s accepted", ep)
		}
	}
}
