package httpapi

import (
	"errors"
	"net/http"

	"dockbrr/internal/store"
)

var (
	errInvalidAction       = errors.New("action must be start, stop, or restart")
	errRemoveNotStandalone = errors.New("remove is only allowed for standalone containers")
	errRemoveNotStopped    = errors.New("remove is only allowed for a stopped container")
)

var lifecycleActions = map[string]bool{"start": true, "stop": true, "restart": true}

// handleLifecycle enqueues a start/stop/restart job for a service.
func (s *Server) handleLifecycle(w http.ResponseWriter, r *http.Request) {
	svc, proj, ok := s.loadServiceProject(w, r)
	if !ok {
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &body); err != nil || !lifecycleActions[body.Action] {
		writeJSONError(w, http.StatusBadRequest, errInvalidAction)
		return
	}
	jobID, err := s.deps.Engine.Enqueue(store.Job{
		Type: body.Action, ServiceID: &svc.ID, ProjectID: &proj.ID, Scope: "service", RequestedBy: "user",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"job_id": jobID})
}

// handleRemove enqueues a remove job for a loose, stopped container. The guard
// is enforced here AND re-checked in the runner (the runner is the source of
// truth; this returns a friendly 409 before enqueue).
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	svc, proj, ok := s.loadServiceProject(w, r)
	if !ok {
		return
	}
	if proj.Kind != "standalone" {
		writeJSONError(w, http.StatusConflict, errRemoveNotStandalone)
		return
	}
	if !store.IsStoppedState(svc.State) {
		writeJSONError(w, http.StatusConflict, errRemoveNotStopped)
		return
	}
	jobID, err := s.deps.Engine.Enqueue(store.Job{
		Type: "remove", ServiceID: &svc.ID, ProjectID: &proj.ID, Scope: "service", RequestedBy: "user",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"job_id": jobID})
}

// loadServiceProject resolves the {id} path param to a service + its project,
// writing a 404 and returning ok=false when either is missing.
func (s *Server) loadServiceProject(w http.ResponseWriter, r *http.Request) (store.Service, store.Project, bool) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return store.Service{}, store.Project{}, false
	}
	svc, err := store.NewServices(s.db).Get(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err)
		return store.Service{}, store.Project{}, false
	}
	proj, err := store.NewProjects(s.db).Get(svc.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err)
		return store.Service{}, store.Project{}, false
	}
	return svc, proj, true
}
