package agentingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestProjectIconRefsAndFetch(t *testing.T) {
	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "s1")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	sum := sha256.Sum256(svg)
	digest := hex.EncodeToString(sum[:])

	snap := snapshotFrame(1, shopContainer("shop-api-1", "3f2a9c1b7d4e", asterismv1.ContainerState_CONTAINER_STATE_RUNNING))
	snap.GetSnapshot().ProjectIcons = []*asterismv1.ProjectIconRef{
		{WorkingDir: "/srv/shop", RelPath: "public/favicon.svg", Sha256: digest, SizeBytes: uint32(len(svg))},
		{WorkingDir: "/srv/bad", RelPath: "x.svg", Sha256: "short"}, // malformed: ignored
	}
	s.send(snap)
	s.sync()
	v := h.reg.Primary()
	ref, ok := v.DirIcon("/srv/shop")
	if !ok || ref.RelPath != "public/favicon.svg" || ref.SHA256 != digest {
		t.Fatalf("DirIcon = %+v %v", ref, ok)
	}
	if _, ok := v.DirIcon("/srv/bad"); ok {
		t.Fatal("malformed ref accepted")
	}

	type result struct {
		data []byte
		err  error
	}
	fetch := func() chan result {
		ch := make(chan result, 1)
		go func() {
			d, err := v.ReadDirIcon(context.Background(), ref)
			ch <- result{d, err}
		}()
		return ch
	}

	// Happy path.
	ch := fetch()
	req := s.next(func(f *asterismv1.HubFrame) bool { return f.GetIconRequest() != nil }).GetIconRequest()
	if req.GetSha256() != digest || req.GetRequestId() == "" {
		t.Fatalf("request %v", req)
	}
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_IconData{IconData: &asterismv1.IconData{RequestId: req.GetRequestId(), Sha256: digest, Data: svg}}})
	if res := <-ch; res.err != nil || string(res.data) != string(svg) {
		t.Fatalf("fetch = %q %v", res.data, res.err)
	}

	// Bytes that do not match the digest are refused.
	ch = fetch()
	req = s.next(func(f *asterismv1.HubFrame) bool { return f.GetIconRequest() != nil }).GetIconRequest()
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_IconData{IconData: &asterismv1.IconData{RequestId: req.GetRequestId(), Sha256: digest, Data: []byte("<svg>evil</svg>")}}})
	if res := <-ch; res.err == nil || !strings.Contains(res.err.Error(), "digest") {
		t.Fatalf("mismatch err = %v", res.err)
	}

	// Agent-side errors surface.
	ch = fetch()
	req = s.next(func(f *asterismv1.HubFrame) bool { return f.GetIconRequest() != nil }).GetIconRequest()
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_IconData{IconData: &asterismv1.IconData{RequestId: req.GetRequestId(), Error: "unknown icon digest"}}})
	if res := <-ch; res.err == nil || !strings.Contains(res.err.Error(), "unknown icon digest") {
		t.Fatalf("agent err = %v", res.err)
	}

	// Offline.
	_ = s.hangup()
	if _, err := v.ReadDirIcon(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline err = %v", err)
	}
}
