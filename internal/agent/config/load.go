package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxFileSize caps the configuration file size.
const MaxFileSize = 1 << 20

// LookupEnv looks up an environment variable (os.LookupEnv in production).
type LookupEnv func(key string) (string, bool)

// Environment variable names honoured by Load.
const (
	EnvConfig         = "ASTERISM_AGENT_CONFIG"
	EnvHubURL         = "ASTERISM_AGENT_HUB_URL"
	EnvToken          = "ASTERISM_AGENT_TOKEN"
	EnvTokenFile      = "ASTERISM_AGENT_TOKEN_FILE"
	EnvHostName       = "ASTERISM_AGENT_HOST_NAME"
	EnvHostRoot       = "ASTERISM_AGENT_HOST_ROOT"
	EnvDockerEndpoint = "ASTERISM_AGENT_DOCKER_ENDPOINT"
	EnvLogLevel       = "ASTERISM_AGENT_LOG_LEVEL"
	EnvAllowActions   = "ASTERISM_AGENT_ALLOW_ACTIONS"
)

// LoadFile reads and parses path. When optional is true and the file does
// not exist, the defaults (plus environment overrides) are returned.
// Schema problems are returned as Issues.
func LoadFile(path string, optional bool, env LookupEnv) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if optional && errors.Is(err, fs.ErrNotExist) {
			cfg := Default()
			applyEnv(cfg, env)
			return cfg, nil
		}
		return nil, fmt.Errorf("open agent config: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read agent config %s: %w", path, err)
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("agent config %s is larger than %d bytes", path, MaxFileSize)
	}
	cfg, err := Parse(data, path, env)
	if cfg != nil {
		cfg.Path = path
	}
	return cfg, err
}

// Parse decodes YAML over the defaults, rejecting unknown keys and invalid
// scalars, expands ${VAR} in hub.* values and applies environment overrides.
// name labels issues (usually the file path).
func Parse(data []byte, name string, env LookupEnv) (*Config, error) {
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}
	cfg := Default()
	label := filepath.Base(name)
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			applyEnv(cfg, env)
			return cfg, nil
		}
		return nil, Issues{{File: label, Line: yamlErrLine(err), Message: "invalid YAML: " + yamlErrMsg(err)}}
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		doc = doc.Content[0]
	}
	if doc.Kind == yaml.ScalarNode && doc.Tag == "!!null" {
		applyEnv(cfg, env)
		return cfg, nil
	}
	w := walker{file: label}
	if hub := mappingValue(doc, "hub"); hub != nil {
		w.expand(hub, "hub", env)
	}
	w.check(doc, reflect.TypeOf(Config{}), "")
	if errs := w.issues.Errors(); len(errs) > 0 {
		return nil, errs.sorted()
	}
	if err := doc.Decode(cfg); err != nil {
		return nil, Issues{{File: label, Line: yamlErrLine(err), Message: yamlErrMsg(err)}}
	}
	cfg.warnings = append(cfg.warnings, w.issues.Warnings()...)
	cfg.lines = w.lines
	cfg.file = label
	applyEnv(cfg, env)
	return cfg, nil
}

// applyEnv applies ASTERISM_AGENT_* overrides. Environment beats YAML.
func applyEnv(cfg *Config, env LookupEnv) {
	if env == nil {
		return
	}
	set := func(key string, dst *string) {
		if v, ok := env(key); ok && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}
	set(EnvHubURL, &cfg.Hub.URL)
	set(EnvHostName, &cfg.Host.Name)
	set(EnvHostRoot, &cfg.Host.Root)
	set(EnvDockerEndpoint, &cfg.Sources.Docker.Endpoint)
	set(EnvLogLevel, &cfg.Agent.LogLevel)
	if v, ok := env(EnvTokenFile); ok && strings.TrimSpace(v) != "" {
		cfg.Hub.TokenFile = strings.TrimSpace(v)
		cfg.Hub.Token = ""
		cfg.TokenSource = "env:" + EnvTokenFile
	}
	if v, ok := env(EnvToken); ok && strings.TrimSpace(v) != "" {
		cfg.Hub.Token = strings.TrimSpace(v)
		cfg.TokenSource = "env:" + EnvToken
	}
	if v, ok := env(EnvAllowActions); ok && strings.TrimSpace(v) != "" {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		switch {
		case err != nil:
			cfg.warnings = append(cfg.warnings, Issue{Severity: SeverityWarning, Path: "env " + EnvAllowActions,
				Message: fmt.Sprintf("ignored invalid value %q (want false)", v)})
		case !b:
			cfg.Actions.Enabled = false
		default:
			cfg.warnings = append(cfg.warnings, Issue{Severity: SeverityWarning, Path: "env " + EnvAllowActions,
				Message: "true is ignored: the environment can only disable actions; enable them with actions.enabled in agent.yaml"})
		}
	}
}

// ---------------------------------------------------------------------------
// Schema walker: unknown keys and per-scalar type checks with line numbers.
// ---------------------------------------------------------------------------

type walker struct {
	file   string
	issues Issues
	lines  map[string]int
}

func (w *walker) mark(path string, n *yaml.Node) {
	if w.lines == nil {
		w.lines = map[string]int{}
	}
	w.lines[path] = n.Line
}

func (w *walker) errorf(n *yaml.Node, path, format string, args ...any) {
	line := 0
	if n != nil {
		line = n.Line
	}
	w.issues = append(w.issues, Issue{File: w.file, Line: line, Path: path, Message: fmt.Sprintf(format, args...)})
}

var (
	unmarshalerType = reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem()
)

func (w *walker) check(n *yaml.Node, t reflect.Type, path string) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return // null keeps the default
	}
	if reflect.PointerTo(t).Implements(unmarshalerType) {
		w.checkScalar(n, t, path)
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			w.errorf(n, path, "want a mapping of keys (%s), got %s", strings.Join(yamlKeys(t), ", "), kindName(n))
			return
		}
		fields := structFields(t)
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			kp := joinPath(path, k.Value)
			if seen[k.Value] {
				w.errorf(k, kp, "duplicate key")
				continue
			}
			seen[k.Value] = true
			w.mark(kp, k)
			f, ok := fields[k.Value]
			if !ok {
				w.errorf(k, kp, "%s", unknownKeyMessage(k.Value, yamlKeys(t), path))
				continue
			}
			w.check(v, f.Type, kp)
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			w.errorf(n, path, "want a list, got %s", kindName(n))
			return
		}
		for i, e := range n.Content {
			ep := fmt.Sprintf("%s[%d]", path, i)
			w.mark(ep, e)
			w.check(e, t.Elem(), ep)
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			w.errorf(n, path, "want a mapping, got %s", kindName(n))
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			kp := joinPath(path, n.Content[i].Value)
			w.mark(kp, n.Content[i])
			w.check(n.Content[i+1], t.Elem(), kp)
		}
	default:
		w.checkScalar(n, t, path)
	}
}

func (w *walker) checkScalar(n *yaml.Node, t reflect.Type, path string) {
	if n.Kind != yaml.ScalarNode {
		w.errorf(n, path, "want a single value, got %s", kindName(n))
		return
	}
	if t.Kind() == reflect.String && n.Tag != "!!str" && n.Tag != "!!int" && n.Tag != "!!float" && n.Tag != "!!bool" {
		w.errorf(n, path, "want a string, got %s", n.Tag)
		return
	}
	v := reflect.New(t)
	if err := n.Decode(v.Interface()); err != nil {
		w.errorf(n, path, "%s", scalarMessage(err, n, t))
	}
}

// expand replaces ${VAR} in every scalar under hub.*.
func (w *walker) expand(n *yaml.Node, path string, env LookupEnv) {
	switch n.Kind {
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "${") {
			return
		}
		var missing []string
		n.Value = envRefRe.ReplaceAllStringFunc(n.Value, func(ref string) string {
			key := ref[2 : len(ref)-1]
			v, ok := env(key)
			if !ok {
				missing = append(missing, key)
			}
			return v
		})
		for _, m := range missing {
			w.errorf(n, path, "environment variable %s referenced as ${%s} is not set", m, m)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			w.expand(n.Content[i+1], joinPath(path, n.Content[i].Value), env)
		}
	case yaml.SequenceNode:
		for i, e := range n.Content {
			w.expand(e, fmt.Sprintf("%s[%d]", path, i), env)
		}
	}
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func structFields(t reflect.Type) map[string]reflect.StructField {
	out := map[string]reflect.StructField{}
	for i := range t.NumField() {
		f := t.Field(i)
		name := yamlName(f)
		if name == "" {
			continue
		}
		out[name] = f
	}
	return out
}

func yamlName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	tag := f.Tag.Get("yaml")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" || tag == "" {
		return ""
	}
	return name
}

func yamlKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		if n := yamlName(t.Field(i)); n != "" {
			keys = append(keys, n)
		}
	}
	return keys
}

func joinPath(base, key string) string {
	switch {
	case base == "":
		return key
	case key == "":
		return base
	default:
		return base + "." + key
	}
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		return fmt.Sprintf("%q", n.Value)
	default:
		return "an unsupported node"
	}
}

func unknownKeyMessage(key string, valid []string, path string) string {
	where := "at the top level"
	if path != "" {
		where = "in " + path
	}
	msg := "unknown key"
	if s := suggest(key, valid); s != "" {
		msg += fmt.Sprintf(" (did you mean %q?)", s)
	}
	sorted := append([]string(nil), valid...)
	sort.Strings(sorted)
	return msg + "; valid keys " + where + ": " + strings.Join(sorted, ", ")
}

// suggest returns the closest valid key within an edit distance of 2.
func suggest(key string, valid []string) string {
	norm := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), "-", "_") }
	best, bestD := "", 3
	for _, v := range valid {
		if norm(key) == v || strings.ReplaceAll(norm(key), "_", "") == strings.ReplaceAll(v, "_", "") {
			return v
		}
		if d := editDistance(norm(key), v); d < bestD {
			best, bestD = v, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// envRefRe matches ${VAR}; bare $VAR is left alone.
var envRefRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

var yamlLineRe = regexp.MustCompile(`line (\d+): `)

func yamlErrLine(err error) int {
	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func yamlErrMsg(err error) string {
	s := strings.TrimPrefix(err.Error(), "yaml: ")
	s = strings.TrimPrefix(s, "unmarshal errors:\n  ")
	return yamlLineRe.ReplaceAllString(s, "")
}

func scalarMessage(err error, n *yaml.Node, t reflect.Type) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		switch t.Kind() {
		case reflect.Bool:
			return fmt.Sprintf("invalid value %q (want true or false)", n.Value)
		case reflect.Int, reflect.Int32, reflect.Int64:
			return fmt.Sprintf("invalid value %q (want a whole number)", n.Value)
		case reflect.Float64, reflect.Float32:
			return fmt.Sprintf("invalid value %q (want a number)", n.Value)
		}
		return fmt.Sprintf("invalid value %q", n.Value)
	}
	return yamlErrMsg(err)
}
