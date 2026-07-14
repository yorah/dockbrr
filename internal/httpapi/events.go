package httpapi

import (
	"net/http"
	"time"
)

type eventDTO struct {
	ID            int64     `json:"id"`
	Kind          string    `json:"kind"`
	RefJobID      *int64    `json:"ref_job_id"`
	FromDigest    string    `json:"from_digest"`
	ToDigest      string    `json:"to_digest"`
	Message       string    `json:"message"`
	CreatedAt     time.Time `json:"created_at"`
	ChangelogURL  string    `json:"changelog_url"`
	ChangelogText string    `json:"changelog_text"`
}

func (s *Server) handleServiceEvents(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	evs, err := s.deps.Events.ListByService(id)
	if err != nil {
		writeInternalError(w, "service events: list", err)
		return
	}
	out := make([]eventDTO, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventDTO{
			ID: e.ID, Kind: e.Kind, RefJobID: e.RefJobID,
			FromDigest: e.FromDigest, ToDigest: e.ToDigest,
			Message: e.Message, CreatedAt: e.CreatedAt,
			ChangelogURL: e.ChangelogURL, ChangelogText: e.ChangelogText,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
