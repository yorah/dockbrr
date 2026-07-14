package config_test

import (
	"path/filepath"
	"testing"

	"dockbrr/internal/config"
)

func noEnv(string) string { return "" }

func TestLoadDefaults(t *testing.T) {
	c, err := config.Load(nil, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.BindAddr != ":8080" {
		t.Fatalf("BindAddr = %q, want :8080", c.BindAddr)
	}
	if c.DockerSocket != "/var/run/docker.sock" {
		t.Fatalf("DockerSocket = %q", c.DockerSocket)
	}
	if !filepath.IsAbs(c.DataDir) {
		t.Fatalf("DataDir %q is not absolute", c.DataDir)
	}
	if filepath.Base(c.DataDir) != "data" {
		t.Fatalf("DataDir base = %q, want data", filepath.Base(c.DataDir))
	}
}

func TestEnvOverridesDefault(t *testing.T) {
	env := map[string]string{"DOCKBRR_BIND": ":9000"}
	c, err := config.Load(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.BindAddr != ":9000" {
		t.Fatalf("BindAddr = %q, want :9000", c.BindAddr)
	}
}

func TestEnvOverridesDataDir(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"DOCKBRR_DATA_DIR": dir}
	c, err := config.Load(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != want {
		t.Fatalf("DataDir = %q, want %q", c.DataDir, want)
	}
}

func TestEnvOverridesDockerSocket(t *testing.T) {
	env := map[string]string{"DOCKBRR_DOCKER_SOCKET": "/custom/docker.sock"}
	c, err := config.Load(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.DockerSocket != "/custom/docker.sock" {
		t.Fatalf("DockerSocket = %q, want /custom/docker.sock", c.DockerSocket)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	env := map[string]string{"DOCKBRR_BIND": ":9000"}
	c, err := config.Load([]string{"-bind", ":7000"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.BindAddr != ":7000" {
		t.Fatalf("BindAddr = %q, want :7000", c.BindAddr)
	}
}

func TestAdminBootstrapFromEnv(t *testing.T) {
	env := map[string]string{"DOCKBRR_ADMIN_USER": "root", "DOCKBRR_ADMIN_PASSWORD": "pw"}
	c, err := config.Load(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminUser != "root" || c.AdminPassword != "pw" {
		t.Fatalf("admin bootstrap = %q/%q", c.AdminUser, c.AdminPassword)
	}
}

func TestLoadLoggingDefaults(t *testing.T) {
	c, err := config.Load(nil, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", c.LogLevel)
	}
	if c.LogMaxSizeMB != 50 {
		t.Errorf("LogMaxSizeMB = %d, want 50", c.LogMaxSizeMB)
	}
	if c.LogMaxBackups != 3 {
		t.Errorf("LogMaxBackups = %d, want 3", c.LogMaxBackups)
	}
	// Default path is <data-dir>/logs/dockbrr.log.
	if filepath.Base(c.LogPath) != "dockbrr.log" || filepath.Base(filepath.Dir(c.LogPath)) != "logs" {
		t.Errorf("LogPath = %q, want .../logs/dockbrr.log", c.LogPath)
	}
	if !filepath.IsAbs(c.LogPath) {
		t.Errorf("LogPath %q is not absolute", c.LogPath)
	}
}

func TestLoadLoggingEnvAndFlagPrecedence(t *testing.T) {
	env := map[string]string{"DOCKBRR_LOG_LEVEL": "warn", "DOCKBRR_LOG_MAX_SIZE": "10"}
	getenv := func(k string) string { return env[k] }
	// flag beats env for level; env supplies size.
	c, err := config.Load([]string{"--log-level", "debug"}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug (flag wins)", c.LogLevel)
	}
	if c.LogMaxSizeMB != 10 {
		t.Errorf("LogMaxSizeMB = %d, want 10 (from env)", c.LogMaxSizeMB)
	}
}

func TestLoadLogPathEnvOverride(t *testing.T) {
	env := map[string]string{"DOCKBRR_LOG_PATH": "/var/log/dockbrr/app.log"}
	c, err := config.Load(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.LogPath != "/var/log/dockbrr/app.log" {
		t.Errorf("LogPath = %q, want the env override", c.LogPath)
	}
}
