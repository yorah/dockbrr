package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dcontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

// UpdaterContainerName is the fixed name of the detached helper container that
// performs a self-update swap. A fixed name lets dockbrr clean up a leftover
// helper (from a failed swap) on its next boot.
const UpdaterContainerName = "dockbrr-self-update"

// SelfUpdateLabel marks the detached self-update helper container so
// discovery can identify and ignore it (it is not a user-managed service).
const SelfUpdateLabel = "dockbrr.self-update"

// ContainerImageRef returns a container's configured image reference (Config.Image).
// Read-only.
func (cl *Client) ContainerImageRef(ctx context.Context, id string) (string, error) {
	ct, err := cl.c.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("docker: inspect %s: %w", id, err)
	}
	if ct.Config == nil {
		return "", fmt.Errorf("docker: inspect %s: no config", id)
	}
	return ct.Config.Image, nil
}

// ImageVersion returns a local image's org.opencontainers.image.version label,
// the value GoReleaser stamps on published dockbrr images (.goreleaser.yaml).
// Empty string when the image carries no such label. Read-only; the image must
// already be present locally (the self-updater calls this right after
// ImagePull to confirm the pulled image is really the release it intends to
// install, since a floating tag like :latest can still resolve to an older
// image while a new release's image is mid-publish).
func (cl *Client) ImageVersion(ctx context.Context, ref string) (string, error) {
	img, err := cl.c.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("docker: inspect image %s: %w", ref, err)
	}
	if img.Config == nil {
		return "", nil
	}
	return img.Config.Labels["org.opencontainers.image.version"], nil
}

// SpawnUpdater creates and starts a detached helper container that runs `image`
// with `cmd`, bind-mounting the Docker socket so the helper can drive the daemon
// after dockbrr itself stops. It is NOT auto-removed: a failed swap leaves the
// container in place so its logs survive for `docker logs`; a successful swap's
// leftover is pruned on the next dockbrr boot (RemoveLeftoverUpdater). Any prior
// container with the fixed name is removed first. Returns the new helper
// container id.
func (cl *Client) SpawnUpdater(ctx context.Context, image string, cmd []string, socketPath string) (string, error) {
	// A fresh self-update is starting: any container still holding the fixed
	// name is a stale leftover from a prior attempt, so it is force-removed
	// even if (unexpectedly) still running. This is deliberately more
	// aggressive than the boot-time cleanup below, which leaves a running
	// helper alone on the chance a swap is genuinely still in flight.
	if id, ok, err := cl.ContainerIDByName(ctx, UpdaterContainerName); err != nil {
		return "", fmt.Errorf("docker: check for leftover updater: %w", err)
	} else if ok {
		if err := cl.c.ContainerRemove(ctx, id, dcontainer.RemoveOptions{Force: true}); err != nil {
			return "", fmt.Errorf("docker: remove leftover updater: %w", err)
		}
	}

	cfg, host := updaterContainerConfig(image, cmd, socketPath)
	resp, err := cl.c.ContainerCreate(ctx, cfg, host, nil, nil, UpdaterContainerName)
	if err != nil {
		return "", fmt.Errorf("docker: create updater: %w", err)
	}
	if err := cl.c.ContainerStart(ctx, resp.ID, dcontainer.StartOptions{}); err != nil {
		return "", fmt.Errorf("docker: start updater: %w", err)
	}
	return resp.ID, nil
}

// updaterContainerConfig builds the Config and HostConfig for the detached
// self-update helper container. Pure: no Docker calls, so it is the primary
// unit under test for the helper's container config (mirrors
// createArgsFromInspect's role for the recreate path).
func updaterContainerConfig(image string, cmd []string, socketPath string) (*dcontainer.Config, *dcontainer.HostConfig) {
	cfg := &dcontainer.Config{
		Image: image,
		Cmd:   cmd,
		Labels: map[string]string{
			SelfUpdateLabel: "1",
		},
		// The helper runs `self-update-swap`, not dockbrr's HTTP server, so
		// the image's baked-in healthcheck (which probes the HTTP server)
		// would always fail and show a failed-swap leftover as "unhealthy"
		// in `docker ps`, potentially tripping external alerting. Disable it
		// explicitly rather than inheriting the image's HEALTHCHECK.
		Healthcheck: &dcontainer.HealthConfig{Test: []string{"NONE"}},
	}
	host := &dcontainer.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeBind,
			Source: socketPath,
			Target: socketPath,
		}},
		RestartPolicy: dcontainer.RestartPolicy{Name: dcontainer.RestartPolicyDisabled},
		// Must stay false: a failed swap helper needs to survive so its logs
		// are available via `docker logs` for post-mortem.
		AutoRemove: false,
	}
	return cfg, host
}

// removeLeftoverUpdater best-effort removes a stopped leftover helper
// container from a prior self-update, by its fixed name. Called on boot. No
// Force: a still-running helper means a swap may genuinely still be in
// flight (e.g. dockbrr's own new instance started up quickly after the
// helper started it), and killing it mid-operation would be worse than
// leaving it; removal is a silent no-op in that case.
func (cl *Client) removeLeftoverUpdater(ctx context.Context) {
	id, ok, err := cl.ContainerIDByName(ctx, UpdaterContainerName)
	if err != nil || !ok {
		return
	}
	_ = cl.c.ContainerRemove(ctx, id, dcontainer.RemoveOptions{})
}

// RemoveLeftoverUpdater is the exported boot-cleanup entry point: it prunes a
// leftover helper container from a self-update that ran before the previous
// dockbrr exit (successful or not).
func (cl *Client) RemoveLeftoverUpdater(ctx context.Context) { cl.removeLeftoverUpdater(ctx) }

// swapOps lists exactly the Client methods SwapContainer and rollback call.
// It exists purely as a test seam: a fake implementing this interface lets
// the stop/remove/create/start/rollback sequencing be exercised without a
// live Docker daemon. *Client satisfies it with no adapter.
type swapOps interface {
	InspectStatus(ctx context.Context, containerID string) (ContainerStatus, error)
	ContainerStop(ctx context.Context, id string) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerForceRemove(ctx context.Context, id string) error
	ContainerCreateFromInspect(ctx context.Context, inspectJSON, newImage, name string) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerIDByName(ctx context.Context, name string) (string, bool, error)
}

// SwapContainer replaces the target container with newImage, preserving its
// full config via ContainerCreateFromInspect. It is executed by the detached
// updater helper (the target is dockbrr itself, so the process calling this
// normally dies at the stop; the helper survives). On a create/start failure
// AFTER the old container was removed, it best-effort recreates the old
// container from the same captured inspect with its original image, so a
// failed swap lands back on a running old version rather than nothing.
//
// This is a thin wrapper around swapContainer, which operates on the swapOps
// interface so the sequencing can be unit-tested with a fake.
func (cl *Client) SwapContainer(ctx context.Context, targetID, newImage string, logf func(string)) error {
	return swapContainer(ctx, cl, targetID, newImage, logf)
}

func swapContainer(ctx context.Context, ops swapOps, targetID, newImage string, logf func(string)) error {
	if logf == nil {
		logf = func(string) {}
	}
	st, err := ops.InspectStatus(ctx, targetID)
	if err != nil {
		return fmt.Errorf("swap: inspect target: %w", err)
	}
	name, oldImage, err := parseInspectNameImage(st.RawJSON)
	if err != nil {
		return fmt.Errorf("swap: %w", err)
	}

	logf("stopping " + name)
	if err := ops.ContainerStop(ctx, targetID); err != nil {
		return fmt.Errorf("swap: stop target: %w", err)
	}
	logf("removing " + name)
	if err := ops.ContainerRemove(ctx, targetID); err != nil {
		return fmt.Errorf("swap: remove target: %w", err)
	}

	logf("creating " + name + " from " + newImage)
	newID, err := ops.ContainerCreateFromInspect(ctx, st.RawJSON, newImage, name)
	if err != nil {
		logf("create failed, rolling back to " + oldImage + ": " + err.Error())
		return rollback(ctx, ops, st.RawJSON, oldImage, name, logf, fmt.Errorf("create new container: %w", err))
	}
	if err := ops.ContainerStart(ctx, newID); err != nil {
		logf("start failed, rolling back to " + oldImage + ": " + err.Error())
		_ = ops.ContainerRemove(ctx, newID)
		return rollback(ctx, ops, st.RawJSON, oldImage, name, logf, fmt.Errorf("start new container: %w", err))
	}
	logf("started " + name + " (" + newID + ") on " + newImage)
	return nil
}

// rollback recreates the old container from the captured inspect (with the
// original image) after a failed swap, so dockbrr is not left dead. It returns
// the original swap error wrapped, or a combined error if the rollback itself
// fails. Best-effort: every step is logged via logf for post-mortem.
//
// It is idempotent on `name` before recreating: ContainerCreateFromInspect can
// itself create the container and only then fail on a later step (e.g.
// NetworkConnect for a second network), leaving a container already holding
// `name`. If that happened on the failed new-image create, a plain recreate
// here would collide on the name and dockbrr would be left dead. So any
// container currently holding `name` is force-removed first, mirroring the
// leftover-handling SpawnUpdater does for its own fixed name.
func rollback(ctx context.Context, ops swapOps, rawJSON, oldImage, name string, logf func(string), cause error) error {
	if id, ok, err := ops.ContainerIDByName(ctx, name); err != nil {
		logf("rollback: check for name collision failed: " + err.Error())
		return fmt.Errorf("swap failed (%w) and rollback failed: check for name collision: %v", cause, err)
	} else if ok {
		logf("rollback: freeing name " + name + " held by " + id)
		if err := ops.ContainerForceRemove(ctx, id); err != nil {
			logf("rollback: force-remove of colliding container failed: " + err.Error())
			return fmt.Errorf("swap failed (%w) and rollback failed: free name %s: %v", cause, name, err)
		}
	}

	id, err := ops.ContainerCreateFromInspect(ctx, rawJSON, oldImage, name)
	if err != nil {
		logf("rollback create failed: " + err.Error())
		return fmt.Errorf("swap failed (%w) and rollback failed: %v", cause, err)
	}
	if err := ops.ContainerStart(ctx, id); err != nil {
		logf("rollback start failed: " + err.Error())
		return fmt.Errorf("swap failed (%w) and rollback start failed: %v", cause, err)
	}
	logf("rolled back to " + oldImage)
	return fmt.Errorf("swap failed, rolled back to previous image: %w", cause)
}

// parseInspectNameImage extracts the container name (leading slash trimmed)
// and its configured image from an inspect JSON blob. Pure.
func parseInspectNameImage(rawJSON string) (name, image string, err error) {
	if strings.TrimSpace(rawJSON) == "" {
		return "", "", errors.New("docker: empty inspect JSON")
	}
	var meta struct {
		Name   string
		Config struct{ Image string }
	}
	if err := json.Unmarshal([]byte(rawJSON), &meta); err != nil {
		return "", "", fmt.Errorf("docker: parse inspect: %w", err)
	}
	return strings.TrimPrefix(meta.Name, "/"), meta.Config.Image, nil
}
