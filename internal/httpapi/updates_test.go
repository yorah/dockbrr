package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dockbrr/internal/store"
)

// updatesDeps creates Projects/Services/Updates repos over db (follows previewDeps pattern).
func updatesDeps(db *store.DB) Deps {
	return Deps{
		Projects: store.NewProjects(db),
		Services: store.NewServices(db),
		Updates:  store.NewUpdates(db),
	}
}

// seedProjectAndService creates a project and service, returning the service ID.
func seedProjectAndService(t *testing.T, s *Server) int64 {
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
	return sid
}

// authedGet performs an authenticated GET request to path and returns the response recorder.
func authedGet(t *testing.T, s *Server, path string, tok, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, path, nil), tok, csrf)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestListUpdatesIncludesChangelogAndVersions(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	svcID := seedProjectAndService(t, srv)

	id, err := srv.deps.Updates.Upsert(store.Update{
		ServiceID: svcID, FromDigest: "sha256:aaa", ToDigest: "sha256:bbb",
		FromVersion: "1.2.3", ToVersion: "1.3.0", Tag: "1.3.0", Severity: "minor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.deps.Updates.SetChangelog(id, "https://example.com/rel", "## Notes\n- fixed"); err != nil {
		t.Fatal(err)
	}

	rec := authedGet(t, srv, "/api/updates", tok, csrf)
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 update, got %d", len(out))
	}
	u := out[0]
	if u["from_version"] != "1.2.3" || u["to_version"] != "1.3.0" {
		t.Errorf("versions missing: %v", u)
	}
	if u["changelog_text"] != "## Notes\n- fixed" {
		t.Errorf("changelog_text missing: %v", u)
	}
	if s, _ := u["detected_at"].(string); s == "" {
		t.Errorf("detected_at missing: %v", u)
	} else if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("detected_at not RFC3339 (%q): %v", s, err)
	}
}

func TestListUpdatesCarriesChangelogStatus(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	svcID := seedProjectAndService(t, srv)

	id, err := srv.deps.Updates.Upsert(store.Update{
		ServiceID: svcID, FromDigest: "sha256:aaa", ToDigest: "sha256:bbb",
		Tag: "1.3.0", Severity: "minor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.deps.Updates.SetChangelogStatus(id, "rate_limited"); err != nil {
		t.Fatal(err)
	}

	rec := authedGet(t, srv, "/api/updates", tok, csrf)
	var got []updateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChangelogStatus != "rate_limited" {
		t.Fatalf("dto = %+v, want changelog_status=rate_limited", got)
	}
}

func TestListLastAppliedUpdates(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	sid := seedProjectAndService(t, srv)

	if _, err := srv.deps.Updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "1.0", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.deps.Updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:b", ToDigest: "sha256:c",
		Tag: "1.1", Severity: "minor", Status: "available",
	}); err != nil {
		t.Fatal(err)
	}

	rec := authedGet(t, srv, "/api/updates/last-applied", tok, csrf)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got []updateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].Status != "applied" || got[0].ChangelogText != "# 1.0" || got[0].ServiceID != sid {
		t.Fatalf("unexpected dto: %+v", got[0])
	}
	if got[0].DetectedAt == "" {
		t.Fatalf("detected_at empty: %+v", got[0])
	}
}
