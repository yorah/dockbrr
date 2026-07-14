package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"dockbrr/internal/config"
	"dockbrr/internal/store"
)

func TestListJobsNewestFirstWithRequestedBy(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	deps := mergeDeps(srv.deps, Deps{Jobs: store.NewJobs(db)})
	srv = New(config.Config{}, db, deps)

	for i := 0; i < 3; i++ {
		if _, err := deps.Jobs.Enqueue(store.Job{Type: "apply", Scope: "service", RequestedBy: "user"}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=2", nil)
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
		t.Fatalf("got %d jobs, want 2", len(out))
	}
	if out[0]["requested_by"] != "user" {
		t.Fatalf("requested_by = %v, want user", out[0]["requested_by"])
	}
	if v, ok := out[0]["created_at"].(string); !ok || v == "" {
		t.Fatalf("created_at = %v, want non-empty string", out[0]["created_at"])
	}
	if out[0]["finished_at"] != "" {
		t.Fatalf("finished_at = %v, want empty string for a queued job", out[0]["finished_at"])
	}
	// Newest first: the last-enqueued job (highest id) comes back first.
	id0, _ := out[0]["id"].(float64)
	id1, _ := out[1]["id"].(float64)
	if id0 <= id1 {
		t.Fatalf("ids = [%v,%v], want descending", id0, id1)
	}
}

func TestListJobsEmpty(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv = New(config.Config{}, db, mergeDeps(srv.deps, Deps{Jobs: store.NewJobs(db)}))
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() == "null\n" {
		t.Fatalf("want 200 + [] (not null); code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestClearJobsDeletesFinishedOnly(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	deps := mergeDeps(srv.deps, Deps{Jobs: store.NewJobs(db), Bus: NewBus()})
	srv = New(config.Config{}, db, deps)

	done, err := deps.Jobs.Enqueue(store.Job{Type: "apply", Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Jobs.Finish(done, "success", nil, ""); err != nil {
		t.Fatal(err)
	}
	queued, err := deps.Jobs.Enqueue(store.Job{Type: "apply", Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}

	req := authReq(httptest.NewRequest(http.MethodDelete, "/api/jobs", nil), tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["deleted"] != 1 {
		t.Fatalf("deleted = %d, want 1", out["deleted"])
	}
	if _, err := deps.Jobs.Get(queued); err != nil {
		t.Fatalf("queued job was deleted: %v", err)
	}
}

func TestClearJobsRequiresAuth(t *testing.T) {
	srv, db, _, _ := authedServer(t, Deps{})
	srv = New(config.Config{}, db, mergeDeps(srv.deps, Deps{Jobs: store.NewJobs(db)}))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/jobs", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestClearJobsRequiresCSRF(t *testing.T) {
	srv, db, tok, _ := authedServer(t, Deps{})
	srv = New(config.Config{}, db, mergeDeps(srv.deps, Deps{Jobs: store.NewJobs(db)}))
	req := httptest.NewRequest(http.MethodDelete, "/api/jobs", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok}) // cookie, no CSRF header
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestListJobsRequiresAuth(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}
