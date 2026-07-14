package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"dockbrr/internal/version"
)

// handleStatus reports scheduler/docker liveness for the dashboard stats row.
// All fields are best-effort reads; the endpoint never errors on missing keys.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	last, _ := s.deps.Settings.Get("last_check_all")
	pollStr, _ := s.deps.Settings.Get("poll_interval_seconds")
	poll, err := strconv.Atoi(pollStr)
	if err != nil || poll <= 0 {
		poll = 900 // mirrors the 15m default in cmd/dockbrr/main.go
	}
	out := map[string]any{
		"last_check_all":        last,
		"poll_interval_seconds": poll,
		"docker_reachable":      s.dockerReachable(r),
		"version":               version.Version,
	}
	// The scheduler's next tick, when it's running. Deliberately not derived from
	// last_check_all + poll: a manual scan stamps last_check_all but leaves the
	// scheduler's ticker untouched, so the arithmetic would drift.
	if s.deps.NextScan != nil {
		if next := s.deps.NextScan(); !next.IsZero() {
			out["next_check_all"] = next.UTC().Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// dockerProbeTimeout bounds a request's docker liveness work. It is the budget
// for ALL docker probes a single request makes, not per probe: /api/system/info
// pings and then asks for the version, and both must share one deadline (see
// handleSystemInfo) so a wedged socket cannot stall the request for 2x this.
const dockerProbeTimeout = 2 * time.Second

// dockerReachable re-probes the daemon with a short timeout so the reported
// liveness is current, not a boot snapshot. It owns the deadline; callers that
// run further docker probes in the same request must use dockerReachableCtx
// with a shared context instead.
func (s *Server) dockerReachable(r *http.Request) bool {
	ctx, cancel := context.WithTimeout(r.Context(), dockerProbeTimeout)
	defer cancel()
	return s.dockerReachableCtx(ctx)
}

// dockerReachableCtx probes the daemon on a caller-owned context. A nil pinger
// (Docker never came up) reports false.
func (s *Server) dockerReachableCtx(ctx context.Context) bool {
	if s.deps.DockerPinger == nil {
		return false
	}
	return s.deps.DockerPinger.Ping(ctx) == nil
}
