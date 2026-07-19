package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"dockbrr/internal/store"
)

// handleJobLogs streams a job's logs over Server-Sent Events: it first replays
// the already-persisted lines (JobLogs.ListByJob) then forwards live lines from
// the engine's Stream until the stream closes or the client disconnects. The
// engine's Stream delivers LIVE lines only, hence the replay prefix.
//
// Boundary note: a line emitted between the history snapshot and the live
// subscription is a rare, acceptable gap for a log view (Phase 7 may dedupe by a
// future line id). Replay-then-live matches the Phase-6 contract.
func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.deps.Jobs.Get(id); err != nil {
		if errors.Is(err, store.ErrJobNotFound) {
			writeJSONError(w, http.StatusNotFound, err)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering
	w.WriteHeader(http.StatusOK)

	send := func(stream, line string) {
		payload, _ := json.Marshal(map[string]string{"stream": stream, "line": line})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	// 1) Replay already-emitted lines.
	history, err := s.deps.JobLogs.ListByJob(id)
	if err == nil {
		for _, lg := range history {
			send(lg.Stream, lg.Line)
		}
	}

	// 2) Stream live lines until the channel closes or the client goes away.
	ch, err := s.deps.Engine.Stream(id)
	if err != nil {
		send("system", "log stream unavailable: "+err.Error())
		return
	}
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-ch:
			if !ok {
				return // job finished; stream closed
			}
			send(line.Stream, line.Line)
		}
	}
}
