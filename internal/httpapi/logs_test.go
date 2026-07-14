package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dockbrr/internal/logger"
)

func TestLogsFilesAndDownload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dockbrr.log")
	if _, err := logger.Init(logger.Config{Path: path, Level: "info", MaxSizeMB: 1, MaxBackups: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hello-log"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, tok, csrf := authedServer(t, Deps{})

	req := authReq(httptest.NewRequest(http.MethodGet, "/api/logs/files", nil), tok, csrf)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "dockbrr.log") {
		t.Fatalf("files: code=%d body=%s", rr.Code, rr.Body.String())
	}

	req = authReq(httptest.NewRequest(http.MethodGet, "/api/logs/files/dockbrr.log/download", nil), tok, csrf)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("download code=%d", rr.Code)
	}
	if b, _ := io.ReadAll(rr.Body); !strings.Contains(string(b), "hello-log") {
		t.Errorf("download body=%q", b)
	}
	if !strings.Contains(rr.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("missing attachment header")
	}
}

func TestLogDownloadRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dockbrr.log")
	if _, err := logger.Init(logger.Config{Path: path, Level: "info", MaxSizeMB: 1, MaxBackups: 1}); err != nil {
		t.Fatal(err)
	}
	s, _, tok, csrf := authedServer(t, Deps{})
	// A name containing a separator never matches the {name} segment cleanly;
	// chi still routes a %2F-encoded value, and logger.Open rejects it -> 400.
	req := authReq(httptest.NewRequest(http.MethodGet, "/api/logs/files/..%2Fsecret/download", nil), tok, csrf)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
		t.Errorf("traversal code=%d, want 400/404", rr.Code)
	}
}

func TestLogsRequireAuth(t *testing.T) {
	s, _, _, _ := authedServer(t, Deps{})
	req := httptest.NewRequest(http.MethodGet, "/api/logs/files", nil) // no cookie
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", rr.Code)
	}
}

func TestLogConfigReportsEffectiveLevel(t *testing.T) {
	dir := t.TempDir()
	if _, err := logger.Init(logger.Config{Path: filepath.Join(dir, "dockbrr.log"), Level: "info", MaxSizeMB: 7, MaxBackups: 4}); err != nil {
		t.Fatal(err)
	}
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))
	s.deps.LogConfig = LogConfig{Path: "/x/dockbrr.log", MaxSizeMB: 7, MaxBackups: 4}
	if err := s.deps.Settings.Set("log_level", "warn"); err != nil {
		t.Fatal(err)
	}

	req := authReq(httptest.NewRequest(http.MethodGet, "/api/logs/config", nil), tok, csrf)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, `"level":"warn"`) {
		t.Fatalf("config code=%d body=%s", rr.Code, body)
	}
	if !strings.Contains(body, `"maxSizeMB":7`) || !strings.Contains(body, `"maxBackups":4`) {
		t.Errorf("config missing static fields: %s", body)
	}
}

func TestPutSettingsLogLevelAppliesAndValidates(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	ok := authReq(httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"log_level":"debug"}`)), tok, csrf)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, ok)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT log_level=debug => %d body=%s", rr.Code, rr.Body.String())
	}
	if v, _ := s.deps.Settings.Get("log_level"); v != "debug" {
		t.Errorf("persisted log_level=%q, want debug", v)
	}

	bad := authReq(httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"log_level":"bogus"}`)), tok, csrf)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, bad)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT log_level=bogus => %d, want 400", rr.Code)
	}
	if v, _ := s.deps.Settings.Get("log_level"); v != "debug" {
		t.Errorf("rejected write mutated log_level to %q", v)
	}
}
