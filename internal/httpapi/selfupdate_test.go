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
}
