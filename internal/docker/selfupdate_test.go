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
