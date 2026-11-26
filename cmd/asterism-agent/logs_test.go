package main

import (
	"context"
	"testing"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

type fakeLogs struct {
	known string
	calls int
}

func (f *fakeLogs) Logs(_ context.Context, req *v1.LogRequest, send func(*v1.LogChunk) bool) {
	f.calls++
	if req.GetWorkloadId() != f.known {
		send(&v1.LogChunk{RequestId: req.GetRequestId(), Eof: true, Error: notFoundMsg})
		return
	}
	send(&v1.LogChunk{RequestId: req.GetRequestId(), Lines: []*v1.LogLine{{Text: "hello from " + f.known}}})
	send(&v1.LogChunk{RequestId: req.GetRequestId(), Eof: true})
}

func TestLogRouter(t *testing.T) {
	dockerSrc, podmanSrc := &fakeLogs{known: "ctr:web-1"}, &fakeLogs{known: "ctr:pod-1"}
	r := logRouter{dockerSrc, podmanSrc}
	collect := func(id string) []*v1.LogChunk {
		var out []*v1.LogChunk
		r.Logs(context.Background(), &v1.LogRequest{RequestId: "r1", WorkloadId: id}, func(ch *v1.LogChunk) bool {
			out = append(out, ch)
			return true
		})
		return out
	}
	if got := collect("ctr:web-1"); len(got) != 2 || got[0].GetLines()[0].GetText() != "hello from ctr:web-1" || podmanSrc.calls != 0 {
		t.Fatalf("docker container: %v (podman calls %d)", got, podmanSrc.calls)
	}
	if got := collect("ctr:pod-1"); len(got) != 2 || got[0].GetLines()[0].GetText() != "hello from ctr:pod-1" {
		t.Fatalf("podman container: %v", got)
	}
	if got := collect("ctr:nope"); len(got) != 1 || got[0].GetError() != notFoundMsg || !got[0].GetEof() {
		t.Fatalf("unknown container: %v", got)
	}
}

func TestBuildLogsWiresDockerSource(t *testing.T) {
	cfg := config.Default()
	cfg.Sources.Docker.Endpoint = "unix:///nonexistent/docker.sock"
	_ = buildSources(cfg, state.Env{Config: cfg})
	if buildLogs(cfg) == nil {
		t.Fatal("the docker source must serve on-demand logs")
	}
	off := config.Default()
	off.Sources.Docker.Enabled = config.TriFalse
	_ = buildSources(off, state.Env{Config: off})
	if buildLogs(off) != nil {
		t.Fatal("no engine source: logs handler must be nil")
	}
}
