package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dockbrr/internal/job"
	"dockbrr/internal/store"
)

func TestSSEReplaysHistoryThenStreamsLive(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	jid, _ := store.NewJobs(db).Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	logs := store.NewJobLogs(db)
	_ = logs.Append(jid, "stdout", "history-1")
	_ = logs.Append(jid, "stdout", "history-2")

	// Live channel: one line, then closed → handler returns.
	live := make(chan job.LogLine, 1)
	live <- job.LogLine{Stream: "stdout", Line: "live-1"}
	close(live)
	eng := &fakeEngine{ch: live}
	s.deps = mergeDeps(s.deps, Deps{Jobs: store.NewJobs(db), JobLogs: logs, Engine: eng})

	req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/jobs/%d/logs", jid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.Handler().ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not return after stream close")
	}

	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("missing X-Accel-Buffering: no (got %q)", rec.Header().Get("X-Accel-Buffering"))
	}
	for _, want := range []string{"history-1", "history-2", "live-1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q:\n%s", want, body)
		}
	}
	// History precedes live.
	if strings.Index(body, "history-2") > strings.Index(body, "live-1") {
		t.Fatal("live line emitted before replayed history")
	}
}

func TestSSEUnknownJob404(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, Deps{Jobs: store.NewJobs(db), JobLogs: store.NewJobLogs(db), Engine: &fakeEngine{}})
	req := authReq(httptest.NewRequest(http.MethodGet, "/api/jobs/999/logs", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown job SSE = %d, want 404", rec.Code)
	}
}
