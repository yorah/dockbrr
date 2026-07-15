package docker

import (
	"encoding/json"
	"testing"

	dcontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func TestCreateArgsFromInspect(t *testing.T) {
	// Minimal inspect response with two networks and a couple of config fields.
	in := dcontainer.InspectResponse{
		ContainerJSONBase: &dcontainer.ContainerJSONBase{
			Name:       "/adoring_saha",
			HostConfig: &dcontainer.HostConfig{NetworkMode: "bridge"},
		},
		Config: &dcontainer.Config{
			Image: "busybox:1.36",
			Env:   []string{"FOO=bar"},
			Labels: map[string]string{"k": "v"},
		},
		NetworkSettings: &dcontainer.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"net-a": {Aliases: []string{"a"}},
				"net-b": {Aliases: []string{"b"}},
			},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	cfg, host, netCfg, extra, err := createArgsFromInspect(string(raw), "busybox:1.37")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "busybox:1.37" {
		t.Fatalf("Config.Image = %q, want the new image busybox:1.37", cfg.Image)
	}
	if len(cfg.Env) != 1 || cfg.Env[0] != "FOO=bar" || cfg.Labels["k"] != "v" {
		t.Fatalf("config not copied verbatim: %+v", cfg)
	}
	if host == nil || host.NetworkMode != "bridge" {
		t.Fatalf("HostConfig not copied: %+v", host)
	}
	// Exactly one endpoint attached at create; the other returned as an extra.
	if netCfg == nil || len(netCfg.EndpointsConfig) != 1 {
		t.Fatalf("NetworkingConfig should carry exactly one endpoint, got %+v", netCfg)
	}
	if len(extra) != 1 {
		t.Fatalf("expected exactly one extra network to connect after create, got %d", len(extra))
	}
	// The union of the create endpoint + extras must be both original networks.
	seen := map[string]bool{}
	for n := range netCfg.EndpointsConfig {
		seen[n] = true
	}
	for n := range extra {
		seen[n] = true
	}
	if !seen["net-a"] || !seen["net-b"] {
		t.Fatalf("networks lost: %+v", seen)
	}
}

func TestCreateArgsFromInspectRejectsEmpty(t *testing.T) {
	if _, _, _, _, err := createArgsFromInspect("", "img"); err == nil {
		t.Fatal("expected an error for empty inspect JSON")
	}
	if _, _, _, _, err := createArgsFromInspect(`{"Config":null}`, "img"); err == nil {
		t.Fatal("expected an error when Config is missing")
	}
}
