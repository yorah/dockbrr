package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

type jobDTO struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Scope       string `json:"scope"`
	ExitCode    *int   `json:"exit_code"`
	Error       string `json:"error"`
	ProjectID   *int64 `json:"project_id"`
	ServiceID   *int64 `json:"service_id"`
	RequestedBy string `json:"requested_by"`
	CreatedAt   string `json:"created_at"`
	FinishedAt  string `json:"finished_at"`
	// Resolved display names, list endpoint only ("" on the single-job
	// endpoint and for since-deleted services/projects).
	ProjectName string `json:"project_name,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
}

// toJobDTO converts a store.Job into the wire shape shared by the single-job
// and list endpoints. Timestamps render as RFC3339, or "" when unset
// (FinishedAt is nil until the job reaches a terminal state).
func toJobDTO(j store.Job) jobDTO {
	finished := ""
	if j.FinishedAt != nil {
		finished = j.FinishedAt.Format(time.RFC3339)
	}
	return jobDTO{
		ID: j.ID, Type: j.Type, Status: j.Status, Scope: j.Scope, ExitCode: j.ExitCode, Error: j.Error,
		ProjectID: j.ProjectID, ServiceID: j.ServiceID, RequestedBy: j.RequestedBy,
		CreatedAt: j.CreatedAt.Format(time.RFC3339), FinishedAt: finished,
	}
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	j, err := s.deps.Jobs.Get(id)
	if err != nil {
		if errors.Is(err, store.ErrJobNotFound) {
			writeJSONError(w, http.StatusNotFound, err)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobDTO(j))
}

// handleListJobs returns the most recent jobs, newest first, for the Jobs
// history screen. Read-only: it only queries the store, never touches Docker.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.deps.Jobs.List(limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]jobDTO, 0, len(jobs))
	for _, j := range jobs {
		dto := toJobDTO(j.Job)
		dto.ProjectName = j.ProjectName
		dto.ServiceName = j.ServiceName
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleClearJobs purges the job history: every job in a terminal state, plus
// its logs (ON DELETE CASCADE). Queued and running jobs are kept: the worker
// still owns them. Snapshots survive with a NULL job_id, so rollback (which
// resolves the latest snapshot by service) is unaffected. This only writes to
// the store; no Docker call, so it does not go through the Job Engine.
func (s *Server) handleClearJobs(w http.ResponseWriter, r *http.Request) {
	n, err := s.deps.Jobs.DeleteFinished()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	logger.Infof("jobs: cleared %d finished job(s)", n)
	if s.deps.Bus != nil {
		s.deps.Bus.Publish(Event{Type: "jobs_cleared"})
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

// handleRollback resolves the original job's service+project and ENQUEUES a
// rollback job. No Docker call here; the Applier restores from the snapshot.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	orig, err := s.deps.Jobs.Get(id)
	if err != nil {
		if errors.Is(err, store.ErrJobNotFound) {
			writeJSONError(w, http.StatusNotFound, err)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	if orig.ServiceID == nil {
		writeJSONError(w, http.StatusBadRequest, errors.New("original job has no service to roll back"))
		return
	}
	jobID, err := s.deps.Engine.Enqueue(store.Job{
		Type: "rollback", ServiceID: orig.ServiceID, ProjectID: orig.ProjectID,
		Scope: orig.Scope, RequestedBy: "user",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": jobID})
}
