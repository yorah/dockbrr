package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"dockbrr/internal/config"
	"dockbrr/internal/store"
	"dockbrr/internal/version"
)

func TestHealthzDegradedWhenDBDown(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "h2.db"))
	if err != nil {
		t.Fatal(err)
	}

	srv := New(config.Config{}, db, Deps{})
	_ = db.Close() // force db.Ping() to fail

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "degraded" {
		t.Fatalf("status field = %q, want degraded", body["status"])
	}
	if body["version"] != version.Version {
		t.Fatalf("version field = %q, want %q", body["version"], version.Version)
	}
}

func TestHealthzOK(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	srv := New(config.Config{}, db, Deps{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %q, want ok", body["status"])
	}
	if body["version"] != version.Version {
		t.Fatalf("version field = %q, want %q", body["version"], version.Version)
	}
}
