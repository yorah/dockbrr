package httpapi

import (
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
