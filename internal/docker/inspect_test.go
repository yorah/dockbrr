package docker

import (
	"strings"
	"testing"

	dcontainer "github.com/docker/docker/api/types/container"
)

func TestContainerStatusFromRunningHealthy(t *testing.T) {
	ct := dcontainer.InspectResponse{
		ContainerJSONBase: &dcontainer.ContainerJSONBase{
			ID: "c1",
			State: &dcontainer.State{
				Status: "running",
				Health: &dcontainer.Health{Status: "healthy"},
			},
		},
	}
	got, err := containerStatusFrom(ct)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "running" {
		t.Fatalf("state = %q, want running", got.State)
	}
	if got.Health != "healthy" {
		t.Fatalf("health = %q, want healthy", got.Health)
	}
	if !strings.Contains(got.RawJSON, "\"running\"") {
		t.Fatalf("raw json missing state: %s", got.RawJSON)
	}
}

func TestContainerStatusFromNoHealthcheck(t *testing.T) {
	ct := dcontainer.InspectResponse{
		ContainerJSONBase: &dcontainer.ContainerJSONBase{
			ID:    "c2",
			State: &dcontainer.State{Status: "running"},
		},
	}
	got, err := containerStatusFrom(ct)
	if err != nil {
		t.Fatal(err)
	}
	if got.Health != "" {
		t.Fatalf("health = %q, want empty (no healthcheck)", got.Health)
	}
}

func TestContainerStatusFromExited(t *testing.T) {
	ct := dcontainer.InspectResponse{
		ContainerJSONBase: &dcontainer.ContainerJSONBase{
			ID:    "c3",
			State: &dcontainer.State{Status: "exited"},
		},
	}
	got, err := containerStatusFrom(ct)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "exited" {
		t.Fatalf("state = %q, want exited", got.State)
	}
}
