package docker

import (
	"context"
	"fmt"
	"strings"

	dcontainer "github.com/docker/docker/api/types/container"
	dimage "github.com/docker/docker/api/types/image"

	"dockbrr/internal/logger"
)

// Container is dockbrr's normalized view of a running/known container,
// derived from container inspect + image inspect.
type Container struct {
	ID          string
	Project     string   // com.docker.compose.project ("" => standalone)
	Service     string   // com.docker.compose.service
	WorkingDir  string   // com.docker.compose.project.working_dir
	ConfigFiles []string // com.docker.compose.project.config_files, "," split
	Name        string   // container name, leading "/" trimmed
	ImageRef    string   // Config.Image (the configured ref)
	RepoDigest  string   // "sha256:..." from image RepoDigests ("" if none)
	ImageID     string   // image content id (sha256:...)
	Pinned      bool     // ImageRef contains "@sha256:"
	State       string   // State.Status (running|exited|...)
	Healthcheck bool     // State.Health != nil
	Health      string   // State.Health.Status ("" if no healthcheck)
}

// containerFromInspect maps a Docker container inspect response and an image
// inspect response into a normalized Container. It is a pure function with no
// I/O, making it the primary unit under test.
func containerFromInspect(ct dcontainer.InspectResponse, img dimage.InspectResponse) Container {
	labels := map[string]string{}
	if ct.Config != nil {
		labels = ct.Config.Labels
	}

	// Compose labels.
	project := labels["com.docker.compose.project"]
	service := labels["com.docker.compose.service"]
	workingDir := labels["com.docker.compose.project.working_dir"]
	configFiles := splitConfigFiles(labels["com.docker.compose.project.config_files"])

	// Name: trim a single leading "/".
	name := strings.TrimPrefix(ct.Name, "/")

	// ImageRef and Pinned.
	imageRef := ""
	if ct.Config != nil {
		imageRef = ct.Config.Image
	}
	pinned := strings.Contains(imageRef, "@sha256:")

	// RepoDigest: find the entry whose repo matches imageRef's repo; else first.
	repoDigest := repoDigestFor(imageRef, img.RepoDigests)

	// ImageID: prefer image inspect ID, fall back to container inspect image field.
	imageID := img.ID
	if imageID == "" {
		imageID = ct.Image
	}

	// State and health.
	state := ""
	var healthcheck bool
	health := ""
	if ct.State != nil {
		state = string(ct.State.Status)
		if ct.State.Health != nil {
			healthcheck = true
			health = string(ct.State.Health.Status)
		}
	}

	return Container{
		ID:          ct.ID,
		Project:     project,
		Service:     service,
		WorkingDir:  workingDir,
		ConfigFiles: configFiles,
		Name:        name,
		ImageRef:    imageRef,
		RepoDigest:  repoDigest,
		ImageID:     imageID,
		Pinned:      pinned,
		State:       state,
		Healthcheck: healthcheck,
		Health:      health,
	}
}

// dockbrrOverridePrefix marks the temporary compose overrides dockbrr's Job
// Engine writes during apply/rollback (job.writePinOverride → "dockbrr-rollback-*.yml").
// docker compose bakes the full -f list into each container's config_files
// label, then dockbrr deletes the temp file, so a stale, now-missing path
// would linger in the label. On the next discovery cycle that missing path
// fails the config-file stat that flags a project "unmanaged", which refuses
// all further applies. These files are dockbrr-internal, never part of the
// user's compose project, so they are stripped from ConfigFiles at the source.
const dockbrrOverridePrefix = "dockbrr-rollback-"

// splitConfigFiles splits the compose config_files label and drops dockbrr's
// own ephemeral overrides. Returns nil (not an empty slice) when the label is
// empty or contains only overrides, matching the "no config files" case.
func splitConfigFiles(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, f := range parts {
		base := f
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if strings.HasPrefix(base, dockbrrOverridePrefix) {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// repoDigestFor returns the digest portion (after "@") of the RepoDigests
// entry whose repo matches imageRef's repo. If no matching entry is found, it
// returns the digest from the first entry. Returns "" if RepoDigests is empty.
func repoDigestFor(imageRef string, repoDigests []string) string {
	if len(repoDigests) == 0 {
		return ""
	}

	// Extract the repo portion of imageRef (everything before ":" or "@").
	imageRepo := imageRef
	if i := strings.IndexAny(imageRef, ":@"); i >= 0 {
		imageRepo = imageRef[:i]
	}

	// Search for an entry whose repo matches.
	for _, rd := range repoDigests {
		// rd is like "nginx@sha256:abc"
		at := strings.Index(rd, "@")
		if at < 0 {
			continue
		}
		rdRepo := rd[:at]
		if rdRepo == imageRepo {
			return rd[at+1:] // "sha256:abc"
		}
	}

	// Fall back to the first entry.
	at := strings.Index(repoDigests[0], "@")
	if at < 0 {
		return ""
	}
	return repoDigests[0][at+1:]
}

// Collect lists all containers (running and stopped) and returns the
// normalized Container slice. Per-container inspect/image errors are logged
// and that container is skipped (best effort). Collect returns an error only
// if the initial container list call fails.
func (cl *Client) Collect(ctx context.Context) ([]Container, error) {
	summaries, err := cl.c.ContainerList(ctx, dcontainer.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}

	result := make([]Container, 0, len(summaries))
	for _, s := range summaries {
		ct, err := cl.c.ContainerInspect(ctx, s.ID)
		if err != nil {
			logger.Warnf("docker: ContainerInspect %s: %v (skipped)", s.ID[:min(12, len(s.ID))], err)
			continue
		}

		img, err := cl.c.ImageInspect(ctx, ct.Image)
		if err != nil {
			logger.Warnf("docker: ImageInspect %s (container %s): %v (skipped)", ct.Image, s.ID[:min(12, len(s.ID))], err)
			continue
		}

		result = append(result, containerFromInspect(ct, img))
	}

	return result, nil
}
