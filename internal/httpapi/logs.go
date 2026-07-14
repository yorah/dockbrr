package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// handleLogConfig reports the static log config plus the effective level
// (log_level setting, or "info" default). Read-only; for UI display.
func (s *Server) handleLogConfig(w http.ResponseWriter, r *http.Request) {
	level := "info"
	if v, err := s.deps.Settings.Get("log_level"); err == nil && v != "" {
		level = v
	} else if err != nil && !errors.Is(err, store.ErrSettingNotFound) {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       s.deps.LogConfig.Path,
		"level":      level,
		"maxSizeMB":  s.deps.LogConfig.MaxSizeMB,
		"maxBackups": s.deps.LogConfig.MaxBackups,
	})
}

// handleLogFiles lists the log directory (newest first).
func (s *Server) handleLogFiles(w http.ResponseWriter, r *http.Request) {
	files, err := logger.Files()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

// handleLogDownload streams a single log file as an attachment. logger.Open is
// the path-traversal guard: base-name only, resolving inside the log dir.
func (s *Server) handleLogDownload(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	rc, err := logger.Open(name)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	io.Copy(w, rc)
}
