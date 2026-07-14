package httpapi

import (
	"context"
	"net/http"
	"time"

	"dockbrr/internal/logger"
)

// handleScanAll runs a fresh detection sweep over every service, then stamps
// last_check_all and publishes a "scanned" refresh hint, the same two side
// effects the scheduler performs on its own tick (cmd/dockbrr/main.go
// schedulerLoop). Without this, a manual "Check all" only fanned out
// per-service checks and never touched last_check_all, so the dashboard's
// "Last scan" tile never reflected a manual sweep.
func (s *Server) handleScanAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	logger.Infof("scan: manual check-all requested")
	if err := s.deps.Checker.CheckAll(ctx); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.deps.Settings.Set("last_check_all", now); err != nil {
		logger.Errorf("scan: record last_check_all: %v", err)
	}
	if s.deps.Bus != nil {
		s.deps.Bus.Publish(Event{Type: "scanned"})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "checked", "last_check_all": now})
}
