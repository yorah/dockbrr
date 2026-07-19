package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseInspectNameAndImage(t *testing.T) {
	raw := `{"Name":"/dockbrr","Config":{"Image":"ghcr.io/yorah/dockbrr:1.1.0"}}`
	name, image, err := parseInspectNameImage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if name != "dockbrr" {
		t.Errorf("name = %q, want dockbrr (leading slash trimmed)", name)
	}
	if image != "ghcr.io/yorah/dockbrr:1.1.0" {
		t.Errorf("image = %q", image)
	}
}

func TestParseInspectNameImageEmpty(t *testing.T) {
	if _, _, err := parseInspectNameImage(""); err == nil {
		t.Fatal("expected error on empty inspect JSON")
	}
}

func TestParseInspectNameImageMalformed(t *testing.T) {
	if _, _, err := parseInspectNameImage("{not json"); err == nil {
		t.Fatal("expected error on malformed inspect JSON")
	}
}

func TestParseInspectNameImageNoLeadingSlash(t *testing.T) {
	// Docker names are always "/"-prefixed in real inspect output, but the
	// parser should not choke if one arrives without it (TrimPrefix is a
	// no-op when the prefix is absent).
	raw := `{"Name":"dockbrr","Config":{"Image":"busybox:1.36"}}`
	name, image, err := parseInspectNameImage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if name != "dockbrr" {
		t.Errorf("name = %q, want dockbrr", name)
	}
	if image != "busybox:1.36" {
		t.Errorf("image = %q", image)
	}
}

// FINAL-1: the helper must not inherit dockbrr's image HEALTHCHECK (it runs
// self-update-swap, not the HTTP server, so the probe would always fail and
// mark a failed-swap leftover "unhealthy" in `docker ps`).
func TestUpdaterContainerConfigDisablesHealthcheck(t *testing.T) {
	cfg, _ := updaterContainerConfig("ghcr.io/yorah/dockbrr:1.1.0", []string{"self-update-swap"}, "/var/run/docker.sock")
	if cfg.Healthcheck == nil {
		t.Fatal("expected Healthcheck to be set (disabled), got nil")
	}
	if len(cfg.Healthcheck.Test) != 1 || cfg.Healthcheck.Test[0] != "NONE" {
		t.Fatalf("Healthcheck.Test = %v, want [NONE]", cfg.Healthcheck.Test)
	}
}

// FINAL-2: the helper carries a distinguishing label so a later discovery-side
// change can filter it out of user-managed services.
func TestUpdaterContainerConfigCarriesSelfUpdateLabel(t *testing.T) {
	cfg, _ := updaterContainerConfig("ghcr.io/yorah/dockbrr:1.1.0", []string{"self-update-swap"}, "/var/run/docker.sock")
	if cfg.Labels[SelfUpdateLabel] != "1" {
		t.Fatalf("Labels[%q] = %q, want \"1\"", SelfUpdateLabel, cfg.Labels[SelfUpdateLabel])
	}
}

func TestUpdaterContainerConfigMountsSocket(t *testing.T) {
	_, host := updaterContainerConfig("img", nil, "/var/run/docker.sock")
	if len(host.Mounts) != 1 || host.Mounts[0].Source != "/var/run/docker.sock" || host.Mounts[0].Target != "/var/run/docker.sock" {
		t.Fatalf("expected the socket path bind-mounted at itself, got %+v", host.Mounts)
	}
}

// fakeSwapOps is a scripted in-memory implementation of swapOps, used to
// unit-test swapContainer/rollback sequencing (T7-M1) without a live daemon.
type fakeSwapOps struct {
	inspectRawJSON string

	createErr error // returned on the FIRST ContainerCreateFromInspect call only
	startErr  error // returned on the FIRST ContainerStart call only

	idByName   string
	idByNameOK bool

	calls           []string // ordered method tags, for sequencing assertions
	createImages    []string // image arg of each ContainerCreateFromInspect call, in call order
	createCount     int
	startCount      int
	removedIDs      []string
	forceRemovedIDs []string
}

func (f *fakeSwapOps) InspectStatus(ctx context.Context, containerID string) (ContainerStatus, error) {
	f.calls = append(f.calls, "inspect")
	return ContainerStatus{RawJSON: f.inspectRawJSON}, nil
}

func (f *fakeSwapOps) ContainerStop(ctx context.Context, id string) error {
	f.calls = append(f.calls, "stop")
	return nil
}

func (f *fakeSwapOps) ContainerRemove(ctx context.Context, id string) error {
	f.calls = append(f.calls, "remove:"+id)
	f.removedIDs = append(f.removedIDs, id)
	return nil
}

func (f *fakeSwapOps) ContainerForceRemove(ctx context.Context, id string) error {
	f.calls = append(f.calls, "force-remove:"+id)
	f.forceRemovedIDs = append(f.forceRemovedIDs, id)
	return nil
}

func (f *fakeSwapOps) ContainerCreateFromInspect(ctx context.Context, inspectJSON, newImage, name string) (string, error) {
	f.createCount++
	f.calls = append(f.calls, "create:"+newImage)
	f.createImages = append(f.createImages, newImage)
	if f.createCount == 1 && f.createErr != nil {
		return "", f.createErr
	}
	return "new-id-" + newImage, nil
}

func (f *fakeSwapOps) ContainerStart(ctx context.Context, id string) error {
	f.startCount++
	f.calls = append(f.calls, "start:"+id)
	if f.startCount == 1 && f.startErr != nil {
		return f.startErr
	}
	return nil
}

func (f *fakeSwapOps) ContainerIDByName(ctx context.Context, name string) (string, bool, error) {
	f.calls = append(f.calls, "idbyname")
	return f.idByName, f.idByNameOK, nil
}

// T7-M1 happy path: stop/remove/create/start all succeed, no rollback logic
// is invoked at all.
func TestSwapContainerHappyPath(t *testing.T) {
	ops := &fakeSwapOps{
		inspectRawJSON: `{"Name":"/dockbrr","Config":{"Image":"old:1"}}`,
	}
	var logs []string
	err := swapContainer(context.Background(), ops, "target-id", "new:2", func(s string) { logs = append(logs, s) })
	if err != nil {
		t.Fatalf("swapContainer returned error: %v", err)
	}
	if ops.createCount != 1 {
		t.Fatalf("expected exactly one create call, got %d", ops.createCount)
	}
	if ops.createImages[0] != "new:2" {
		t.Fatalf("create used image %q, want new:2", ops.createImages[0])
	}
	if ops.startCount != 1 {
		t.Fatalf("expected exactly one start call, got %d", ops.startCount)
	}
	if len(ops.forceRemovedIDs) != 0 {
		t.Fatalf("no rollback expected on happy path, but force-remove was called: %v", ops.forceRemovedIDs)
	}
	for _, c := range ops.calls {
		if c == "idbyname" {
			t.Fatalf("rollback's name-collision check ran on a successful swap: calls=%v", ops.calls)
		}
	}
}

// T7-M1 rollback path: the create for the new image fails after a container
// is already occupying the name (simulating ContainerCreateFromInspect having
// created the container and then failed on a later step, e.g. NetworkConnect).
// Verifies: (a) the occupant is freed via ContainerIDByName + force-remove,
// (b) the recreate uses the captured inspect with the OLD image, not the new
// one, (c) the returned error indicates a rollback happened.
func TestSwapContainerRollsBackOnCreateFailure(t *testing.T) {
	ops := &fakeSwapOps{
		inspectRawJSON: `{"Name":"/dockbrr","Config":{"Image":"old:1"}}`,
		createErr:      errors.New("create failed: name already in use"),
		idByName:       "occupant-id",
		idByNameOK:     true,
	}
	var logs []string
	err := swapContainer(context.Background(), ops, "target-id", "new:2", func(s string) { logs = append(logs, s) })
	if err == nil {
		t.Fatal("expected an error on create failure")
	}
	if !strings.Contains(err.Error(), "rolled back to previous image") {
		t.Fatalf("error does not indicate a rollback: %v", err)
	}
	if len(ops.forceRemovedIDs) != 1 || ops.forceRemovedIDs[0] != "occupant-id" {
		t.Fatalf("expected the occupant (occupant-id) force-removed to free the name, got %v", ops.forceRemovedIDs)
	}
	if ops.createCount != 2 {
		t.Fatalf("expected 2 create calls (failed new-image create + rollback recreate), got %d", ops.createCount)
	}
	if ops.createImages[0] != "new:2" {
		t.Fatalf("first create used image %q, want new:2", ops.createImages[0])
	}
	if ops.createImages[1] != "old:1" {
		t.Fatalf("rollback create used image %q, want old:1 (the original image, not the new one)", ops.createImages[1])
	}
	if ops.startCount != 1 {
		t.Fatalf("expected exactly one start call (the rolled-back container), got %d", ops.startCount)
	}
	if len(logs) == 0 {
		t.Fatal("expected rollback progress to be logged via logf")
	}
}
