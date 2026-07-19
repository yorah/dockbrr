package httpapi

import (
	"errors"
	"net/http"
	"time"

	"dockbrr/internal/store"
)

// handleSelfUpdate reports whether a newer dockbrr release is available. It is
// best-effort: a nil checker or a swallowed GitHub error yields a valid body
// with update_available:false, never a 5xx.
func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if s.deps.SelfUpdate == nil {
		writeJSON(w, http.StatusOK, map[string]any{"update_available": false})
		return
	}
	res, _ := s.deps.SelfUpdate.Check(r.Context()) // soft error: res is still a valid verdict
	out := map[string]any{
		"current":          res.Current,
		"latest":           res.Latest,
		"html_url":         res.HTMLURL,
		"update_available": res.UpdateAvailable,
	}
	if !res.CheckedAt.IsZero() {
		out["checked_at"] = res.CheckedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSelfUpdateApply enqueues a self_update job that pulls the latest
// dockbrr image and hands the container swap to a detached helper.
// Preconditions (not running in a container, a checker error, or no update
// available) return 409 and enqueue nothing: a doomed job never starts. A
// checker error gets its own message, distinct from a genuine no-update
// verdict, so a transient GitHub outage doesn't read as "you're up to date".
// Single-flight: if a self_update is already queued or running, the existing
// job's id is returned (200) instead of enqueuing a second one.
func (s *Server) handleSelfUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.deps.SelfID == "" {
		writeJSONError(w, http.StatusConflict, errors.New("self-update is only available when dockbrr runs in a container"))
		return
	}
	if s.deps.SelfUpdate == nil {
		writeJSONError(w, http.StatusConflict, errors.New("self-update is unavailable"))
		return
	}
	res, err := s.deps.SelfUpdate.Check(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusConflict, errors.New("could not check for updates, try again later"))
		return
	}
	if !res.UpdateAvailable {
		writeJSONError(w, http.StatusConflict, errors.New("no dockbrr update is available"))
		return
	}
	if s.deps.Jobs != nil {
		if active, ok, err := s.deps.Jobs.ActiveByType("self_update"); err != nil {
			writeInternalError(w, "check active self_update", err)
			return
		} else if ok {
			// Single-flight: a self_update is already queued or running. Return its
			// id idempotently rather than stacking a second job on top of it.
			writeJSON(w, http.StatusOK, map[string]any{"job_id": active.ID})
			return
		}
	}
	id, err := s.deps.Engine.Enqueue(store.Job{Type: "self_update", RequestedBy: "user"})
	if err != nil {
		writeInternalError(w, "enqueue self_update", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": id})
}
