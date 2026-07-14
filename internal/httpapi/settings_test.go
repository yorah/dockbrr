package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"dockbrr/internal/config"
	"dockbrr/internal/logger"
	"dockbrr/internal/secret"
	"dockbrr/internal/store"
)

func settingsDeps(t *testing.T, db *store.DB) Deps {
	t.Helper()
	key, _ := secret.LoadOrCreateKey(t.TempDir())
	sealer, _ := secret.NewSealer(key)
	return Deps{
		Sealer:      sealer,
		Settings:    store.NewSettings(db, sealer),
		Credentials: store.NewCredentials(db, sealer),
		Projects:    store.NewProjects(db),
		Services:    store.NewServices(db),
		HostID:      1,
	}
}

func TestSettingsGetPutRoundTrip(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	// Re-wire settings deps onto the same db.
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	put := authReq(httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"poll_interval_seconds":"300","cache_ttl_seconds":"600","github_token":"ghp_secret"}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT settings = %d body=%s", rec.Code, rec.Body.String())
	}

	get := authReq(httptest.NewRequest(http.MethodGet, "/api/settings", nil), tok, csrf)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, get)
	body := rec.Body.String()
	if !strings.Contains(body, `"poll_interval_seconds":"300"`) {
		t.Fatalf("GET settings missing value: %s", body)
	}
	if !strings.Contains(body, `"cache_ttl_seconds":"600"`) {
		t.Fatalf("GET settings missing cache_ttl_seconds round-trip: %s", body)
	}
	if !strings.Contains(body, `"github_token_set":true`) {
		t.Fatalf("github_token_set not true: %s", body)
	}
	if strings.Contains(body, "ghp_secret") {
		t.Fatalf("secret token leaked in GET: %s", body)
	}

	var parsed struct {
		RestartRequired []string `json:"restart_required"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode GET settings body: %v", err)
	}
	found := false
	for _, k := range parsed.RestartRequired {
		if k == "concurrency" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restart_required = %v, want to contain %q", parsed.RestartRequired, "concurrency")
	}
	// Stored sealed, not plaintext.
	raw, _ := s.deps.Settings.Get("github_token")
	if raw == "ghp_secret" {
		t.Fatal("github_token stored in plaintext")
	}
	// And decrypts back.
	if got, _ := s.deps.Settings.GetSecret("github_token"); got != "ghp_secret" {
		t.Fatalf("secret round trip = %q", got)
	}
}

// TestPutSettingsRejectsUnknownKeyWithoutPartialWrite guards against a
// single-pass validate-and-write loop: since map iteration order is
// randomized, a body with one valid key and one unknown key could
// previously write the valid key before reaching the unknown key and
// returning 400. The client sees "rejected" but the setting was mutated
// anyway. The two-pass fix validates every key first, so a 400 response
// must mean nothing was persisted, regardless of map order.
func TestPutSettingsRejectsUnknownKeyWithoutPartialWrite(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	put := authReq(httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"poll_interval_seconds":"300","bogus":"x"}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, put)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT settings with unknown key = %d body=%s, want 400", rec.Code, rec.Body.String())
	}

	if _, err := s.deps.Settings.Get("poll_interval_seconds"); !errors.Is(err, store.ErrSettingNotFound) {
		t.Fatalf("poll_interval_seconds was written despite rejected request: err=%v", err)
	}
}

func TestRegistryCredentialCRUD(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	post := authReq(httptest.NewRequest(http.MethodPost, "/api/registries",
		strings.NewReader(`{"host":"ghcr.io","username":"alice","password":"p"}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, post)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST registry = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, authReq(httptest.NewRequest(http.MethodGet, "/api/registries", nil), tok, csrf))
	if !strings.Contains(rec.Body.String(), "ghcr.io") || strings.Contains(rec.Body.String(), `"p"`) {
		t.Fatalf("GET registries = %s", rec.Body.String())
	}

	del := authReq(httptest.NewRequest(http.MethodDelete, "/api/registries/ghcr.io", nil), tok, csrf)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, del)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE registry = %d", rec.Code)
	}
}

func TestAutoUpdateToggles(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})

	pp := authReq(httptest.NewRequest(http.MethodPut, pathf("/api/projects/%d/auto-update", pid),
		strings.NewReader(`{"enabled":true}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, pp)
	if rec.Code != http.StatusOK {
		t.Fatalf("project toggle = %d", rec.Code)
	}
	if got, _ := store.NewProjects(db).Get(pid); !got.AutoUpdateEnabled {
		t.Fatal("project auto-update not enabled")
	}

	sp := authReq(httptest.NewRequest(http.MethodPut, pathf("/api/services/%d/auto-update", sid),
		strings.NewReader(`{"enabled":true}`)), tok, csrf)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, sp)
	if rec.Code != http.StatusOK {
		t.Fatalf("service toggle = %d", rec.Code)
	}
	if got, _ := store.NewServices(db).Get(sid); got.AutoUpdateEnabled == nil || !*got.AutoUpdateEnabled {
		t.Fatal("service auto-update not enabled")
	}
}

func TestManualComposeIntake(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(file, []byte("services:\n  web:\n    image: nginx:1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"manual-web","working_dir":"` + dir + `","config_files":["` + file + `"]}`
	req := authReq(httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("manual intake = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The persisted/returned name is the compose-derived label, not the body
	// name. This is the property the health-gate re-discovery depends on.
	if out.Name == "" || out.Name == "manual-web" {
		t.Fatalf("response name = %q; want the compose-derived label, not the body name", out.Name)
	}
	got, _ := store.NewProjects(db).List()
	found := false
	for _, p := range got {
		if p.Source == "manual" && p.Name == out.Name {
			found = true
			// Manual creation must stay opt-in regardless of
			// default_auto_update_enabled: that setting only affects
			// discovery's brand-new-project path, never manual intake.
			if p.AutoUpdateEnabled {
				t.Error("manually-created project has auto_update_enabled=true, want false")
			}
		}
	}
	if !found {
		t.Fatalf("manual project not persisted with source=manual and name=%q", out.Name)
	}

	// Manual intake must seed one services row per compose service so
	// detection can begin without waiting for containers.
	svcs, err := s.deps.Services.ListByProject(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 { // the fixture compose file defines one service
		t.Fatalf("want 1 seeded service, got %d", len(svcs))
	}
	if svcs[0].Name == "" || svcs[0].ImageRef == "" {
		t.Errorf("seeded service missing name/image: %+v", svcs[0])
	}
	if svcs[0].CurrentDigest != "" {
		t.Errorf("seeded service must have empty digest until containers are matched")
	}

	// An unparseable/missing compose file is rejected.
	bad := `{"name":"broken","working_dir":"` + dir + `","config_files":["` + filepath.Join(dir, "nope.yml") + `"]}`
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, authReq(httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(bad)), tok, csrf))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad intake = %d, want 400", rec.Code)
	}
}

// writeFile is a test helper to write a file to a directory.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreateProjectUsesComposeLabelNotBodyName(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv = New(config.Config{}, db, mergeDeps(srv.deps, Deps{Projects: store.NewProjects(db), Services: store.NewServices(db), HostID: 1}))

	dir := t.TempDir()
	// No top-level `name:` → compose derives the project name from the dir base.
	composeFile := filepath.Join(dir, "docker-compose.yml")
	writeFile(t, composeFile,
		"services:\n  web:\n    image: nginx:1.27\n")

	body := pathf(`{"name":"totally-different","working_dir":%q,"config_files":[%q]}`, dir, composeFile)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	// The stored name must equal the compose-derived label (dir base), NOT the
	// body's "totally-different", so health-gate re-discovery matches containers.
	got, err := store.NewProjects(db).Get(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name == "totally-different" {
		t.Fatal("stored the body name; must store the compose-derived label")
	}
	if out.Name != got.Name {
		t.Fatalf("response name %q != stored name %q", out.Name, got.Name)
	}
}

// TestSettingsExportOmitsSecretsAndImportRoundTrips verifies that the export
// endpoint never emits sealed secrets (github_token, registry passwords) and
// that a subsequent import restores non-secret settings, upserts only NEW
// registry hosts (with empty passwords), skips unknown keys, and never
// overwrites an existing registry's stored password.
func TestSettingsExportOmitsSecretsAndImportRoundTrips(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	const tokenSecret = "ghp_supersecret_value"
	const regPassword = "registry_password_value"

	// Seed: a non-secret setting, a sealed secret, and a credentialed registry.
	if err := s.deps.Settings.Set("poll_interval_seconds", "600"); err != nil {
		t.Fatal(err)
	}
	if err := s.deps.Settings.SetSecret("github_token", tokenSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.deps.Credentials.Upsert("ghcr.io", "alice", regPassword); err != nil {
		t.Fatal(err)
	}

	// GET export, then inspect the raw body for any secret leakage.
	exp := authReq(httptest.NewRequest(http.MethodGet, "/api/settings/export", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, exp)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET export = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"poll_interval_seconds":"600"`) {
		t.Fatalf("export missing poll_interval_seconds: %s", body)
	}
	if strings.Contains(body, tokenSecret) {
		t.Fatalf("github_token secret leaked in export: %s", body)
	}
	if strings.Contains(body, regPassword) {
		t.Fatalf("registry password leaked in export: %s", body)
	}
	if strings.Contains(body, "github_token") {
		t.Fatalf("github_token key must not appear in export at all: %s", body)
	}
	// Sealed ciphertext of the token must not leak either.
	if sealed, _ := s.deps.Settings.Get("github_token"); sealed != "" && strings.Contains(body, sealed) {
		t.Fatalf("github_token ciphertext leaked in export: %s", body)
	}

	var payload struct {
		Version    int               `json:"version"`
		Settings   map[string]string `json:"settings"`
		Registries []struct {
			Host     string `json:"host"`
			Username string `json:"username"`
		} `json:"registries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode export body: %v", err)
	}
	if payload.Version != 1 {
		t.Fatalf("export version = %d, want 1", payload.Version)
	}
	if len(payload.Registries) != 1 || payload.Registries[0].Host != "ghcr.io" || payload.Registries[0].Username != "alice" {
		t.Fatalf("export registries = %+v", payload.Registries)
	}

	// Wipe the non-secret setting, then round-trip it back via import. Add an
	// unknown key and a secret key to confirm both are skipped, and a brand-new
	// registry host to confirm it is created with an empty password.
	if err := s.deps.Settings.Set("poll_interval_seconds", ""); err != nil {
		t.Fatal(err)
	}
	importBody := `{"version":1,"settings":{"poll_interval_seconds":"600","bogus_key":"x","github_token":"should_be_skipped"},` +
		`"registries":[{"host":"ghcr.io","username":"alice"},{"host":"docker.io","username":"bob"}]}`
	imp := authReq(httptest.NewRequest(http.MethodPost, "/api/settings/import", strings.NewReader(importBody)), tok, csrf)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, imp)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST import = %d body=%s", rec.Code, rec.Body.String())
	}
	var res struct {
		Applied int      `json:"applied"`
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode import body: %v", err)
	}

	// poll_interval_seconds restored.
	if v, _ := s.deps.Settings.Get("poll_interval_seconds"); v != "600" {
		t.Fatalf("poll_interval_seconds after import = %q, want 600", v)
	}
	// The secret key must NOT have been applied through import.
	if got, err := s.deps.Settings.GetSecret("github_token"); err != nil || got != tokenSecret {
		t.Fatalf("github_token was mutated by import: got=%q err=%v", got, err)
	}
	// Unknown + secret keys reported as skipped.
	if !contains(res.Skipped, "bogus_key") {
		t.Fatalf("skipped = %v, want to contain bogus_key", res.Skipped)
	}
	if !contains(res.Skipped, "github_token") {
		t.Fatalf("skipped = %v, want to contain github_token (secret keys are never imported)", res.Skipped)
	}
	// Existing registry host must be skipped and its stored password untouched.
	if _, pw, ok := s.deps.Credentials.Lookup("ghcr.io"); !ok || pw != regPassword {
		t.Fatalf("existing registry password mutated by import: ok=%v pw=%q", ok, pw)
	}
	// New registry host created with an EMPTY password placeholder.
	if _, pw, ok := s.deps.Credentials.Lookup("docker.io"); !ok || pw != "" {
		t.Fatalf("new registry docker.io = ok=%v pw=%q, want created with empty password", ok, pw)
	}
}

func TestSettingsImportRejectsUnknownVersion(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	if err := s.deps.Settings.Set("poll_interval_seconds", ""); err != nil {
		t.Fatal(err)
	}

	importBody := `{"version":2,"settings":{"poll_interval_seconds":"600"},"registries":[]}`
	imp := authReq(httptest.NewRequest(http.MethodPost, "/api/settings/import", strings.NewReader(importBody)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, imp)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST import with version=2 = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	// Nothing should have been applied.
	if v, _ := s.deps.Settings.Get("poll_interval_seconds"); v != "" {
		t.Fatalf("poll_interval_seconds after rejected import = %q, want unchanged empty", v)
	}
}

// Import must run log_level through the SAME validation as PUT: a garbage level
// is rejected with a 400 and, because validation fully precedes any write,
// nothing from that payload is persisted (map iteration order is randomized, so
// a validate-as-you-write loop could otherwise leave a valid key behind).
func TestSettingsImportRejectsInvalidLogLevelWithoutPartialWrite(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))
	if err := s.deps.Settings.Set("log_level", "warn"); err != nil {
		t.Fatal(err)
	}

	importBody := `{"version":1,"settings":{"poll_interval_seconds":"600","log_level":"bogus"},"registries":[]}`
	imp := authReq(httptest.NewRequest(http.MethodPost, "/api/settings/import", strings.NewReader(importBody)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, imp)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import with log_level=bogus = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if v, _ := s.deps.Settings.Get("log_level"); v != "warn" {
		t.Errorf("rejected import mutated log_level to %q, want warn", v)
	}
	if _, err := s.deps.Settings.Get("poll_interval_seconds"); !errors.Is(err, store.ErrSettingNotFound) {
		t.Errorf("rejected import partially wrote poll_interval_seconds: err=%v", err)
	}
}

// A VALID imported log_level must not only persist but take effect immediately,
// exactly like the PUT path, otherwise the level silently lags until restart.
func TestSettingsImportAppliesLogLevel(t *testing.T) {
	if _, err := logger.Init(logger.Config{Level: "info"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.SetLevel("info") })

	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	importBody := `{"version":1,"settings":{"log_level":"debug"},"registries":[]}`
	imp := authReq(httptest.NewRequest(http.MethodPost, "/api/settings/import", strings.NewReader(importBody)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, imp)
	if rec.Code != http.StatusOK {
		t.Fatalf("import log_level=debug = %d body=%s", rec.Code, rec.Body.String())
	}
	if v, _ := s.deps.Settings.Get("log_level"); v != "debug" {
		t.Errorf("persisted log_level = %q, want debug", v)
	}
	if got := zerolog.GlobalLevel(); got != zerolog.DebugLevel {
		t.Errorf("global log level = %v after import, want debug (import must apply the level, not wait for a restart)", got)
	}
}

func TestGetSettingsReturnsDefaultsWhenUnset(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	// GET with an empty settings store should return defaults, not empty strings
	get := authReq(httptest.NewRequest(http.MethodGet, "/api/settings", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, get)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"poll_interval_seconds":  "900",
		"concurrency":            "2",
		"health_timeout_seconds": "120",
		"health_poll_seconds":    "2",
		"cache_ttl_seconds":      "600",
		"scan_on_start":          "true",
	}
	for k, v := range want {
		if got, _ := out[k].(string); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// write_back_compose should also return its default
	if got, _ := out["write_back_compose"].(string); got != "true" {
		t.Errorf("write_back_compose = %q, want %q", got, "true")
	}
}

// The UI dims a setting still sitting on its default, which it can only do if
// GET reports the defaults alongside the effective values. Secrets are excluded.
func TestGetSettingsEchoesDefaults(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	// A user-set value must not change what the defaults map reports.
	body := strings.NewReader(`{"concurrency":"8"}`)
	put := authReq(httptest.NewRequest(http.MethodPut, "/api/settings", body), tok, csrf)
	put.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(httptest.NewRecorder(), put)

	get := authReq(httptest.NewRequest(http.MethodGet, "/api/settings", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, get)

	var out struct {
		Concurrency string            `json:"concurrency"`
		Defaults    map[string]string `json:"defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Concurrency != "8" {
		t.Errorf("concurrency = %q, want %q", out.Concurrency, "8")
	}
	if got := out.Defaults["concurrency"]; got != "2" {
		t.Errorf("defaults.concurrency = %q, want %q (the default, not the set value)", got, "2")
	}
	for key, want := range settingDefaults {
		if got := out.Defaults[key]; got != want {
			t.Errorf("defaults[%q] = %q, want %q", key, got, want)
		}
	}
	if _, ok := out.Defaults["github_token"]; ok {
		t.Error("defaults must not include the github_token secret")
	}
}

// TestSettingDefaultsMatchConsumers verifies that settingDefaults matches the
// hardcoded defaults used by consumer call sites in cmd/dockbrr/main.go
// (settingDuration/settingInt). This pins settingDefaults so any future change
// is caught in review rather than silently drifting from the consumers.
func TestSettingDefaultsMatchConsumers(t *testing.T) {
	// These mirror the hardcoded defaults at the consumer call sites in
	// cmd/dockbrr/main.go (settingDuration/settingInt). Keep both in sync; this
	// pins settingDefaults so a silent change is caught in review.
	want := map[string]string{
		"poll_interval_seconds":  "900",
		"concurrency":            "2",
		"health_timeout_seconds": "120",
		"health_poll_seconds":    "2",
		"cache_ttl_seconds":      "600",
		// Discovery-side consumers (internal/discovery/discovery.go auto-prune
		// pass). Pinned here too so drift from the discovery.go literals is
		// caught, not just the main.go-consumed defaults above.
		"auto_remove_gone":   "true",
		"gone_grace_seconds": "3600",
		// New-project auto-update default (internal/discovery/discovery.go
		// Reconcile, GetBoolDefault("default_auto_update_enabled", false)).
		"default_auto_update_enabled": "false",
		// Pruner consumer (cmd/dockbrr/main.go pruneLoop). 0 disables pruning.
		"job_retention_days": "30",
		// Boot-scan gate (cmd/dockbrr/main.go schedulerLoop,
		// GetBoolDefault("scan_on_start", true)).
		"scan_on_start": "true",
	}
	for k, v := range want {
		if settingDefaults[k] != v {
			t.Errorf("settingDefaults[%q] = %q, want %q (must match cmd/dockbrr/main.go)", k, settingDefaults[k], v)
		}
	}
}

func TestGoneSettingsDefaultsAndAccept(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))

	// GET with an empty settings store should return defaults for auto_remove_gone and gone_grace_seconds
	get := authReq(httptest.NewRequest(http.MethodGet, "/api/settings", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, get)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := out["auto_remove_gone"].(string); got != "true" {
		t.Errorf("auto_remove_gone default = %v, want %q", got, "true")
	}
	if got, _ := out["gone_grace_seconds"].(string); got != "3600" {
		t.Errorf("gone_grace_seconds default = %v, want %q", got, "3600")
	}

	// PUT new values
	put := authReq(httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"auto_remove_gone":"false","gone_grace_seconds":"600"}`)), tok, csrf)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT settings = %d body=%s", rec.Code, rec.Body.String())
	}

	// GET again and verify persisted values
	get2 := authReq(httptest.NewRequest(http.MethodGet, "/api/settings", nil), tok, csrf)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, get2)

	var out2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatal(err)
	}
	if got, _ := out2["auto_remove_gone"].(string); got != "false" {
		t.Errorf("auto_remove_gone after PUT = %v, want %q", got, "false")
	}
	if got, _ := out2["gone_grace_seconds"].(string); got != "600" {
		t.Errorf("gone_grace_seconds after PUT = %v, want %q", got, "600")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
