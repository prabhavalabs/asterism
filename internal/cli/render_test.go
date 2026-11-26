package cli

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixtureNow is shortly after the newest event in the fixtures.
var fixtureNow = time.UnixMilli(1795705625291 + 90_000).UTC()

func load[T any, P interface {
	*T
	proto.Message
}](t *testing.T, name string) P {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	m := P(new(T))
	if err := protojson.Unmarshal(raw, m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch (run go test ./internal/cli -update)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestRenderGolden(t *testing.T) {
	plain := theme{}
	graph := load[asterismv1.GetGraphResponse](t, "graph.json").GetGraph()
	cases := []struct {
		name   string
		render func(*bytes.Buffer)
	}{
		{"status", func(b *bytes.Buffer) {
			renderStatus(b, plain, load[asterismv1.GetOverviewResponse](t, "overview.json"), fixtureNow)
		}},
		{"ps", func(b *bytes.Buffer) { renderPs(b, plain, load[asterismv1.ListWorkloadsResponse](t, "workloads.json")) }},
		{"ports", func(b *bytes.Buffer) { renderPorts(b, plain, load[asterismv1.ListPortsResponse](t, "ports.json")) }},
		{"ingress", func(b *bytes.Buffer) {
			renderIngress(b, plain, load[asterismv1.ListIngressResponse](t, "ingress.json"))
		}},
		{"events", func(b *bytes.Buffer) {
			renderEvents(b, plain, load[asterismv1.ListEventsResponse](t, "events.json"), time.UTC)
		}},
		{"logs", func(b *bytes.Buffer) {
			logPrinter{w: b, loc: time.UTC}.print(load[asterismv1.TailLogsResponse](t, "logs.json").GetLines())
		}},
		{"topo", func(b *bytes.Buffer) { renderTopo(b, plain, graph) }},
		{"topo_around_project", func(b *bytes.Buffer) {
			g, err := filterGraph(graph, "proj:nova-core", 1)
			if err != nil {
				t.Fatal(err)
			}
			renderTopo(b, plain, g)
		}},
		{"topo_around_workload", func(b *bytes.Buffer) {
			g, err := filterGraph(graph, "ctr:ingest-collector", 1)
			if err != nil {
				t.Fatal(err)
			}
			renderTopo(b, plain, g)
		}},
		{"top", func(b *bytes.Buffer) { b.WriteString(renderTop(plain, topFixture(t), "cpu", 0, 0, time.UTC, nil)) }},
		{"top_mem_small", func(b *bytes.Buffer) {
			b.WriteString(renderTop(plain, topFixture(t), "mem", 72, 10, time.UTC, errors.New("daemon not reachable")))
		}},
		{"empty", func(b *bytes.Buffer) {
			renderPs(b, plain, &asterismv1.ListWorkloadsResponse{})
			renderPorts(b, plain, &asterismv1.ListPortsResponse{})
			renderIngress(b, plain, &asterismv1.ListIngressResponse{})
			renderEvents(b, plain, &asterismv1.ListEventsResponse{}, time.UTC)
			renderTopo(b, plain, &asterismv1.Graph{})
			renderStatus(b, plain, &asterismv1.GetOverviewResponse{}, fixtureNow)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			tc.render(&b)
			if strings.Contains(b.String(), "\x1b") {
				t.Fatal("plain output contains escape sequences")
			}
			golden(t, tc.name, b.String())
		})
	}
}

func topFixture(t *testing.T) *topSnapshot {
	ov := load[asterismv1.GetOverviewResponse](t, "overview.json")
	wl := load[asterismv1.ListWorkloadsResponse](t, "workloads.json")
	return &topSnapshot{Host: ov.GetHost(), Workloads: wl.GetWorkloads(), Total: wl.GetPageInfo().GetTotalSize(), At: fixtureNow}
}

func TestColourThemeStyles(t *testing.T) {
	var b bytes.Buffer
	renderStatus(&b, theme{color: true}, load[asterismv1.GetOverviewResponse](t, "overview.json"), fixtureNow)
	out := b.String()
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("colour output has no escape sequences")
	}
	if !strings.Contains(out, "█") {
		t.Error("colour output should draw usage bars")
	}
}

func TestTopWidthTruncates(t *testing.T) {
	out := renderTop(theme{color: true}, topFixture(t), "cpu", 40, 8, time.UTC, nil)
	lines := strings.Split(out, "\n")
	if len(lines) > 8 {
		t.Errorf("got %d lines, want at most 8", len(lines))
	}
	for _, l := range lines {
		if w := len([]rune(stripANSI(l))); w > 40 {
			t.Errorf("line wider than 40 columns (%d): %q", w, l)
		}
	}
}

func stripANSI(s string) string { return clean(s) }

func TestTopModelKeys(t *testing.T) {
	m := newTopModel(t.Context(), nil, "cpu", theme{}, time.UTC, "http://x")
	step := func(msg tea.Msg) tea.Cmd {
		next, cmd := m.Update(msg)
		m = next.(topModel)
		return cmd
	}
	step(tea.WindowSizeMsg{Width: 80, Height: 20})
	if m.width != 80 || m.height != 20 {
		t.Fatalf("resize not applied: %dx%d", m.width, m.height)
	}
	for _, tc := range []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: 'm', Text: "m"}, "mem"},
		{tea.KeyPressMsg{Code: 'c', Text: "c"}, "cpu"},
		{tea.KeyPressMsg{Code: tea.KeyTab}, "mem"},
		{tea.KeyPressMsg{Code: tea.KeyTab}, "cpu"},
	} {
		step(tc.key)
		if m.by != tc.want {
			t.Errorf("after %q: by = %s, want %s", tc.key.String(), m.by, tc.want)
		}
	}
	if cmd := step(topDataMsg{err: errors.New("boom")}); cmd == nil {
		t.Error("a fetch result must schedule the next tick")
	}
	if m.err == nil {
		t.Error("fetch error should be kept for the footer")
	}
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl}} {
		cmd := step(k)
		if cmd == nil {
			t.Fatalf("%q should quit", k.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%q: want tea.QuitMsg", k.String())
		}
	}
}

func TestCursorDeduplicates(t *testing.T) {
	line := func(ts int64, text string) *asterismv1.LogLine { return &asterismv1.LogLine{TsMs: ts, Text: text} }
	c := newCursor([]*asterismv1.LogLine{line(1, "a"), line(2, "b"), line(2, "c")})
	got := c.advance([]*asterismv1.LogLine{line(1, "a"), line(2, "b"), line(2, "c"), line(2, "d"), line(3, "e")})
	var texts []string
	for _, l := range got {
		texts = append(texts, l.GetText())
	}
	if strings.Join(texts, ",") != "d,e" {
		t.Errorf("fresh = %v, want d,e", texts)
	}
	if again := c.advance([]*asterismv1.LogLine{line(3, "e")}); len(again) != 0 {
		t.Errorf("repeated poll printed %d lines again", len(again))
	}
	if dup := c.advance([]*asterismv1.LogLine{line(3, "e"), line(3, "e")}); len(dup) != 1 {
		t.Errorf("a second identical line at the same ts must print once, got %d", len(dup))
	}
}

func TestCleanStripsControlSequences(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                   "plain",
		"\x1b[31mred\x1b[0m":      "red",
		"bell\x07 and\ttab":       "bell and tab",
		"osc \x1b]0;title\x07end": "osc end",
		"c1 \u009bx":              "c1 x",
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFlagParsing(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"30m":                  now.Add(-30 * time.Minute),
		"1h":                   now.Add(-time.Hour),
		"2d":                   now.Add(-48 * time.Hour),
		"1w":                   now.Add(-7 * 24 * time.Hour),
		"2026-11-25T10:00:00Z": time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
	} {
		got, err := sinceTime(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("sinceTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "-1h", "xd"} {
		if _, err := sinceTime(bad, now); err == nil {
			t.Errorf("sinceTime(%q): want an error", bad)
		}
	}
	for in, want := range map[string]string{"": "", "warning": "warning,critical", "critical": "critical", "error": "critical", "info": ""} {
		got, err := severitiesFrom(in)
		if err != nil || strings.Join(got, ",") != want {
			t.Errorf("severitiesFrom(%q) = %v, %v; want %q", in, got, err, want)
		}
	}
	if _, err := severitiesFrom("loud"); err == nil {
		t.Error("severitiesFrom: want an error for an unknown level")
	}
	if v, err := enumFlag("exposure", "public", "PORT_EXPOSURE_", asterismv1.PortExposure_value); err != nil || v != int32(asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC) {
		t.Errorf("enumFlag public = %d, %v", v, err)
	}
	if _, err := enumFlag("exposure", "everywhere", "PORT_EXPOSURE_", asterismv1.PortExposure_value); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("enumFlag should list valid values: %v", err)
	}
	if projectID("shop") != "proj:shop" || projectID("proj:shop") != "proj:shop" || projectID("") != "" {
		t.Error("projectID should add the proj: prefix only when missing")
	}
}

func TestFilterGraphUnknown(t *testing.T) {
	graph := load[asterismv1.GetGraphResponse](t, "graph.json").GetGraph()
	if _, err := filterGraph(graph, "ctr:missing", 1); err == nil || !strings.Contains(err.Error(), "proj:<name>") {
		t.Errorf("want an actionable error, got %v", err)
	}
	g, err := filterGraph(graph, "nova-core", 1)
	if err != nil || len(g.GetNodes()) == 0 {
		t.Fatalf("bare project name should match: %v", err)
	}
	for _, e := range g.GetEdges() {
		found := 0
		for _, n := range g.GetNodes() {
			if n.GetId() == e.GetSourceId() || n.GetId() == e.GetTargetId() {
				found++
			}
		}
		if found != 2 {
			t.Errorf("edge %s keeps a node outside the filter", e.GetId())
		}
	}
}

func TestURLFromListen(t *testing.T) {
	for listen, want := range map[string]string{
		"127.0.0.1:7700": "http://127.0.0.1:7700",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		":7700":          "http://127.0.0.1:7700",
		"[::]:7700":      "http://127.0.0.1:7700",
		"[::1]:7700":     "http://[::1]:7700",
	} {
		got, err := URLFromListen(listen)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", listen, got, err, want)
		}
	}
	if _, err := URLFromListen("no-port"); err == nil {
		t.Error("want an error for a listen address without a port")
	}
}
