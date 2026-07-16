package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dockbrr/internal/store"
)

func TestLifecycleEndpointEnqueues(t *testing.T) {
	eng := &fakeEngine{} // implements Enqueue, records the last job; reuse the existing test fake
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "app", WorkingDir: "/srv"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "web", ImageRef: "nginx:1", State: "running"})

	body := strings.NewReader(`{"action":"restart"}`)
	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/lifecycle", sid), body), tok, csrf)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		JobID int64 `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.JobID == 0 {
		t.Fatalf("body = %s, want a job_id", rec.Body.String())
	}
	last := eng.enqueued[len(eng.enqueued)-1]
	if last.Type != "restart" || last.ServiceID == nil || *last.ServiceID != sid {
		t.Fatalf("enqueued job = %+v, want restart for service %d", last, sid)
	}
}

func TestLifecycleEndpointRejectsBadAction(t *testing.T) {
	eng := &fakeEngine{}
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "app", WorkingDir: "/srv"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "web", ImageRef: "nginx:1", State: "running"})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/lifecycle", sid), strings.NewReader(`{"action":"nuke"}`)), tok, csrf)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for bad action", rec.Code)
	}
}

func TestRemoveEndpointGuardsLooseStopped(t *testing.T) {
	eng := &fakeEngine{}
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	// Running standalone: must be rejected (409).
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "running"})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/remove", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a running container", rec.Code)
	}
	for _, j := range eng.enqueued {
		if j.Type == "remove" {
			t.Fatal("remove job must not be enqueued for a running container")
		}
	}
}

func TestRemoveEndpointEnqueuesForLooseStopped(t *testing.T) {
	eng := &fakeEngine{}
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "exited"})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/remove", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	last := eng.enqueued[len(eng.enqueued)-1]
	if last.Type != "remove" || last.ServiceID == nil || *last.ServiceID != sid {
		t.Fatalf("enqueued job = %+v, want remove for service %d", last, sid)
	}
}

func TestRemoveEndpointRejectsComposeProject(t *testing.T) {
	eng := &fakeEngine{}
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "app", WorkingDir: "/srv"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "web", ImageRef: "nginx:1", State: "exited"})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/remove", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a compose-managed service", rec.Code)
	}
}

type fakeLogs struct{ out string }

func (f fakeLogs) ContainerLogsTail(_ context.Context, _ string, _ int) (string, error) {
	return f.out, nil
}

func TestLogsEndpointReturnsTail(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{DockerLogs: fakeLogs{out: "line1\nline2\n"}})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "exited", ContainerIDs: []string{"c1"}})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/services/%d/logs?tail=100", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Logs != "line1\nline2\n" {
		t.Fatalf("logs = %q, want the fake tail", out.Logs)
	}
}

func TestLogsEndpointNilDepsReturns503(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "exited", ContainerIDs: []string{"c1"}})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/services/%d/logs", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when DockerLogs is nil", rec.Code)
	}
}

func TestLogsEndpointEmptyContainerIDsReturns200Empty(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{DockerLogs: fakeLogs{out: "should not be returned"}})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "exited"})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/services/%d/logs", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for empty container ids", rec.Code)
	}
	var out struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Logs != "" {
		t.Fatalf("logs = %q, want empty", out.Logs)
	}
}

func TestLogsEndpointRejectsBadTail(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{DockerLogs: fakeLogs{out: "x"}})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "exited", ContainerIDs: []string{"c1"}})

	for _, tail := range []string{"0", "2001", "abc", "-1"} {
		rec := httptest.NewRecorder()
		req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/services/%d/logs?tail=%s", sid, tail), nil), tok, csrf)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("tail=%s: status = %d, want 400", tail, rec.Code)
		}
	}
}
