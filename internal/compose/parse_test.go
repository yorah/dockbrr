package compose_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dockbrr/internal/compose"
)

func writeCompose(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseServicesAndImages(t *testing.T) {
	dir := t.TempDir()
	f := writeCompose(t, dir, "compose.yml", `
services:
  web:
    image: nginx:1.25
  db:
    image: postgres:16
`)
	proj, err := compose.Parse(context.Background(), dir, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Services) != 2 {
		t.Fatalf("services = %d, want 2 (%+v)", len(proj.Services), proj.Services)
	}
	// Sorted by name: db, web.
	if proj.Services[0].Name != "db" || proj.Services[0].Image != "postgres:16" {
		t.Fatalf("service[0] = %+v, want db/postgres:16", proj.Services[0])
	}
	if proj.Services[1].Name != "web" || proj.Services[1].Image != "nginx:1.25" {
		t.Fatalf("service[1] = %+v, want web/nginx:1.25", proj.Services[1])
	}
}

func TestParseCapturesNamespaceModeFields(t *testing.T) {
	dir := t.TempDir()
	f := writeCompose(t, dir, "compose.yml", `
services:
  gluetun:
    image: qmcgaw/gluetun
  qbit:
    image: lscr.io/linuxserver/qbittorrent
    network_mode: service:gluetun
  sidecar:
    image: alpine
    ipc: service:gluetun
    pid: service:gluetun
`)
	proj, err := compose.Parse(context.Background(), dir, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	var qbit, sidecar compose.Service
	for _, s := range proj.Services {
		switch s.Name {
		case "qbit":
			qbit = s
		case "sidecar":
			sidecar = s
		}
	}
	if qbit.NetworkMode != "service:gluetun" {
		t.Fatalf("qbit.NetworkMode = %q, want service:gluetun", qbit.NetworkMode)
	}
	if sidecar.Ipc != "service:gluetun" || sidecar.Pid != "service:gluetun" {
		t.Fatalf("sidecar ipc/pid = %q/%q, want service:gluetun for both", sidecar.Ipc, sidecar.Pid)
	}
}

func TestNamespaceDependentsMatchesNetworkModeIpcAndPid(t *testing.T) {
	services := []compose.Service{
		{Name: "gluetun"},
		{Name: "qbit", NetworkMode: "service:gluetun"},
		{Name: "sidecar", Ipc: "service:gluetun"},
		{Name: "monitor", Pid: "service:gluetun"},
		{Name: "unrelated", NetworkMode: "service:other"},
		{Name: "plain-dependent"}, // depends_on-only, no namespace field
	}
	got := compose.NamespaceDependents(services, "gluetun")
	want := []string{"monitor", "qbit", "sidecar"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("dependents = %v, want %v (sorted)", got, want)
	}
}

func TestNamespaceDependentsNoneFound(t *testing.T) {
	services := []compose.Service{{Name: "gluetun"}, {Name: "qbit"}}
	if got := compose.NamespaceDependents(services, "gluetun"); len(got) != 0 {
		t.Fatalf("dependents = %v, want none", got)
	}
}

func TestNamespaceDependentsExcludesTargetItself(t *testing.T) {
	// Defensive: a service can't sensibly reference its own name, but must
	// never come back as its own dependent if it somehow does.
	services := []compose.Service{{Name: "gluetun", NetworkMode: "service:gluetun"}}
	if got := compose.NamespaceDependents(services, "gluetun"); len(got) != 0 {
		t.Fatalf("dependents = %v, want none (target excluded from its own results)", got)
	}
}

func TestNamespaceDependentsDedupesAcrossFields(t *testing.T) {
	// A service matching on more than one field (unusual, but possible) must
	// only appear once.
	services := []compose.Service{
		{Name: "gluetun"},
		{Name: "qbit", NetworkMode: "service:gluetun", Ipc: "service:gluetun"},
	}
	got := compose.NamespaceDependents(services, "gluetun")
	if len(got) != 1 || got[0] != "qbit" {
		t.Fatalf("dependents = %v, want exactly [qbit]", got)
	}
}

func TestParseInterpolatesDotEnv(t *testing.T) {
	dir := t.TempDir()
	f := writeCompose(t, dir, "compose.yml", `
services:
  cache:
    image: ${CACHE_IMAGE}
`)
	writeCompose(t, dir, ".env", "CACHE_IMAGE=redis:7.2.0\n")
	proj, err := compose.Parse(context.Background(), dir, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Services) != 1 || proj.Services[0].Image != "redis:7.2.0" {
		t.Fatalf("image = %+v, want cache/redis:7.2.0 (dotenv must be loaded for interpolation)", proj.Services)
	}
}

func TestParseInterpolatesProcessEnvOverridesDotEnv(t *testing.T) {
	dir := t.TempDir()
	f := writeCompose(t, dir, "compose.yml", `
services:
  cache:
    image: ${CACHE_IMAGE}
`)
	writeCompose(t, dir, ".env", "CACHE_IMAGE=redis:7.2.0\n")
	t.Setenv("CACHE_IMAGE", "redis:8.0.0") // shell env overrides .env
	proj, err := compose.Parse(context.Background(), dir, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if proj.Services[0].Image != "redis:8.0.0" {
		t.Fatalf("image = %q, want redis:8.0.0 (process env must override .env)", proj.Services[0].Image)
	}
}

func TestParseMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := compose.Parse(context.Background(), dir, []string{filepath.Join(dir, "nope.yml")})
	if err == nil {
		t.Fatal("expected an error for a missing compose file")
	}
}

func TestHashFilesDeterministicAndSensitive(t *testing.T) {
	dir := t.TempDir()
	f := writeCompose(t, dir, "compose.yml", "services:\n  web:\n    image: nginx:1.25\n")
	h1, err := compose.HashFiles([]string{f})
	if err != nil {
		t.Fatal(err)
	}
	if h1 == "" {
		t.Fatal("hash is empty")
	}
	h2, _ := compose.HashFiles([]string{f})
	if h1 != h2 {
		t.Fatal("hash not deterministic")
	}
	// Change content -> hash changes.
	if err := os.WriteFile(f, []byte("services:\n  web:\n    image: nginx:1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h3, _ := compose.HashFiles([]string{f})
	if h3 == h1 {
		t.Fatal("hash unchanged after content change")
	}
}

func TestHashFilesMissingErrors(t *testing.T) {
	if _, err := compose.HashFiles([]string{"/no/such/file.yml"}); err == nil {
		t.Fatal("expected an error hashing a missing file")
	}
}

func TestProjectNameStripsLeadingDashAndUnderscore(t *testing.T) {
	// Create a temp directory and a subdirectory starting with dash.
	tempDir := t.TempDir()
	weirdDir := filepath.Join(tempDir, "-weird")
	if err := os.Mkdir(weirdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a minimal valid compose file in the -weird directory.
	f := writeCompose(t, weirdDir, "compose.yml", `
services:
  web:
    image: nginx:latest
`)
	// Parse with the -weird directory as working dir.
	proj, err := compose.Parse(context.Background(), weirdDir, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	// Project name must not start with dash or underscore, and must not be empty.
	if len(proj.Name) == 0 {
		t.Fatal("project name is empty")
	}
	if proj.Name[0] == '-' || proj.Name[0] == '_' {
		t.Fatalf("project name starts with %q: %s", string(proj.Name[0]), proj.Name)
	}
}
