package docker

import "testing"

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
