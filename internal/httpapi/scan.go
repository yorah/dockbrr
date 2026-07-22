package httpapi

import (
	"errors"
	"net/http"

	"dockbrr/internal/logger"
)

// handleScanAll starts a background scan-run. With no body it sweeps every
// service (scope "all", which stamps last_check_all + publishes "scanned").
// With {"project_id": N} it sweeps that project only. Returns 202 immediately;
// progress streams over SSE. A scan already in flight returns 409.
func (s *Server) handleScanAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID int64 `json:"project_id"`
	}
	_ = decodeJSON(r, &body) // optional

	scope, projectID := "all", int64(0)
	if body.ProjectID > 0 {
		scope, projectID = "project", body.ProjectID
	}
	logger.Infof("scan: manual check-all requested (scope=%s project=%d)", scope, projectID)

	st, err := s.scan.Start(scope, projectID, 0)
	if errors.Is(err, ErrScanBusy) {
		writeJSONError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// handleScanStatus returns the authoritative scan-run snapshot so a freshly
// mounted (or reconnected) client can seed/resync its progress state.
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scan.Snapshot())
}

// handleScanAbort cancels the in-flight scan-run. Idempotent: 204 whether or
// not a scan was running. Read-only in effect (detection never mutates Docker),
// but a non-GET call so it carries the CSRF header like other mutations.
func (s *Server) handleScanAbort(w http.ResponseWriter, r *http.Request) {
	s.scan.Abort()
	w.WriteHeader(http.StatusNoContent)
}
