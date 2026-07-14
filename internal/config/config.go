// Package config loads dockbrr's bootstrap configuration, the minimal set
// needed before the database exists. All other config is UI/DB-managed.
package config

import (
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
)

type Config struct {
	DataDir       string
	BindAddr      string
	DockerSocket  string
	AdminUser     string
	AdminPassword string
	LogPath       string
	LogLevel      string
	LogMaxSizeMB  int
	LogMaxBackups int
}

// Load resolves config from flags (highest priority), then env, then defaults.
// getenv is injected for testability (pass os.Getenv in production).
func Load(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("dockbrr", flag.ContinueOnError)
	var c Config
	fs.StringVar(&c.DataDir, "data-dir",
		envOr(getenv, "DOCKBRR_DATA_DIR", "./data"), "data directory for sqlite + secret key")
	fs.StringVar(&c.BindAddr, "bind",
		envOr(getenv, "DOCKBRR_BIND", ":8080"), "HTTP listen address")
	fs.StringVar(&c.DockerSocket, "docker-socket",
		envOr(getenv, "DOCKBRR_DOCKER_SOCKET", "/var/run/docker.sock"), "Docker socket path")
	fs.StringVar(&c.AdminUser, "admin-user",
		envOr(getenv, "DOCKBRR_ADMIN_USER", ""), "bootstrap admin username (applied only when no user exists)")
	fs.StringVar(&c.AdminPassword, "admin-password",
		envOr(getenv, "DOCKBRR_ADMIN_PASSWORD", ""), "bootstrap admin password (applied only when no user exists)")
	fs.StringVar(&c.LogPath, "log-path",
		envOr(getenv, "DOCKBRR_LOG_PATH", ""), "log file path (empty => <data-dir>/logs/dockbrr.log; console always on)")
	fs.StringVar(&c.LogLevel, "log-level",
		envOr(getenv, "DOCKBRR_LOG_LEVEL", "info"), "log level: trace|debug|info|warn|error")
	logMaxSize := fs.Int("log-max-size",
		envOrInt(getenv, "DOCKBRR_LOG_MAX_SIZE", 50), "max log file size in MB before rotation")
	logMaxBackups := fs.Int("log-max-backups",
		envOrInt(getenv, "DOCKBRR_LOG_MAX_BACKUPS", 3), "number of rotated log files to keep")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data-dir: %w", err)
	}
	c.DataDir = abs
	c.LogMaxSizeMB = *logMaxSize
	c.LogMaxBackups = *logMaxBackups
	if c.LogPath == "" {
		c.LogPath = filepath.Join(c.DataDir, "logs", "dockbrr.log")
	}
	return c, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func envOrInt(getenv func(string) string, key string, def int) int {
	if v := getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
