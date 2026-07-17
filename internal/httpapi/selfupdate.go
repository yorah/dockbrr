package httpapi

import (
	"net/http"
	"time"
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
