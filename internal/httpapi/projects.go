package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"dockbrr/internal/detect"
	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

type serviceDTO struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	ImageRef          string `json:"image_ref"`
	CurrentDigest     string `json:"current_digest"`
	State             string `json:"state"`
	Pinned            bool   `json:"pinned"`
	Drifted           bool   `json:"drifted"`
	Healthcheck       bool   `json:"healthcheck"`
	AutoUpdateEnabled *bool  `json:"auto_update_enabled"`
	CheckStatus       string `json:"check_status"`
	LastChecked       string `json:"last_checked"`
}

type projectDTO struct {
	ID                int64        `json:"id"`
	Name              string       `json:"name"`
	Kind              string       `json:"kind"`
	WorkingDir        string       `json:"working_dir"`
	AutoUpdateEnabled bool         `json:"auto_update_enabled"`
	Unmanaged         bool         `json:"unmanaged"`
	AutoNamed         bool         `json:"auto_named"`
	Services          []serviceDTO `json:"services"`
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects := store.NewProjects(s.db)
	services := store.NewServices(s.db)

	prs, err := projects.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}

	// A cache-read failure must not break the dashboard: remote stays nil and
	// every service's check_status/last_checked field falls back to "".
	var remote map[[2]string]store.RemoteState
	if s.deps.RemoteStates != nil {
		if m, err := s.deps.RemoteStates.All(); err == nil {
			remote = m
		}
	}

	out := make([]projectDTO, 0, len(prs))
	for _, pr := range prs {
		svcs, err := services.ListByProject(pr.ID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		sdtos := make([]serviceDTO, 0, len(svcs))
		for _, sv := range svcs {
			var checkStatus, lastChecked string
			if remote != nil {
				repo, tag := detect.SplitRef(sv.ImageRef)
				if st, ok := remote[[2]string{repo, tag}]; ok {
					checkStatus = st.Status
					if st.ResolvedAt != nil {
						lastChecked = st.ResolvedAt.UTC().Format(time.RFC3339)
					}
				}
			}
			sdtos = append(sdtos, serviceDTO{
				ID:                sv.ID,
				Name:              sv.Name,
				ImageRef:          sv.ImageRef,
				CurrentDigest:     sv.CurrentDigest,
				State:             sv.State,
				Pinned:            sv.Pinned,
				Drifted:           sv.Drifted,
				Healthcheck:       sv.Healthcheck,
				AutoUpdateEnabled: sv.AutoUpdateEnabled,
				CheckStatus:       checkStatus,
				LastChecked:       lastChecked,
			})
		}
		out = append(out, projectDTO{
			ID:                pr.ID,
			Name:              pr.Name,
			Kind:              pr.Kind,
			WorkingDir:        pr.WorkingDir,
			AutoUpdateEnabled: pr.AutoUpdateEnabled,
			Unmanaged:         pr.Unmanaged,
			AutoNamed:         pr.AutoNamed,
			Services:          sdtos,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// composeFileDTO is one compose config file backing a project, read straight
// off disk (invariant 6: no shell, no compose exec for a read-only view). A
// per-file read failure is reported in Error rather than failing the request.
type composeFileDTO struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// handleProjectCompose returns the raw content of each of a project's compose
// config files, for the compose-file viewer modal.
func (s *Server) handleProjectCompose(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	proj, err := s.deps.Projects.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err)
		return
	}
	files := make([]composeFileDTO, 0, len(proj.ConfigFiles))
	for _, p := range proj.ConfigFiles {
		f := composeFileDTO{Path: p}
		if b, rerr := os.ReadFile(p); rerr != nil {
			f.Error = rerr.Error()
		} else {
			f.Content = string(b)
		}
		files = append(files, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func writeJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// writeInternalError logs the real cause server-side (op names the call site) and
// returns a generic 500 to the client, so store/auth internals never leak to the
// browser. Use this instead of writeJSONError(w, 500, err) for unexpected errors.
func writeInternalError(w http.ResponseWriter, op string, err error) {
	logger.Errorf("httpapi: %s: %v", op, err)
	writeJSONError(w, http.StatusInternalServerError, errInternal)
}
