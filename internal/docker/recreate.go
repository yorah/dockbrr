package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	dcontainer "github.com/docker/docker/api/types/container"
	dimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
)

// ImagePull pulls ref and blocks until the pull completes (the SDK returns a
// progress stream that must be drained for the pull to finish). Mutating:
// called only by the Job Engine.
func (cl *Client) ImagePull(ctx context.Context, ref string) error {
	rc, err := cl.c.ImagePull(ctx, ref, dimage.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker: pull %s (drain): %w", ref, err)
	}
	return nil
}

// ContainerCreateFromInspect recreates a container from a stored inspect JSON
// (as produced by InspectStatus.RawJSON) with Config.Image swapped to newImage
// and the given name. It creates with the first network endpoint and connects
// any remaining networks afterward. Returns the new container id.
func (cl *Client) ContainerCreateFromInspect(ctx context.Context, inspectJSON, newImage, name string) (string, error) {
	cfg, host, netCfg, extra, err := createArgsFromInspect(inspectJSON, newImage)
	if err != nil {
		return "", err
	}
	resp, err := cl.c.ContainerCreate(ctx, cfg, host, netCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("docker: create %s: %w", name, err)
	}
	for netName, ep := range extra {
		if err := cl.c.NetworkConnect(ctx, netName, resp.ID, ep); err != nil {
			return "", fmt.Errorf("docker: connect %s to %s: %w", resp.ID, netName, err)
		}
	}
	return resp.ID, nil
}

// createArgsFromInspect parses a container InspectResponse JSON and returns the
// ContainerCreate arguments with Config.Image replaced by newImage. The first
// network endpoint (map iteration order is not stable, so "first" is arbitrary
// but deterministic within one call) goes into the returned NetworkingConfig;
// the remaining endpoints are returned separately to connect after create.
// Pure: no Docker calls, the primary unit under test.
func createArgsFromInspect(inspectJSON, newImage string) (*dcontainer.Config, *dcontainer.HostConfig, *network.NetworkingConfig, map[string]*network.EndpointSettings, error) {
	if strings.TrimSpace(inspectJSON) == "" {
		return nil, nil, nil, nil, errors.New("docker: empty inspect JSON")
	}
	var in dcontainer.InspectResponse
	if err := json.Unmarshal([]byte(inspectJSON), &in); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("docker: parse inspect: %w", err)
	}
	if in.Config == nil {
		return nil, nil, nil, nil, errors.New("docker: inspect has no Config")
	}
	cfg := in.Config
	cfg.Image = newImage

	var host *dcontainer.HostConfig
	if in.ContainerJSONBase != nil {
		host = in.HostConfig
	}
	if host != nil {
		reattachVolumeMounts(host, in.Mounts)
	}

	nets := map[string]*network.EndpointSettings{}
	if in.NetworkSettings != nil {
		for name, ep := range in.NetworkSettings.Networks {
			nets[name] = ep
		}
	}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	extra := map[string]*network.EndpointSettings{}
	first := true
	for name, ep := range nets {
		if first {
			netCfg.EndpointsConfig[name] = ep
			first = false
		} else {
			extra[name] = ep
		}
	}
	return cfg, host, netCfg, extra, nil
}

// reattachVolumeMounts ensures named and anonymous volumes recorded in the
// inspect's runtime Mounts array are reattached to the new container. Docker
// only persists volume identity through HostConfig.Binds for volumes bound by
// name at `docker run` time; anonymous volumes and image VOLUME declarations
// exist only in inspect.Mounts (Type "volume", a generated Name). Without
// this, recreate would provision fresh empty volumes at those destinations
// and orphan the old data.
//
// Bind mounts (Type "bind") are already represented in HostConfig.Binds and
// are skipped here. Any destination already covered by an existing
// HostConfig.Mounts entry or Binds entry is left untouched to avoid Docker's
// "Duplicate mount point" error on create.
func reattachVolumeMounts(host *dcontainer.HostConfig, mounts []dcontainer.MountPoint) {
	covered := map[string]bool{}
	for _, m := range host.Mounts {
		if m.Target != "" {
			covered[m.Target] = true
		}
	}
	for _, b := range host.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 && parts[1] != "" {
			covered[parts[1]] = true
		}
	}
	for _, mp := range mounts {
		if mp.Type != mount.TypeVolume || mp.Name == "" {
			continue
		}
		if covered[mp.Destination] {
			continue
		}
		host.Mounts = append(host.Mounts, mount.Mount{
			Type:     mount.TypeVolume,
			Source:   mp.Name,
			Target:   mp.Destination,
			ReadOnly: !mp.RW,
		})
		covered[mp.Destination] = true
	}
}
