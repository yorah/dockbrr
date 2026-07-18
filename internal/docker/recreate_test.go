package docker

import (
	"encoding/json"
	"testing"

	dcontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
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

func TestCreateArgsFromInspectReattachesVolumes(t *testing.T) {
	// An anonymous volume at /data (only present in Mounts, generated Name),
	// plus a named-volume bind already covering /named via HostConfig.Binds.
	in := dcontainer.InspectResponse{
		ContainerJSONBase: &dcontainer.ContainerJSONBase{
			Name: "/adoring_saha",
			HostConfig: &dcontainer.HostConfig{
				Binds: []string{"myvol:/named"},
			},
		},
		Config: &dcontainer.Config{Image: "postgres:16"},
		Mounts: []dcontainer.MountPoint{
			{Type: mount.TypeVolume, Name: "abc123", Destination: "/data", RW: true},
			{Type: mount.TypeVolume, Name: "myvol", Destination: "/named", RW: true},
			{Type: mount.TypeBind, Source: "/host/path", Destination: "/host-bound", RW: true},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	_, host, _, _, err := createArgsFromInspect(string(raw), "postgres:16")
	if err != nil {
		t.Fatal(err)
	}
	if host == nil {
		t.Fatal("HostConfig is nil")
	}

	var dataMount *mount.Mount
	for i := range host.Mounts {
		if host.Mounts[i].Target == "/data" {
			dataMount = &host.Mounts[i]
		}
		if host.Mounts[i].Target == "/named" {
			t.Fatalf("duplicate mount for /named (already covered by Binds): %+v", host.Mounts)
		}
	}
	if dataMount == nil {
		t.Fatalf("expected an anonymous-volume mount for /data, got HostConfig.Mounts=%+v", host.Mounts)
	}
	if dataMount.Type != mount.TypeVolume || dataMount.Source != "abc123" {
		t.Fatalf("anonymous volume mount = %+v, want Type=volume Source=abc123", dataMount)
	}
	if dataMount.ReadOnly {
		t.Fatalf("anonymous volume mount should be read-write (RW=true in inspect): %+v", dataMount)
	}
	// /named must still be reachable only via Binds, not duplicated in Mounts.
	if len(host.Binds) != 1 || host.Binds[0] != "myvol:/named" {
		t.Fatalf("Binds should be untouched: %+v", host.Binds)
	}
	// The bind-type mount (/host-bound) must not be copied into HostConfig.Mounts
	// either: bind mounts are represented via Binds/other mechanisms, not here.
	for _, m := range host.Mounts {
		if m.Target == "/host-bound" {
			t.Fatalf("bind-type inspect mount should not be added to HostConfig.Mounts: %+v", host.Mounts)
		}
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

func TestCreateArgsFromInspectPreservesComposeLabels(t *testing.T) {
	raw := `{"Name":"/dockbrr","Config":{"Image":"old:1","Labels":{"com.docker.compose.project":"stack","com.docker.compose.service":"dockbrr"}},"HostConfig":{},"NetworkSettings":{"Networks":{}}}`
	cfg, _, _, _, err := createArgsFromInspect(raw, "new:2")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "new:2" {
		t.Errorf("image = %q, want new:2", cfg.Image)
	}
	if cfg.Labels["com.docker.compose.project"] != "stack" {
		t.Errorf("compose project label dropped: %v", cfg.Labels)
	}
	if cfg.Labels["com.docker.compose.service"] != "dockbrr" {
		t.Errorf("compose service label dropped: %v", cfg.Labels)
	}
}
