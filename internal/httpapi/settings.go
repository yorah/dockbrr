package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"dockbrr/internal/compose"
	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// settingDefaults is the single source of truth for editable-setting defaults.
// GET falls back to these when a key is unset so the UI shows real values, and
// the consumer call sites (settingDuration/settingInt in main.go) use the same
// numbers. Any change here must be reflected in cmd/dockbrr/main.go and is
// guarded by TestSettingDefaultsMatchConsumers in settings_test.go.
var settingDefaults = map[string]string{
	"poll_interval_seconds":       "900",
	"scan_on_start":               "true",
	"concurrency":                 "2",
	"health_timeout_seconds":      "120",
	"health_poll_seconds":         "2",
	"cache_ttl_seconds":           "600",
	"auto_remove_gone":            "true",
	"gone_grace_seconds":          "3600",
	"default_auto_update_enabled": "false",
	"write_back_compose":          "true",
	"log_level":                   "info",
	"job_retention_days":          "30",
}

// settingKeys is the whitelist of UI-editable settings and whether each is a
// sealed secret. Only these keys are read/written by the settings endpoints.
var settingKeys = []struct {
	key    string
	secret bool
}{
	{"poll_interval_seconds", false},
	{"scan_on_start", false},
	{"concurrency", false},
	{"health_timeout_seconds", false},
	{"health_poll_seconds", false},
	{"cache_ttl_seconds", false},
	{"auto_remove_gone", false},
	{"gone_grace_seconds", false},
	{"default_auto_update_enabled", false},
	{"write_back_compose", false},
	{"log_level", false},
	{"job_retention_days", false},
	{"github_token", true},
}

// restartRequired lists settingKeys that are only read at boot. Everything
// else in settingKeys is hot-reloaded (re-read live by the components that
// consume it) so a UI edit takes effect without a restart.
var restartRequired = []string{"concurrency", "scan_on_start"}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	for _, spec := range settingKeys {
		v, err := s.deps.Settings.Get(spec.key)
		if err != nil && !errors.Is(err, store.ErrSettingNotFound) {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		if spec.secret {
			out[spec.key+"_set"] = v != "" // never echo the secret
		} else {
			if v == "" {
				if def, ok := settingDefaults[spec.key]; ok {
					v = def
				}
			}
			out[spec.key] = v
		}
	}
	out["restart_required"] = restartRequired
	// Echo the defaults alongside the effective values so the UI can tell an
	// untouched setting from one the user happened to type the same number into,
	// and dim the former.
	defs := map[string]string{}
	for _, spec := range settingKeys {
		if spec.secret {
			continue // secrets have no meaningful default to show
		}
		if def, ok := settingDefaults[spec.key]; ok {
			defs[spec.key] = def
		}
	}
	out["defaults"] = defs
	writeJSON(w, http.StatusOK, out)
}

// validateSettingValue is the single source of truth for per-key value rules.
// Both PUT and import call it, and both call it on EVERY key before writing
// anything, so a rejected (400) request persists nothing. A nil return means
// the value is safe to store.
func validateSettingValue(key, value string) error {
	if key == "log_level" {
		if _, err := logger.ParseLevel(value); err != nil {
			return errors.New("invalid log_level: " + value)
		}
	}
	return nil
}

// applyLiveSettings performs the side effects of settings that are hot-applied
// rather than read at boot, so a write takes effect without a restart. Values
// must already have passed validateSettingValue. Both PUT and import call it.
func applyLiveSettings(values map[string]string) {
	if v, ok := values["log_level"]; ok {
		_ = logger.SetLevel(v) // validated above
	}
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := decodeJSON(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	allowed := map[string]bool{}
	secret := map[string]bool{}
	for _, spec := range settingKeys {
		allowed[spec.key] = true
		secret[spec.key] = spec.secret
	}
	// First pass: validate every key against the whitelist before writing
	// anything. Map iteration order is randomized, so validation must fully
	// precede any write, otherwise a rejected (400) request could still
	// mutate a valid key it happened to reach first. Fail-closed: a
	// rejected request persists nothing.
	for k, v := range body {
		if !allowed[k] {
			writeJSONError(w, http.StatusBadRequest, errors.New("unknown setting: "+k))
			return
		}
		if err := validateSettingValue(k, v); err != nil {
			writeJSONError(w, http.StatusBadRequest, err)
			return
		}
	}
	// Second pass: all keys are known-valid, so apply the writes.
	for k, v := range body {
		var err error
		switch {
		case secret[k] && v != "":
			err = s.deps.Settings.SetSecret(k, v)
		default: // non-secret, or a secret being cleared with ""
			err = s.deps.Settings.Set(k, v)
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
	}
	applyLiveSettings(body)
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// exportPayload is the JSON shape of both the export response and the import
// request body. Secrets (github_token, registry passwords) are intentionally
// absent; they never leave the sealed store.
type exportPayload struct {
	Version    int               `json:"version"`
	Settings   map[string]string `json:"settings"`
	Registries []exportRegistry  `json:"registries"`
}

type exportRegistry struct {
	Host     string `json:"host"`
	Username string `json:"username"`
}

// handleExportSettings emits every non-secret whitelisted setting plus the
// registry hosts/usernames (never their sealed passwords). The secret filter is
// the same settingKeys spec.secret flag used by GET/PUT, so a key can never be
// exported unless it is explicitly non-secret.
func (s *Server) handleExportSettings(w http.ResponseWriter, r *http.Request) {
	out := exportPayload{Version: 1, Settings: map[string]string{}, Registries: []exportRegistry{}}
	for _, spec := range settingKeys {
		if spec.secret {
			continue // secrets never leave the sealed store
		}
		v, err := s.deps.Settings.Get(spec.key)
		if err != nil {
			if errors.Is(err, store.ErrSettingNotFound) {
				continue
			}
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		if v != "" {
			out.Settings[spec.key] = v
		}
	}
	creds, err := s.deps.Credentials.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	for _, c := range creds {
		out.Registries = append(out.Registries, exportRegistry{Host: c.RegistryHost, Username: c.Username})
	}
	w.Header().Set("Content-Disposition", `attachment; filename="dockbrr-settings.json"`)
	writeJSON(w, http.StatusOK, out)
}

// handleImportSettings applies non-secret settings through the SAME whitelist as
// handlePutSettings: any unknown or secret key is skipped (never applied), so
// an import can never write github_token or any other sealed value. Registry
// hosts are created only if missing, with empty-password placeholders; an
// existing host's stored secret is never touched.
func (s *Server) handleImportSettings(w http.ResponseWriter, r *http.Request) {
	var in exportPayload
	if err := decodeJSON(r, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if in.Version != 1 {
		writeJSONError(w, http.StatusBadRequest, errors.New("unsupported import version"))
		return
	}
	// allowed = non-secret whitelist keys only. Secret keys are deliberately
	// excluded so they land in "skipped", identical to an unknown key.
	allowed := map[string]bool{}
	for _, spec := range settingKeys {
		if !spec.secret {
			allowed[spec.key] = true
		}
	}
	applied := 0
	skipped := []string{}
	// First pass: partition into applicable/skipped and validate every applicable
	// value through the SAME rules as PUT. Validation fully precedes any write, so
	// a bad value (e.g. log_level "bogus") rejects the whole payload with a 400
	// and persists nothing, so there's no half-applied import.
	applicable := map[string]string{}
	for k, v := range in.Settings {
		if !allowed[k] {
			skipped = append(skipped, k) // unknown or secret key: never applied
			continue
		}
		if err := validateSettingValue(k, v); err != nil {
			writeJSONError(w, http.StatusBadRequest, err)
			return
		}
		applicable[k] = v
	}
	// Second pass: all values are known-valid, so write them.
	for k, v := range applicable {
		if err := s.deps.Settings.Set(k, v); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		applied++
	}
	// Hot-apply the same way PUT does, right after the settings are persisted:
	// if a later registry upsert fails and 500s the import, the settings keys
	// are already committed to the store regardless, so applying them live now
	// keeps the running process (e.g. its log level) consistent with what was
	// just written instead of leaving it on stale state until a restart.
	applyLiveSettings(applicable)
	// Registries: create missing hosts (credential-less placeholders the user
	// fills in afterwards); never touch an existing host's stored secret.
	existing := map[string]bool{}
	creds, err := s.deps.Credentials.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	for _, c := range creds {
		existing[c.RegistryHost] = true
	}
	for _, reg := range in.Registries {
		if reg.Host == "" || existing[reg.Host] {
			skipped = append(skipped, "registry:"+reg.Host)
			continue
		}
		if _, err := s.deps.Credentials.Upsert(reg.Host, reg.Username, ""); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		existing[reg.Host] = true // guard against duplicate hosts in one payload
		applied++
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": applied, "skipped": skipped})
}

func (s *Server) handleListRegistries(w http.ResponseWriter, r *http.Request) {
	creds, err := s.deps.Credentials.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	if creds == nil {
		creds = []store.Credential{}
	}
	writeJSON(w, http.StatusOK, creds)
}

func (s *Server) handleUpsertRegistry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host     string `json:"host"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Host == "" {
		writeJSONError(w, http.StatusBadRequest, errors.New("host is required"))
		return
	}
	if _, err := s.deps.Credentials.Upsert(body.Host, body.Username, body.Password); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"host": body.Host})
}

func (s *Server) handleDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	host := chi.URLParam(r, "host")
	if host == "" {
		writeJSONError(w, http.StatusBadRequest, errors.New("host is required"))
		return
	}
	if err := s.deps.Credentials.Delete(host); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProjectAutoUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.deps.Projects.SetAutoUpdate(id, body.Enabled); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"auto_update_enabled": body.Enabled})
}

func (s *Server) handleServiceAutoUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"` // null clears the override (inherit)
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.deps.Services.SetAutoUpdate(id, body.Enabled); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auto_update_enabled": body.Enabled})
}

// handleCreateProject registers a manual compose project after validating that
// the compose files parse (pure-Go compose-go; no Docker).
func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string   `json:"name"`
		WorkingDir  string   `json:"working_dir"`
		ConfigFiles []string `json:"config_files"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" || len(body.ConfigFiles) == 0 {
		writeJSONError(w, http.StatusBadRequest, errors.New("name and config_files are required"))
		return
	}
	parsed, err := compose.Parse(context.Background(), body.WorkingDir, body.ConfigFiles)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, errors.New("compose files not parseable: "+err.Error()))
		return
	}
	// The compose project label, not the user-supplied name, is what the
	// health-gate re-discovery matches (com.docker.compose.project). Store it so
	// apply/rollback on a manual project can never mis-fire.
	id, err := s.deps.Projects.Upsert(store.Project{
		HostID:      s.deps.HostID,
		Kind:        "compose",
		Name:        parsed.Name,
		WorkingDir:  body.WorkingDir,
		ConfigFiles: body.ConfigFiles,
		Source:      "manual",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	// Seed one service row per compose service so detection can begin without
	// waiting for containers. Digest/container ids stay empty until discovery
	// matches running containers (services.Upsert preserves auto_update_enabled
	// and fills runtime columns on later reconciles).
	for _, cs := range parsed.Services {
		if cs.Image == "" {
			continue // build-only service: nothing to monitor
		}
		if _, err := s.deps.Services.Upsert(store.Service{
			ProjectID: id,
			Name:      cs.Name,
			ImageRef:  cs.Image,
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": parsed.Name})
}
