package dockerapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/actions"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

var _ actions.Executor = (*Client)(nil)

// ContainerAction implements actions.Executor: it POSTs one of the five
// allow-listed lifecycle endpoints and returns the container as inspected
// afterwards. 404 maps to actions.ErrNotFound and 409 (or 304 "already in
// that state" for start/stop) to actions.ErrConflict.
func (c *Client) ContainerAction(ctx context.Context, containerID string, action v1.ContainerAction, timeout time.Duration) (*v1.Container, error) {
	if !dockercli.ValidContainerID(containerID) {
		return nil, fmt.Errorf("invalid container id %q: %w", truncate(containerID), actions.ErrNotFound)
	}
	var verb string
	switch action {
	case v1.ContainerAction_CONTAINER_ACTION_START:
		verb = "start"
	case v1.ContainerAction_CONTAINER_ACTION_STOP:
		verb = "stop"
	case v1.ContainerAction_CONTAINER_ACTION_RESTART:
		verb = "restart"
	case v1.ContainerAction_CONTAINER_ACTION_PAUSE:
		verb = "pause"
	case v1.ContainerAction_CONTAINER_ACTION_UNPAUSE:
		verb = "unpause"
	default:
		return nil, fmt.Errorf("unsupported container action %v", action)
	}
	q := url.Values{}
	if verb == "stop" || verb == "restart" {
		secs := int(timeout / time.Second)
		if secs <= 0 {
			secs = int(actions.DefaultTimeout / time.Second)
		}
		q.Set("t", strconv.Itoa(secs))
	}
	// Stop/restart may legitimately take the whole grace period.
	actx, cancel := context.WithTimeout(ctx, timeout+30*time.Second)
	defer cancel()
	resp, err := c.do(actx, c.stream, http.MethodPost, "/containers/"+containerID+"/"+verb, q)
	switch {
	case IsStatus(err, http.StatusNotFound):
		return nil, fmt.Errorf("%s %s: %w", verb, dockercli.ShortID(containerID), actions.ErrNotFound)
	case IsStatus(err, http.StatusConflict):
		return nil, fmt.Errorf("%s %s: %w", verb, dockercli.ShortID(containerID), actions.ErrConflict)
	case err != nil:
		return nil, fmt.Errorf("%s %s: %w", verb, dockercli.ShortID(containerID), err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, fmt.Errorf("%s %s: already in that state: %w", verb, dockercli.ShortID(containerID), actions.ErrConflict)
	}
	raw, err := c.InspectContainer(ctx, containerID)
	if err != nil {
		return nil, nil // the action succeeded; inventory refreshes via events
	}
	ec, err := dockercli.ContainerFromEngine(raw, time.Now())
	if err != nil {
		return nil, nil
	}
	if c.PostProcess != nil {
		c.PostProcess(ec.Container)
	}
	return ec.Container, nil
}
