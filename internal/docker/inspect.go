package docker

import (
	"context"
	"encoding/json"
	"fmt"

	dcontainer "github.com/docker/docker/api/types/container"
)

// ContainerStatus is the Job Engine's view of a container for the health gate
// and the pre-mutation snapshot. RawJSON is the re-serialized inspect response
// stored in state_snapshots.prev_container_inspect.
type ContainerStatus struct {
	State   string // running|exited|restarting|created|...
	Health  string // healthy|unhealthy|starting|"" (no healthcheck)
	RawJSON string
}

// InspectStatus inspects a container and returns its normalized status plus the
// raw inspect JSON. Read-only.
func (cl *Client) InspectStatus(ctx context.Context, containerID string) (ContainerStatus, error) {
	ct, err := cl.c.ContainerInspect(ctx, containerID)
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("docker: inspect %s: %w", containerID, err)
	}
	return containerStatusFrom(ct)
}

// containerStatusFrom maps an inspect response to a ContainerStatus, embedding
// the re-marshaled inspect JSON. Pure, and the primary unit under test.
func containerStatusFrom(ct dcontainer.InspectResponse) (ContainerStatus, error) {
	var cs ContainerStatus
	if ct.State != nil {
		cs.State = string(ct.State.Status)
		if ct.State.Health != nil {
			cs.Health = string(ct.State.Health.Status)
		}
	}
	raw, err := json.Marshal(ct)
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("docker: marshal inspect: %w", err)
	}
	cs.RawJSON = string(raw)
	return cs, nil
}
