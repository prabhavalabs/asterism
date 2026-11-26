// Package dockertest is a fake Docker Engine API on a unix socket that
// replays recorded Engine JSON fixtures. Tests use it to drive the agent's
// Docker client and collector without a Docker daemon, and to assert that
// only allow-listed endpoints are ever requested.
package dockertest

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// APIVersion is the version the fake engine advertises.
const APIVersion = "1.51"

// Hit is one received request.
type Hit struct {
	Method string
	Path   string
	Query  string
}

// Engine is a running fake engine.
type Engine struct {
	// Endpoint is "unix://<socket>".
	Endpoint string
	dir      string

	mu        sync.Mutex
	hits      []Hit
	files     map[string]string // container ref (name, short or full id) -> inspect fixture
	status    map[string]int    // "POST /containers/<id>/<verb>" -> status override
	override  map[string][]byte // path -> body override
	events    chan []byte
	eventsHit chan struct{}
	allow     func(method, path string) bool
}

// New starts a fake engine serving fixtures from dir (the dockercli
// testdata/engine directory). allow is dockerapi.Allowed, used to report
// violations; inspect maps container names to their inspect fixture files.
func New(t testing.TB, dir string, allow func(method, path string) bool) *Engine {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "dkt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		Endpoint:  "unix://" + sock,
		dir:       dir,
		files:     map[string]string{},
		status:    map[string]int{},
		override:  map[string][]byte{},
		events:    make(chan []byte, 64),
		eventsHit: make(chan struct{}, 16),
		allow:     allow,
	}
	e.indexInspect(t)
	srv := &http.Server{Handler: http.HandlerFunc(e.serve)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		close(e.events)
		_ = srv.Close()
	})
	return e
}

// indexInspect maps every inspect-*.json fixture by name and ids.
func (e *Engine) indexInspect(t testing.TB) {
	matches, _ := filepath.Glob(filepath.Join(e.dir, "inspect-*.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		e.files[strings.TrimPrefix(d.Name, "/")] = m
		e.files[d.ID] = m
		if len(d.ID) >= 12 {
			e.files[d.ID[:12]] = m
		}
	}
}

// Hits returns every request received so far.
func (e *Engine) Hits() []Hit {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Hit(nil), e.hits...)
}

// Violations returns requests outside the allow-list.
func (e *Engine) Violations() []Hit {
	var out []Hit
	for _, h := range e.Hits() {
		if e.allow != nil && !e.allow(h.Method, h.Path) {
			out = append(out, h)
		}
	}
	return out
}

// Count returns how many requests matched method and a path regexp.
func (e *Engine) Count(method, pathRe string) int {
	re := regexp.MustCompile(pathRe)
	n := 0
	for _, h := range e.Hits() {
		if h.Method == method && re.MatchString(h.Path) {
			n++
		}
	}
	return n
}

// SetStatus makes "METHOD /path" (version prefix stripped) answer code.
func (e *Engine) SetStatus(methodPath string, code int) {
	e.mu.Lock()
	e.status[methodPath] = code
	e.mu.Unlock()
}

// SetBody overrides the body served for a path (version prefix stripped).
func (e *Engine) SetBody(path string, body []byte) {
	e.mu.Lock()
	e.override[path] = body
	e.mu.Unlock()
}

// SetInspect maps a container ref to an inspect fixture file name.
func (e *Engine) SetInspect(ref, file string) {
	e.mu.Lock()
	e.files[ref] = filepath.Join(e.dir, file)
	e.mu.Unlock()
}

// RemoveContainer makes inspect of ref return 404.
func (e *Engine) RemoveContainer(ref string) {
	e.mu.Lock()
	delete(e.files, ref)
	e.mu.Unlock()
}

// PushEvent sends one raw event line to the open /events stream.
func (e *Engine) PushEvent(line []byte) { e.events <- line }

// EventsConnected returns a channel that receives once per /events request.
func (e *Engine) EventsConnected() <-chan struct{} { return e.eventsHit }

var verPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

func (e *Engine) serve(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.hits = append(e.hits, Hit{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery})
	p := verPrefix.ReplaceAllString(r.URL.Path, "")
	code, hasCode := e.status[r.Method+" "+p]
	body, hasBody := e.override[p]
	e.mu.Unlock()

	w.Header().Set("API-Version", APIVersion)
	if hasCode {
		w.WriteHeader(code)
		if code >= 400 {
			_, _ = w.Write([]byte(`{"message":"fake engine status"}`))
		}
		return
	}
	if hasBody {
		_, _ = w.Write(body)
		return
	}
	switch {
	case p == "/_ping":
		_, _ = w.Write([]byte("OK"))
	case p == "/info":
		e.file(w, "info.json")
	case p == "/containers/json":
		e.file(w, "containers.json")
	case p == "/images/json":
		e.file(w, "images.json")
	case p == "/volumes":
		e.file(w, "volumes.json")
	case p == "/networks":
		e.file(w, "networks.json")
	case p == "/system/df":
		e.file(w, "system-df.json")
	case p == "/events":
		e.serveEvents(w, r)
	case strings.HasPrefix(p, "/containers/"):
		parts := strings.Split(strings.TrimPrefix(p, "/containers/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		ref, op := parts[0], parts[1]
		e.mu.Lock()
		f, ok := e.files[ref]
		e.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container: ` + ref + `"}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && op == "json":
			http.ServeFile(w, r, f)
		case r.Method == http.MethodGet && op == "logs":
			e.file(w, "logs-multiplexed.bin")
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (e *Engine) file(w http.ResponseWriter, name string) {
	b, err := os.ReadFile(filepath.Join(e.dir, name))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func (e *Engine) serveEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	select {
	case e.eventsHit <- struct{}{}:
	default:
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-e.events:
			if !ok {
				return
			}
			if line == nil { // nil closes this stream (simulated drop)
				return
			}
			_, _ = w.Write(append(line, '\n'))
			if fl != nil {
				fl.Flush()
			}
		}
	}
}
