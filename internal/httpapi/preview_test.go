package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dockbrr/internal/store"
)

// previewDeps builds Projects/Services/Updates repos over db, mirroring
// settingsDeps' pattern: re-wire deps onto the SAME db authedServer created,
// so the seeded admin/session (also on that db) remain valid for authReq.
func previewDeps(db *store.DB) Deps {
	return Deps{
		Projects: store.NewProjects(db),
		Services: store.NewServices(db),
		Updates:  store.NewUpdates(db),
	}
}

func seedUpdateForPreview(t *testing.T, s *Server) (updateID int64) {
	t.Helper()
	pid, err := s.deps.Projects.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "app", WorkingDir: "/srv/app",
		ConfigFiles: []string{"docker-compose.yml"}, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := s.deps.Services.Upsert(store.Service{
		ProjectID: pid, Name: "web", ImageRef: "ghcr.io/x/web:1.0",
		CurrentDigest: "sha256:aaa", State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := s.deps.Updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:aaa", ToDigest: "sha256:bbb",
		Tag: "1.1", Severity: "minor", Status: "available",
	})
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

func TestPreviewServiceScope(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, previewDeps(db))
	uid := seedUpdateForPreview(t, s)

	req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/updates/%d/preview?scope=service", uid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct{ Pull, Up string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Pull, "compose") || !strings.HasSuffix(strings.TrimSpace(out.Pull), "web") {
		t.Fatalf("pull = %q, want it to target service web", out.Pull)
	}
	// The preview must carry the compose project name (-p) so it reuses the
	// original project namespace. The seeded project is named "app".
	if !strings.Contains(out.Pull, "-p app") {
		t.Fatalf("pull = %q, want -p app (project namespace)", out.Pull)
	}
	if !strings.Contains(out.Up, "-p app") {
		t.Fatalf("up = %q, want -p app (project namespace)", out.Up)
	}
	if !strings.Contains(out.Up, "--no-deps web") {
		t.Fatalf("up = %q, want --no-deps web", out.Up)
	}
}

func TestPreviewProjectScope(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, previewDeps(db))
	uid := seedUpdateForPreview(t, s)

	req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/updates/%d/preview?scope=project", uid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct{ Pull, Up string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Up, "--no-deps") {
		t.Fatalf("project-scope up must not use --no-deps: %q", out.Up)
	}
}

func TestPreviewUnknownUpdate404(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, previewDeps(db))

	req := authReq(httptest.NewRequest(http.MethodGet, "/api/updates/9999/preview", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}
