package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"dockbrr/internal/version"
)

type dockerInfoDTO struct {
	Reachable  bool   `json:"reachable"`
	Version    string `json:"version,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
}

type authInfoDTO struct {
	Username string `json:"username"`
	Method   string `json:"method"`
}

type systemInfoDTO struct {
	Version     string        `json:"version"`
	Commit      string        `json:"commit"`
	CommitDirty bool          `json:"commit_dirty"`
	BuildDate   string        `json:"build_date"`
	GoVersion   string        `json:"go_version"`
	Platform    string        `json:"platform"`
	StartedAt   string        `json:"started_at,omitempty"`
	Docker      dockerInfoDTO `json:"docker"`
	DBPath      string        `json:"db_path"`
	BindAddr    string        `json:"bind_addr"`
	DataDir     string        `json:"data_dir"`
	Auth        authInfoDTO   `json:"auth"`
}

// buildStamps reads the VCS metadata Go embeds automatically when building from
// a git checkout (no ldflags needed). Absent under `go run` or -buildvcs=false,
// in which case the zero values travel to the UI, which renders a placeholder dash.
func buildStamps() (commit string, dirty bool, buildDate string) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, ""
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		case "vcs.time":
			buildDate = s.Value
		}
	}
	return commit, dirty, buildDate
}

// handleSystemInfo reports build, runtime, docker, storage and auth facts for
// the Settings → Application page. Read-only: it never mutates Docker or the
// store, and it deliberately carries no secrets (no tokens, no password hash).
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	commit, dirty, buildDate := buildStamps()
	out := systemInfoDTO{
		Version:     version.Version,
		Commit:      commit,
		CommitDirty: dirty,
		BuildDate:   buildDate,
		GoVersion:   runtime.Version(),
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		DBPath:      filepath.Join(s.cfg.DataDir, "dockbrr.db"),
		BindAddr:    s.cfg.BindAddr,
		DataDir:     s.cfg.DataDir,
		Auth:        authInfoDTO{Method: "password"},
	}
	if !s.deps.StartedAt.IsZero() {
		out.StartedAt = s.deps.StartedAt.UTC().Format(time.RFC3339)
	}
	if uid, ok := userIDFromCtx(r.Context()); ok {
		if u, err := s.userByID(uid); err == nil {
			out.Auth.Username = u.Username
		}
	}
	// One budget for BOTH docker probes: the ping and the version call share a
	// single deadline, so a wedged (not refused) socket costs this endpoint
	// dockerProbeTimeout in total, not once per probe.
	ctx, cancel := context.WithTimeout(r.Context(), dockerProbeTimeout)
	defer cancel()
	out.Docker.Reachable = s.dockerReachableCtx(ctx)
	if out.Docker.Reachable && s.deps.DockerVersion != nil {
		if v, api, err := s.deps.DockerVersion(ctx); err == nil {
			out.Docker.Version, out.Docker.APIVersion = v, api
		}
	}
	writeJSON(w, http.StatusOK, out)
}
