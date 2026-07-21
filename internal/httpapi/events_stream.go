package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// heartbeatInterval is how often an idle event stream emits an SSE comment to
// keep the connection alive through proxies/load-balancers that reap idle
// sockets. A var (not const) so tests can shorten it.
var heartbeatInterval = 25 * time.Second

// Event is a dashboard-refresh hint pushed to SSE subscribers. It carries no
// payload beyond identifiers: the client refetches the authoritative queries on
// receipt (events are hints, queries are the source of truth).
type Event struct {
	Type      string `json:"type"` // detected|job_finished|reconciled|scanned|scan_progress|scan_finished
	ServiceID int64  `json:"service_id,omitempty"`
	JobID     int64  `json:"job_id,omitempty"`
	// Done/Total carry scan-run progress. Exception to the payload-free hint
	// rule: progress is ephemeral with no query to refetch. The authoritative
	// GET /api/scan snapshot self-heals dropped events on mount/reconnect.
	Done  int `json:"done,omitempty"`
	Total int `json:"total,omitempty"`
}

// Bus fans events out to SSE subscribers. Publish is non-blocking: a slow
// subscriber's full buffer drops the event (the client refetches on the next
// one; queries are the source of truth, events are only refresh hints).
type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

// Publish fans an event out to every subscriber without blocking. A subscriber
// whose buffer is full simply drops this event.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe registers a buffered subscriber and returns its receive channel plus
// a cancel func that unregisters it. The buffer (16) absorbs bursts; overflow is
// dropped by Publish.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// handleEventStream streams dashboard-refresh hints over SSE (cookie-authed GET,
// same pattern as handleJobLogs (GET so no CSRF). Each frame is a JSON Event.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if s.deps.Bus == nil {
		writeJSONError(w, http.StatusServiceUnavailable, fmt.Errorf("event bus not configured"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.deps.Bus.Subscribe()
	defer cancel()
	ctx := r.Context()
	beat := time.NewTicker(heartbeatInterval)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			// SSE comment line: keeps the socket warm, ignored by EventSource.
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case e := <-ch:
			payload, _ := json.Marshal(e)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
