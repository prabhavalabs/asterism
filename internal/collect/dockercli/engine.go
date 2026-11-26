package dockercli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// This file converts Docker Engine API JSON (GET /containers/{id}/json,
// /containers/json, /events) into Asterism messages. It is shared by the
// agent and any future local Engine-API mode, and uses only the stdlib.
//
// Secrets: Config.Env is decoded by envKeys, which keeps only the part
// before '=' while unmarshalling, so environment values are never stored in
// any Go value, let alone encoded.

// ErrNotContainer is returned for JSON that is not a container document.
var ErrNotContainer = errors.New("dockercli: not a container document")

// envKeys unmarshals a JSON array of "KEY=value" strings into the sorted,
// de-duplicated keys only.
type envKeys []string

// UnmarshalJSON implements json.Unmarshaler. It scans the array in place
// and copies only the bytes before '=' of each element: values are skipped
// without being decoded or copied anywhere.
func (e *envKeys) UnmarshalJSON(b []byte) error {
	*e = nil
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		return nil
	}
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return fmt.Errorf("Env: want an array of strings")
	}
	var out []string
	i, end := 1, len(b)-1
	for i < end {
		for i < end && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r' || b[i] == ',') {
			i++
		}
		if i >= end {
			break
		}
		if b[i] != '"' {
			return fmt.Errorf("Env: want an array of strings")
		}
		i++
		start, keyEnd, valid := i, -1, true
		for i < end && b[i] != '"' {
			switch b[i] {
			case '\\':
				if keyEnd < 0 {
					valid = false // escapes are not legal in env names
				}
				i++ // skip the escaped byte
			case '=':
				if keyEnd < 0 {
					keyEnd = i
				}
			}
			i++
		}
		if i >= end {
			return fmt.Errorf("Env: unterminated string")
		}
		if keyEnd < 0 {
			keyEnd = i
		}
		i++ // closing quote
		if valid {
			if k := strings.TrimSpace(string(b[start:keyEnd])); k != "" {
				out = append(out, k)
			}
		}
	}
	*e = envKeys(cleanEnvKeys(out))
	return nil
}

// engineContainer is the subset of GET /containers/{id}/json the agent uses.
// It embeds the CLI inspect document so both paths share buildContainer.
type engineContainer struct {
	inspectDoc
	Config engineConfig `json:"Config"`
}

type engineConfig struct {
	Image        string              `json:"Image"`
	Labels       map[string]string   `json:"Labels"`
	Cmd          strSlice            `json:"Cmd"`
	Entrypoint   strSlice            `json:"Entrypoint"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	User         string              `json:"User"`
	Tty          bool                `json:"Tty"`
	Healthcheck  *struct {
		Test     []string `json:"Test"`
		Interval int64    `json:"Interval"`
		Timeout  int64    `json:"Timeout"`
		Retries  int      `json:"Retries"`
	} `json:"Healthcheck"`
	Env envKeys `json:"Env"`
}

// strSlice accepts a JSON string or array of strings (older engines encode
// a single-string Cmd/Entrypoint as a string).
type strSlice []string

// UnmarshalJSON implements json.Unmarshaler.
func (s *strSlice) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// EngineContainer is a converted container plus Engine facts the agent
// needs but the proto does not carry.
type EngineContainer struct {
	Container *asterismv1.Container
	// FullID is the 64-hex container id (Container.ContainerId is short).
	FullID string
	// Tty reports a TTY container, whose logs are not multiplexed.
	Tty bool
}

// ContainerFromEngine converts one GET /containers/{id}/json document.
// Environment variables are reduced to their names during decoding; labels,
// command, entrypoint and healthcheck output are redacted as in ParseInspect.
func ContainerFromEngine(raw []byte, now time.Time) (EngineContainer, error) {
	var d engineContainer
	if err := json.Unmarshal(raw, &d); err != nil {
		return EngineContainer{}, fmt.Errorf("decode container: %w", err)
	}
	if !ValidContainerID(d.ID) {
		return EngineContainer{}, ErrNotContainer
	}
	d.inspectDoc.Config.Image = d.Config.Image
	d.inspectDoc.Config.Labels = d.Config.Labels
	d.inspectDoc.Config.Cmd = d.Config.Cmd
	d.inspectDoc.Config.Entrypoint = d.Config.Entrypoint
	d.inspectDoc.Config.ExposedPorts = d.Config.ExposedPorts
	d.inspectDoc.Config.User = d.Config.User
	d.inspectDoc.Config.Healthcheck = d.Config.Healthcheck
	d.inspectDoc.Config.EnvKeys = d.Config.Env
	c := buildContainer(&d.inspectDoc, now)
	if c == nil {
		return EngineContainer{}, ErrNotContainer
	}
	return EngineContainer{Container: c, FullID: d.ID, Tty: d.Config.Tty}, nil
}

// EngineSummary is one entry of GET /containers/json (no inspect).
type EngineSummary struct {
	ID     string // full id
	Name   string // without the leading '/'
	State  asterismv1.ContainerState
	Status string
	Labels map[string]string // raw; callers must filter before sending
}

// ParseEngineContainerList parses GET /containers/json?all=1.
func ParseEngineContainerList(raw []byte) ([]EngineSummary, error) {
	var docs []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		State  string            `json:"State"`
		Status string            `json:"Status"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("decode container list: %w", err)
	}
	out := make([]EngineSummary, 0, len(docs))
	for _, d := range docs {
		if !ValidContainerID(d.ID) {
			continue
		}
		name := ""
		for _, n := range d.Names {
			n = strings.TrimPrefix(n, "/")
			// Linked containers have extra names like "other/alias".
			if !strings.Contains(n, "/") {
				name = n
				break
			}
		}
		out = append(out, EngineSummary{ID: d.ID, Name: name, State: ParseState(d.State), Status: d.Status, Labels: d.Labels})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// EngineEvent is a decoded /events message (container scope only).
type EngineEvent struct {
	Type   string
	Action string // "start", "die", "health_status: healthy", ...
	ID     string
	Name   string
	Time   time.Time
	// ExitCode is set for "die" events.
	ExitCode string
}

// BaseAction strips the detail suffix ("health_status: healthy" ->
// "health_status", "exec_start: sh" -> "exec_start").
func (e EngineEvent) BaseAction() string {
	a, _, _ := strings.Cut(e.Action, ":")
	return strings.TrimSpace(a)
}

// ParseEngineEvent decodes one /events JSON message. Actor attributes are
// read for the name and exit code only; label attributes (which may carry
// secrets) are discarded.
func ParseEngineEvent(raw []byte) (EngineEvent, error) {
	var d struct {
		Type   string `json:"Type"`
		Action string `json:"Action"`
		Status string `json:"status"`
		ID     string `json:"id"`
		Actor  struct {
			ID         string `json:"ID"`
			Attributes struct {
				Name     string `json:"name"`
				ExitCode string `json:"exitCode"`
			} `json:"Attributes"`
		} `json:"Actor"`
		Time     int64 `json:"time"`
		TimeNano int64 `json:"timeNano"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return EngineEvent{}, fmt.Errorf("decode event: %w", err)
	}
	e := EngineEvent{Type: d.Type, Action: d.Action, ID: d.Actor.ID, Name: d.Actor.Attributes.Name, ExitCode: d.Actor.Attributes.ExitCode}
	if e.Action == "" {
		e.Action = d.Status
	}
	if e.ID == "" {
		e.ID = d.ID
	}
	switch {
	case d.TimeNano > 0:
		e.Time = time.Unix(0, d.TimeNano)
	case d.Time > 0:
		e.Time = time.Unix(d.Time, 0)
	}
	return e, nil
}
