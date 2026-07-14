package httpapi

import (
	"errors"
	"net/http"

	"dockbrr/internal/compose"
	"dockbrr/internal/store"
)

// handlePreview renders the exact `docker compose pull` + `up` commands the
// Applier would run for an update at the given scope, display only. It reuses
// the same compose spec builders the Applier uses (single source of truth) and
// touches no Docker.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	upd, err := s.deps.Updates.Get(id)
	if err != nil {
		if errors.Is(err, store.ErrUpdateNotFound) {
			writeJSONError(w, http.StatusNotFound, err)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	svc, err := s.deps.Services.Get(upd.ServiceID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	proj, err := s.deps.Projects.Get(svc.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	scope := "service"
	if r.URL.Query().Get("scope") == "project" {
		scope = "project"
	}
	pull, err := compose.Preview(compose.PullSpec(proj.ConfigFiles, proj.WorkingDir, proj.Name, scope, svc.Name))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	up, err := compose.Preview(compose.UpSpec(proj.ConfigFiles, proj.WorkingDir, proj.Name, scope, svc.Name))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pull": pull, "up": up})
}
