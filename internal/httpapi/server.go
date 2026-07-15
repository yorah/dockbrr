// Package httpapi serves the REST + SSE API and the embedded SPA.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"dockbrr/internal/config"
	"dockbrr/internal/job"
	"dockbrr/internal/secret"
	"dockbrr/internal/store"
	"dockbrr/internal/version"
)

// JobService is the Job Engine surface the API needs: enqueue mutating jobs and
// subscribe to a job's live log stream. *job.Engine satisfies it. The API NEVER
// shells Docker; mutating actions become persisted jobs via Enqueue.
type JobService interface {
	Enqueue(j store.Job) (int64, error)
	Stream(id int64) (<-chan job.LogLine, error)
}

// Checker triggers read-only detection, either a fresh check of one service
// (invalidating its detect cache first, so a manual check always does a full
// re-scan) or a sweep of every service. *scan.Scanner satisfies it. Detection
// does not go through the Job Engine (read-only).
type Checker interface {
	CheckServiceFresh(ctx context.Context, serviceID int64) error
	CheckAll(ctx context.Context) error
}

// DockerPinger re-probes daemon liveness on each /api/status request so the
// dashboard tile reflects the current state, not a boot-time snapshot that goes
// stale when Docker recovers or dies after startup. *docker.Client satisfies it;
// a nil pinger is reported unreachable.
type DockerPinger interface {
	Ping(ctx context.Context) error
}

// DockerLogsReader reads a bounded tail of a container's logs. *docker.Client
// satisfies it. Read-only, so it may be called directly from an API handler.
type DockerLogsReader interface {
	ContainerLogsTail(ctx context.Context, id string, tail int) (string, error)
}

// LogConfig carries the static (bootstrap) log settings for the config endpoint.
// The live level is read from the log_level DB setting, not here.
type LogConfig struct {
	Path       string
	MaxSizeMB  int
	MaxBackups int
}

// Deps are the wired dependencies handlers use. Tests pass fakes for Engine /
// Checker; the real engine + scanner are wired in cmd/dockbrr/main.go.
type Deps struct {
	Sealer       *secret.Sealer
	Users        *store.Users
	Sessions     *store.Sessions
	Credentials  *store.Credentials
	Settings     *store.Settings
	Services     *store.Services
	Projects     *store.Projects
	Updates      *store.Updates
	Events       *store.Events
	Jobs         *store.Jobs
	JobLogs      *store.JobLogs
	RemoteStates *store.RemoteStates
	Engine       JobService
	Checker      Checker
	HostID       int64
	DockerPinger DockerPinger
	// DockerLogs is the read-only container-logs reader. Nil disables the logs
	// endpoint (returns 503). Read-only: this is the sole API->docker read path.
	DockerLogs DockerLogsReader
	Bus        *Bus
	LogConfig    LogConfig
	// NextScan reports when the scheduler will next run a check-all. Zero time
	// (or a nil func, as in tests) means "unknown" and is omitted from /status.
	NextScan func() time.Time
	// StartedAt is the process start time, reported by /api/system/info so the
	// UI can tick uptime client-side. Zero (as in tests) omits the field.
	StartedAt time.Time
	// DockerVersion reports the daemon + API version for /api/system/info.
	// Optional: nil (tests, Docker never came up) degrades to no version, not
	// an error, mirrors the NextScan idiom.
	DockerVersion func(ctx context.Context) (version, apiVersion string, err error)
}

type Server struct {
	cfg  config.Config
	db   *store.DB
	deps Deps
	mux  *chi.Mux
}

// New builds the API server. deps carries the wired repos + engine/checker;
// pass Deps{} in tests that exercise only /healthz or /api/projects.
func New(cfg config.Config, db *store.DB, deps Deps) *Server {
	s := &Server{cfg: cfg, db: db, deps: deps, mux: chi.NewRouter()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// serviceName resolves a service id to its name for log lines, so an operator
// reading "service 307" doesn't have to go look up which container that is.
// Best-effort: an unknown id (or a Deps without Services, as in tests) logs "?".
func (s *Server) serviceName(id int64) string {
	if s.deps.Services == nil {
		return "?"
	}
	svc, err := s.deps.Services.Get(id)
	if err != nil {
		return "?"
	}
	return svc.Name
}

func (s *Server) routes() {
	s.mux.Get("/healthz", s.handleHealth)

	// Open (pre-auth) routes.
	s.mux.Get("/api/setup/status", s.handleSetupStatus)
	s.mux.Post("/api/setup", s.handleSetup)
	s.mux.Post("/api/auth/login", s.handleLogin)

	// Authenticated API surface (later tasks add more routes to this group).
	s.mux.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)
		r.Post("/api/auth/password", s.handleChangePassword)
		r.Get("/api/status", s.handleStatus)
		r.Get("/api/system/info", s.handleSystemInfo)
		r.Get("/api/projects", s.handleProjects)
		r.Get("/api/projects/{id}/compose", s.handleProjectCompose)
		r.Get("/api/settings", s.handleGetSettings)
		r.Put("/api/settings", s.handlePutSettings)
		r.Get("/api/settings/export", s.handleExportSettings)
		r.Post("/api/settings/import", s.handleImportSettings)
		r.Get("/api/registries", s.handleListRegistries)
		r.Post("/api/registries", s.handleUpsertRegistry)
		r.Delete("/api/registries/{host}", s.handleDeleteRegistry)
		r.Post("/api/projects", s.handleCreateProject)
		r.Put("/api/projects/{id}/auto-update", s.handleProjectAutoUpdate)
		r.Put("/api/services/{id}/auto-update", s.handleServiceAutoUpdate)
		r.Get("/api/updates", s.handleListUpdates)
		r.Get("/api/updates/last-applied", s.handleListLastApplied)
		r.Get("/api/updates/{id}/preview", s.handlePreview)
		r.Post("/api/updates/{id}/apply", s.handleApply)
		r.Post("/api/updates/{id}/dismiss", s.handleDismiss)
		r.Post("/api/updates/{id}/restore", s.handleRestore)
		r.Post("/api/services/{id}/check", s.handleCheck)
		r.Post("/api/services/{id}/lifecycle", s.handleLifecycle)
		r.Post("/api/services/{id}/remove", s.handleRemove)
		r.Get("/api/services/{id}/logs", s.handleLogs)
		r.Post("/api/scan", s.handleScanAll)
		r.Get("/api/services/{id}/events", s.handleServiceEvents)
		r.Get("/api/jobs", s.handleListJobs)
		r.Delete("/api/jobs", s.handleClearJobs)
		r.Get("/api/jobs/{id}", s.handleGetJob)
		r.Post("/api/jobs/{id}/rollback", s.handleRollback)
		r.Get("/api/jobs/{id}/logs", s.handleJobLogs)
		r.Get("/api/events/stream", s.handleEventStream)
		r.Get("/api/logs/config", s.handleLogConfig)
		r.Get("/api/logs/files", s.handleLogFiles)
		r.Get("/api/logs/files/{name}/download", s.handleLogDownload)
	})

	// SPA fallback: any route not matched above (i.e. not /healthz and not an
	// /api/* handler) serves the embedded SPA. Registered /api/* + /healthz keep
	// their handlers; the SPA handler itself 404s /api/* and /healthz so an
	// unmatched /api path never yields index.html.
	s.mux.NotFound(NewSPAHandler(distFS).ServeHTTP)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	code := http.StatusOK
	if err := s.db.Ping(); err != nil {
		status, code = "degraded", http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  status,
		"version": version.Version,
	})
}
