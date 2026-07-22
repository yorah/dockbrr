package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"dockbrr/internal/selfupdate"
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
	// ?force=true (or 1) bypasses the cache TTL for the manual "Check for
	// updates" action; the default poll keeps serving the cached verdict.
	if force := r.URL.Query().Get("force"); force == "true" || force == "1" {
		// Manual check: a GitHub failure is reported, not masked. Returning a
		// stale cache here would render identically to a fresh "up to date"
		// verdict, so the user could never tell the check silently failed.
		res, err := s.deps.SelfUpdate.CheckFresh(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, errors.New("could not check for updates, try again later"))
			return
		}
		writeSelfUpdate(w, res)
		return
	}
	res, _ := s.deps.SelfUpdate.Check(r.Context()) // background poll: soft error, serve last-known
	writeSelfUpdate(w, res)
}

// writeSelfUpdate renders a self-update verdict as the endpoint's JSON body.
// checked_at is omitted when zero so a never-checked verdict carries no
// timestamp (the frontend keys "have we checked yet?" off its presence).
func writeSelfUpdate(w http.ResponseWriter, res selfupdate.Result) {
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
	id, status, err := s.enqueueSelfUpdate(r.Context())
	if err != nil {
		if status == http.StatusInternalServerError {
			writeInternalError(w, "enqueue self_update", err)
		} else {
			writeJSONError(w, status, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": id})
}

// serviceIsSelf reports whether svc is dockbrr's own container. False on a
// host install (SelfID == ""). Ids may be stored 12- or 64-hex while SelfID
// may be 64-hex, so prefix-match both directions (same rule as
// job.Dispatcher.targetsSelf).
func (s *Server) serviceIsSelf(svc store.Service) bool {
	self := s.deps.SelfID
	if self == "" {
		return false
	}
	for _, cid := range svc.ContainerIDs {
		if strings.HasPrefix(cid, self) || strings.HasPrefix(self, cid) {
			return true
		}
	}
	return false
}

// enqueueSelfUpdate runs the self_update preconditions and enqueues the job,
// shared by the manual endpoint and the Apply-on-self route. status == 0 means
// success (jobID is a new or an already-active single-flight job); a non-zero
// status is the HTTP code to write alongside err. A StatusInternalServerError
// status signals the caller to use writeInternalError.
func (s *Server) enqueueSelfUpdate(ctx context.Context) (int64, int, error) {
	if s.deps.SelfID == "" {
		return 0, http.StatusConflict, errors.New("self-update is only available when dockbrr runs in a container")
	}
	if s.deps.SelfUpdate == nil {
		return 0, http.StatusConflict, errors.New("self-update is unavailable")
	}
	res, err := s.deps.SelfUpdate.Check(ctx)
	if err != nil {
		return 0, http.StatusConflict, errors.New("could not check for updates, try again later")
	}
	if !res.UpdateAvailable {
		return 0, http.StatusConflict, errors.New("no dockbrr update is available")
	}
	if s.deps.Jobs != nil {
		if active, ok, err := s.deps.Jobs.ActiveByType("self_update"); err != nil {
			return 0, http.StatusInternalServerError, err
		} else if ok {
			// Single-flight: a self_update is already queued or running. Return its
			// id idempotently rather than stacking a second job on top of it.
			return active.ID, 0, nil
		}
	}
	id, err := s.deps.Engine.Enqueue(store.Job{Type: "self_update", RequestedBy: "user"})
	if err != nil {
		return 0, http.StatusInternalServerError, err
	}
	return id, 0, nil
}
