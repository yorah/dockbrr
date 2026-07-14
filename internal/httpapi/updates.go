package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

type updateDTO struct {
	ID            int64  `json:"id"`
	ServiceID     int64  `json:"service_id"`
	FromDigest    string `json:"from_digest"`
	ToDigest      string `json:"to_digest"`
	FromVersion   string `json:"from_version"`
	ToVersion     string `json:"to_version"`
	Tag           string `json:"tag"`
	Severity      string `json:"severity"`
	ChangelogURL  string `json:"changelog_url"`
	ChangelogText string `json:"changelog_text"`
	Status        string `json:"status"`
	DetectedAt    string `json:"detected_at"`
}

func (s *Server) handleListUpdates(w http.ResponseWriter, r *http.Request) {
	ups, err := s.deps.Updates.ListVisible()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]updateDTO, 0, len(ups))
	for _, u := range ups {
		out = append(out, updateDTO{
			ID: u.ID, ServiceID: u.ServiceID, FromDigest: u.FromDigest, ToDigest: u.ToDigest,
			FromVersion: u.FromVersion, ToVersion: u.ToVersion,
			Tag: u.Tag, Severity: u.Severity,
			ChangelogURL: u.ChangelogURL, ChangelogText: u.ChangelogText,
			Status: u.Status, DetectedAt: u.DetectedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListLastApplied serves the newest APPLIED update per service. The
// dashboard uses it as the fallback for its Changelog column: once an update is
// applied it drops out of /api/updates (ListVisible), but its cached changelog
// is still worth reading, so the row falls back to this.
func (s *Server) handleListLastApplied(w http.ResponseWriter, r *http.Request) {
	ups, err := s.deps.Updates.ListLastAppliedByService()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]updateDTO, 0, len(ups))
	for _, u := range ups {
		out = append(out, updateDTO{
			ID: u.ID, ServiceID: u.ServiceID, FromDigest: u.FromDigest, ToDigest: u.ToDigest,
			FromVersion: u.FromVersion, ToVersion: u.ToVersion,
			Tag: u.Tag, Severity: u.Severity,
			ChangelogURL: u.ChangelogURL, ChangelogText: u.ChangelogText,
			Status: u.Status, DetectedAt: u.DetectedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApply resolves update→service→project and ENQUEUES an apply job. It does
// not touch Docker; the Job Engine (Applier) performs the mutation.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
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
	if proj.Unmanaged {
		writeJSONError(w, http.StatusConflict, errors.New("project is unmanaged: compose files not found at their recorded paths"))
		return
	}
	if svc.State == "gone" {
		writeJSONError(w, http.StatusConflict, errors.New("service is gone: its container no longer exists, so it cannot be applied"))
		return
	}
	scope := scopeFromBody(r)
	pid := svc.ProjectID
	jobID, err := s.deps.Engine.Enqueue(store.Job{
		Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: scope, RequestedBy: "user",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": jobID})
}

func (s *Server) handleDismiss(w http.ResponseWriter, r *http.Request) {
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
	if err := s.deps.Updates.SetStatus(id, "dismissed"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = s.deps.Events.Insert(store.Event{
		ServiceID: upd.ServiceID, Kind: "dismissed", ToDigest: upd.ToDigest, Message: "update dismissed",
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}

// handleRestore flips a dismissed update back to available, undoing a prior
// dismiss. Mirrors handleDismiss. Records a "restored" service event.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
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
	if err := s.deps.Updates.SetStatus(id, "available"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = s.deps.Events.Insert(store.Event{
		ServiceID: upd.ServiceID, Kind: "restored", ToDigest: upd.ToDigest, Message: "update restored",
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "available"})
}

// handleCheck triggers read-only detection for one service and waits for it.
// The response means "detection ran", so the client's cache invalidation
// observes the result. Bounded by a server-side timeout so a hung registry
// cannot pin the request forever.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	logger.Infof("check: manual check requested (service %d (%s))", id, s.serviceName(id))
	if err := s.deps.Checker.CheckServiceFresh(ctx, id); err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "checked"})
}

// scopeFromBody reads an optional {"scope":...}; defaults to "service".
func scopeFromBody(r *http.Request) string {
	var body struct {
		Scope string `json:"scope"`
	}
	_ = decodeJSON(r, &body) // optional body; ignore errors
	if body.Scope == "project" {
		return "project"
	}
	return "service"
}
