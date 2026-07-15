package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func TestProjectsEndpoint(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})

	projs := store.NewProjects(db)
	svcs := store.NewServices(db)

	// Compose project: one service with no auto_update override (nil → null).
	composeID, err := projs.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web",
		WorkingDir: "/srv/web",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svcs.Upsert(store.Service{
		ProjectID: composeID, Name: "app", ImageRef: "nginx:1.25",
		CurrentDigest: "sha256:aaa", State: "running",
		Healthcheck: true, AutoUpdateEnabled: nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Standalone project: one service with auto_update override set to true.
	trueVal := true
	standaloneID, err := projs.Upsert(store.Project{
		HostID: 1, Kind: "standalone", Name: "db",
		WorkingDir: "/srv/db",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svcs.Upsert(store.Service{
		ProjectID: standaloneID, Name: "postgres", ImageRef: "postgres:15",
		CurrentDigest: "sha256:bbb", State: "running",
		AutoUpdateEnabled: &trueVal,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, "/api/projects", nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var body []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(body) != 2 {
		t.Fatalf("got %d projects, want 2", len(body))
	}

	// Projects are ordered by name: "db" then "web".
	byName := map[string]map[string]json.RawMessage{}
	for _, p := range body {
		var name string
		if err := json.Unmarshal(p["name"], &name); err != nil {
			t.Fatal(err)
		}
		byName[name] = p
	}

	// --- compose / web project ---
	web, ok := byName["web"]
	if !ok {
		t.Fatal("missing web project")
	}
	var webKind string
	if err := json.Unmarshal(web["kind"], &webKind); err != nil || webKind != "compose" {
		t.Fatalf("web kind = %s, want compose", web["kind"])
	}

	var webSvcs []map[string]json.RawMessage
	if err := json.Unmarshal(web["services"], &webSvcs); err != nil || len(webSvcs) != 1 {
		t.Fatalf("web services = %s, want 1-element array", web["services"])
	}
	webSvc := webSvcs[0]

	// auto_update_enabled must be JSON null (nil *bool marshals to null).
	if string(webSvc["auto_update_enabled"]) != "null" {
		t.Fatalf("web/app auto_update_enabled = %s, want null", webSvc["auto_update_enabled"])
	}
	var webSvcName string
	if err := json.Unmarshal(webSvc["name"], &webSvcName); err != nil || webSvcName != "app" {
		t.Fatalf("web svc name = %s, want app", webSvc["name"])
	}

	// --- standalone / db project ---
	dbProj, ok := byName["db"]
	if !ok {
		t.Fatal("missing db project")
	}
	var dbKind string
	if err := json.Unmarshal(dbProj["kind"], &dbKind); err != nil || dbKind != "standalone" {
		t.Fatalf("db kind = %s, want standalone", dbProj["kind"])
	}

	var dbSvcs []map[string]json.RawMessage
	if err := json.Unmarshal(dbProj["services"], &dbSvcs); err != nil || len(dbSvcs) != 1 {
		t.Fatalf("db services = %s, want 1-element array", dbProj["services"])
	}
	dbSvc := dbSvcs[0]
	if string(dbSvc["auto_update_enabled"]) != "true" {
		t.Fatalf("db/postgres auto_update_enabled = %s, want true", dbSvc["auto_update_enabled"])
	}
}

func TestProjectsEmptyDB(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, "/api/projects", nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	raw := rec.Body.Bytes()
	if string(raw) == "null\n" || string(raw) == "null" {
		t.Fatal("empty projects returned null, want []")
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, raw)
	}
	if len(arr) != 0 {
		t.Fatalf("got %d items, want 0", len(arr))
	}
}

func TestProjectComposeEndpoint(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	projs := srv.deps.Projects

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	content := "services:\n  web:\n    image: nginx:1.25\n"
	if err := os.WriteFile(composePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(dir, "missing.yml")

	pid, err := projs.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web", WorkingDir: dir,
		ConfigFiles: []string{composePath, missingPath}, Source: "discovered",
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := authedGet(t, srv, pathf("/api/projects/%d/compose", pid), tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Error   string `json:"error"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(body.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(body.Files))
	}

	got := body.Files[0]
	if got.Path != composePath {
		t.Fatalf("path = %q, want %q", got.Path, composePath)
	}
	if got.Content != content {
		t.Fatalf("content = %q, want %q", got.Content, content)
	}
	if got.Error != "" {
		t.Fatalf("error = %q, want empty for a readable file", got.Error)
	}

	gotMissing := body.Files[1]
	if gotMissing.Path != missingPath {
		t.Fatalf("path = %q, want %q", gotMissing.Path, missingPath)
	}
	if gotMissing.Error == "" {
		t.Fatal("error = \"\", want non-empty for an unreadable file")
	}
	if gotMissing.Content != "" {
		t.Fatalf("content = %q, want empty for an unreadable file", gotMissing.Content)
	}
}

func TestProjectComposeUnauthenticatedThroughRouter(t *testing.T) {
	srv, db, _, _ := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	pid, err := srv.deps.Projects.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web", WorkingDir: "/srv/web",
		ConfigFiles: []string{"/srv/web/docker-compose.yml"}, Source: "discovered",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, pathf("/api/projects/%d/compose", pid), nil) // no cookie
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (requireAuth wired on /api/projects/{id}/compose)", rec.Code)
	}
}

func TestProjectsUnauthenticatedThroughRouter(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil) // no cookie
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (requireAuth wired on /api/projects)", rec.Code)
	}
}

func TestProjectsCarryCheckStatus(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	remoteStates := store.NewRemoteStates(db)
	srv.deps = mergeDeps(srv.deps, Deps{RemoteStates: remoteStates})

	// seedProjectAndService (updates_test.go) seeds ImageRef "ghcr.io/x/web:1.0".
	svcID := seedProjectAndService(t, srv)

	now := time.Now().UTC()
	if err := remoteStates.Upsert(store.RemoteState{
		Repo: "ghcr.io/x/web", Tag: "1.0", Status: "rate_limited", ResolvedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	rec := authedGet(t, srv, "/api/projects", tok, csrf)
	var body []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(body) != 1 {
		t.Fatalf("got %d projects, want 1", len(body))
	}
	var svcs []map[string]json.RawMessage
	if err := json.Unmarshal(body[0]["services"], &svcs); err != nil || len(svcs) != 1 {
		t.Fatalf("services = %s, want 1-element array", body[0]["services"])
	}
	svc := svcs[0]

	var gotID int64
	if err := json.Unmarshal(svc["id"], &gotID); err != nil || gotID != svcID {
		t.Fatalf("service id = %s, want %d", svc["id"], svcID)
	}

	var checkStatus string
	if err := json.Unmarshal(svc["check_status"], &checkStatus); err != nil {
		t.Fatalf("check_status: %v; svc=%v", err, svc)
	}
	if checkStatus != "rate_limited" {
		t.Fatalf("check_status = %q, want rate_limited", checkStatus)
	}

	var lastChecked string
	if err := json.Unmarshal(svc["last_checked"], &lastChecked); err != nil {
		t.Fatalf("last_checked: %v; svc=%v", err, svc)
	}
	if lastChecked == "" {
		t.Fatal("last_checked = \"\", want non-empty RFC3339 timestamp")
	}
	if _, err := time.Parse(time.RFC3339, lastChecked); err != nil {
		t.Fatalf("last_checked not RFC3339 (%q): %v", lastChecked, err)
	}
}

func TestProjectsCheckStatusEmptyWhenNoRemoteState(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, updatesDeps(db))
	srv.deps = mergeDeps(srv.deps, Deps{RemoteStates: store.NewRemoteStates(db)})
	seedProjectAndService(t, srv)

	rec := authedGet(t, srv, "/api/projects", tok, csrf)
	var body []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var svcs []map[string]json.RawMessage
	if err := json.Unmarshal(body[0]["services"], &svcs); err != nil || len(svcs) != 1 {
		t.Fatalf("services = %s, want 1-element array", body[0]["services"])
	}
	svc := svcs[0]

	var checkStatus, lastChecked string
	if err := json.Unmarshal(svc["check_status"], &checkStatus); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(svc["last_checked"], &lastChecked); err != nil {
		t.Fatal(err)
	}
	if checkStatus != "" || lastChecked != "" {
		t.Fatalf("check_status=%q last_checked=%q, want both empty", checkStatus, lastChecked)
	}
}

func TestProjectsEndpointIncludesAutoNamed(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})

	projs := store.NewProjects(db)
	id, err := projs.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha", WorkingDir: ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := projs.SetAutoNamed(id, true); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, "/api/projects", nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var out []struct {
		Name      string `json:"name"`
		AutoNamed bool   `json:"auto_named"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Name != "adoring_saha" || !out[0].AutoNamed {
		t.Fatalf("projects payload = %+v, want one adoring_saha with auto_named=true", out)
	}
}
