package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dockbrr/internal/secret"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

func selfUpdateDeps(t *testing.T, db *store.DB, apiBase, current string) Deps {
	t.Helper()
	key, _ := secret.LoadOrCreateKey(t.TempDir())
	sealer, _ := secret.NewSealer(key)
	settings := store.NewSettings(db, sealer)
	return Deps{
		Sealer:     sealer,
		Settings:   settings,
		SelfUpdate: selfupdate.NewChecker(http.DefaultClient, settings, current, apiBase, time.Hour, nil),
	}
}

func TestSelfUpdateEndpoint(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.0.0","html_url":"https://github.com/yorah/dockbrr/releases/tag/v9.0.0"}`))
	}))
	t.Cleanup(gh.Close)

	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, selfUpdateDeps(t, db, gh.URL, "0.4.2"))

	rec := authedGet(t, srv, "/api/updates/self", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["update_available"] != true {
		t.Errorf("update_available: %v", out)
	}
	if out["latest"] != "v9.0.0" {
		t.Errorf("latest: %v", out)
	}
	if out["html_url"] != "https://github.com/yorah/dockbrr/releases/tag/v9.0.0" {
		t.Errorf("html_url: %v", out)
	}
}

func TestSelfUpdateEndpointForceBypassesCache(t *testing.T) {
	var hits int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.0.0","html_url":"https://x/y"}`))
	}))
	t.Cleanup(gh.Close)

	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, selfUpdateDeps(t, db, gh.URL, "0.4.2"))

	// First cached call warms the cache (1 hit).
	if rec := authedGet(t, srv, "/api/updates/self", tok, csrf); rec.Code != http.StatusOK {
		t.Fatalf("warm: want 200, got %d", rec.Code)
	}
	// Forced call must refetch despite the fresh cache (2nd hit).
	rec := authedGet(t, srv, "/api/updates/self?force=true", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("force: want 200, got %d", rec.Code)
	}
	if hits != 2 {
		t.Errorf("force=true must bypass cache; GitHub hits=%d, want 2", hits)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["latest"] != "v9.0.0" || out["update_available"] != true {
		t.Errorf("forced verdict: %v", out)
	}
}

func TestSelfUpdateEndpointNilDep(t *testing.T) {
	// Deps without a SelfUpdate checker must degrade to update_available:false,
	// never panic (mirrors the nil-dep tolerance of other handlers).
	srv, _, tok, csrf := authedServer(t, Deps{})
	rec := authedGet(t, srv, "/api/updates/self", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["update_available"] != false {
		t.Errorf("nil dep should be false: %v", out)
	}
	if _, ok := out["checked_at"]; ok {
		t.Errorf("nil dep must not carry a checked_at: %v", out)
	}
}

// ghLatestServer serves a fake GitHub releases/latest response with the given
// tag, for driving the self-update checker without hitting the real network.
func ghLatestServer(t *testing.T, tag string) string {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/yorah/dockbrr/releases/tag/` + tag + `"}`))
	}))
	t.Cleanup(gh.Close)
	return gh.URL
}

func TestSelfUpdateApplyNotContainerized409(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	d := selfUpdateDeps(t, db, ghLatestServer(t, "v9.0.0"), "0.4.2")
	d.Engine = eng
	// SelfID left "" (host install): must 409 before ever consulting the checker.
	srv.deps = mergeDeps(srv.deps, d)

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/self/apply", nil), tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply without SelfID = %d, want 409", rec.Code)
	}
	if len(eng.enqueued) != 0 {
		t.Fatalf("enqueued %d jobs, want 0", len(eng.enqueued))
	}
}

func TestSelfUpdateApplyNoUpdate409(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	// tag equals current: checker reports update_available:false.
	d := selfUpdateDeps(t, db, ghLatestServer(t, "v0.4.2"), "0.4.2")
	d.Engine = eng
	d.SelfID = "abc123def456"
	srv.deps = mergeDeps(srv.deps, d)

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/self/apply", nil), tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply with no update available = %d, want 409", rec.Code)
	}
	if len(eng.enqueued) != 0 {
		t.Fatalf("enqueued %d jobs, want 0", len(eng.enqueued))
	}
	if msg := errorBody(t, rec); msg != "no dockbrr update is available" {
		t.Errorf("error message = %q", msg)
	}
}

// errorBody decodes the {"error": "..."} body writeJSONError produces.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out["error"]
}

// erroringGHServer always 500s, so the checker returns a genuine error (as
// opposed to a soft "no update" verdict) when there's no cache to fall back on.
func erroringGHServer(t *testing.T) string {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	return gh.URL
}

func TestSelfUpdateApplyCheckError409(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	d := selfUpdateDeps(t, db, erroringGHServer(t), "0.4.2")
	d.Engine = eng
	d.SelfID = "abc123def456"
	srv.deps = mergeDeps(srv.deps, d)

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/self/apply", nil), tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply with checker error = %d, want 409", rec.Code)
	}
	if len(eng.enqueued) != 0 {
		t.Fatalf("enqueued %d jobs, want 0", len(eng.enqueued))
	}
	msg := errorBody(t, rec)
	if msg != "could not check for updates, try again later" {
		t.Errorf("error message = %q", msg)
	}
	if msg == "no dockbrr update is available" {
		t.Error("checker-error body must differ from the genuine no-update body")
	}
}

func TestSelfUpdateApplyEnqueues(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	d := selfUpdateDeps(t, db, ghLatestServer(t, "v9.0.0"), "0.4.2")
	d.Engine = eng
	d.SelfID = "abc123def456"
	srv.deps = mergeDeps(srv.deps, d)

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/self/apply", nil), tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if len(eng.enqueued) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(eng.enqueued))
	}
	if got := eng.enqueued[0].Type; got != "self_update" {
		t.Fatalf("enqueued job type = %q, want self_update", got)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["job_id"] != float64(1) {
		t.Errorf("job_id = %v, want 1", out["job_id"])
	}
}

func TestSelfUpdateApplySingleFlight(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	d := selfUpdateDeps(t, db, ghLatestServer(t, "v9.0.0"), "0.4.2")
	d.Engine = eng
	d.SelfID = "abc123def456"
	d.Jobs = store.NewJobs(db)
	srv.deps = mergeDeps(srv.deps, d)

	// A self_update job is already queued (e.g. from an earlier request, or
	// enqueued directly by another path). The apply endpoint must not stack a
	// second one on top of it.
	existingID, err := d.Jobs.Enqueue(store.Job{Type: "self_update", RequestedBy: "user"})
	if err != nil {
		t.Fatal(err)
	}

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/self/apply", nil), tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply with in-flight self_update = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if len(eng.enqueued) != 0 {
		t.Fatalf("enqueued %d new jobs via Engine, want 0 (single-flight)", len(eng.enqueued))
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["job_id"] != float64(existingID) {
		t.Errorf("job_id = %v, want existing id %d", out["job_id"], existingID)
	}
}

func TestSelfUpdateApplyRequiresAuth(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/updates/self/apply", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestSelfUpdateApplyRequiresCSRF(t *testing.T) {
	srv, _, tok, _ := authedServer(t, Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/updates/self/apply", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok}) // cookie, no CSRF header
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}
