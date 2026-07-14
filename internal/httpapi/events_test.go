package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"dockbrr/internal/config"
	"dockbrr/internal/store"
)

func TestServiceEvents(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	deps := mergeDeps(srv.deps, Deps{
		Projects: store.NewProjects(db),
		Services: store.NewServices(db),
		Events:   store.NewEvents(db),
	})
	srv = New(config.Config{}, db, deps)

	// Create a project and service to attach events to.
	pid, err := deps.Projects.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "test", WorkingDir: "/test",
		ConfigFiles: []string{"docker-compose.yml"}, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := deps.Services.Upsert(store.Service{
		ProjectID: pid, Name: "web", ImageRef: "nginx:latest",
		CurrentDigest: "sha256:aaa", State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = deps.Events.Insert(store.Event{ServiceID: sid, Kind: "detected", ToDigest: "sha256:bbb", Message: "update available"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = deps.Events.Insert(store.Event{ServiceID: sid, Kind: "succeeded", ToDigest: "sha256:bbb", Message: "applied"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, pathf("/api/services/%d/events", sid), nil)
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d events, want 2", len(out))
	}
	// Events are ordered DESC (newest first), so "succeeded" is first.
	if out[0]["kind"] != "succeeded" {
		t.Fatalf("first kind = %v, want succeeded", out[0]["kind"])
	}
	if out[1]["kind"] != "detected" {
		t.Fatalf("second kind = %v, want detected", out[1]["kind"])
	}
}

func TestServiceEventsMalformedID(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv = New(config.Config{}, db, mergeDeps(srv.deps, Deps{Events: store.NewEvents(db)}))
	req := httptest.NewRequest(http.MethodGet, "/api/services/not-a-number/events", nil)
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 for malformed id; body=%s", rec.Code, rec.Body.String())
	}
}

func TestServiceEventsEmpty(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv = New(config.Config{}, db, mergeDeps(srv.deps, Deps{Events: store.NewEvents(db)}))
	req := httptest.NewRequest(http.MethodGet, "/api/services/9999/events", nil)
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() == "null\n" {
		t.Fatalf("want 200 + [] (not null); code=%d body=%q", rec.Code, rec.Body.String())
	}
}
