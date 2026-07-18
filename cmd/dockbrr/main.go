package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"dockbrr/internal/auth"
	"dockbrr/internal/changelog"
	"dockbrr/internal/compose"
	"dockbrr/internal/config"
	"dockbrr/internal/detect"
	"dockbrr/internal/discovery"
	"dockbrr/internal/docker"
	"dockbrr/internal/httpapi"
	"dockbrr/internal/job"
	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/scan"
	"dockbrr/internal/secret"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
	"dockbrr/internal/version"
)

const (
	syncInterval  = 60 * time.Second       // discovery reconcile cadence
	claimPoll     = 100 * time.Millisecond // engine idle poll
	httpClientTTL = 15 * time.Second
	pruneInterval = 24 * time.Hour // job-history retention sweep cadence

	// defaultJobRetentionDays mirrors settingDefaults["job_retention_days"] in
	// internal/httpapi/settings.go (pinned by TestSettingDefaultsMatchConsumers).
	defaultJobRetentionDays = 30

	// discoveryReadyTimeout bounds how long scan_on_start waits for discovery's
	// boot-time reconcile before running anyway. Without this wait, scan_on_start
	// races discovery's first pass and can check services against pre-recreate
	// store rows (stale image_ref/current_digest) on a fresh boot. Bounded so a
	// down/slow Docker daemon can't stall the boot scan. schedulerLoop must stay
	// safe without Docker.
	discoveryReadyTimeout = 10 * time.Second
)

// testCancel lets tests trigger graceful shutdown without an OS signal.
var (
	testCancelMu sync.Mutex
	testCancel   context.CancelFunc
)

func shutdownTestServer() {
	testCancelMu.Lock()
	f := testCancel
	testCancelMu.Unlock()
	if f != nil {
		f()
	}
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		_, _ = os.Stdout.WriteString("dockbrr " + version.Version + "\n")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "self-update-swap" {
		// Detached helper entry point: must not open the DB or bind the HTTP
		// server, so it returns here instead of falling into run().
		if err := runSelfUpdateSwap(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(os.Args[1:], os.Getenv); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, getenv func(string) string) error {
	cfg, err := config.Load(args, getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}

	if _, err := logger.Init(logger.Config{
		Path:       cfg.LogPath,
		Level:      cfg.LogLevel,
		MaxSizeMB:  cfg.LogMaxSizeMB,
		MaxBackups: cfg.LogMaxBackups,
	}); err != nil {
		return err
	}

	key, err := secret.LoadOrCreateKey(cfg.DataDir)
	if err != nil {
		return err
	}
	sealer, err := secret.NewSealer(key)
	if err != nil {
		return err
	}

	db, err := store.Open(filepath.Join(cfg.DataDir, "dockbrr.db"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Repositories.
	settings := store.NewSettings(db, sealer)
	users := store.NewUsers(db)
	sessions := store.NewSessions(db)
	creds := store.NewCredentials(db, sealer)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	updates := store.NewUpdates(db)
	events := store.NewEvents(db)
	images := store.NewImages(db)
	states := store.NewRemoteStates(db)
	tagCache := store.NewTagDigests(db)
	jobs := store.NewJobs(db)
	jobLogs := store.NewJobLogs(db)
	snapshots := store.NewSnapshots(db)
	changelogRepos := store.NewChangelogRepos(db)

	// First-boot marker.
	if _, err := settings.Get("installed_at"); err != nil {
		if !errors.Is(err, store.ErrSettingNotFound) {
			return err
		}
		if err := settings.Set("installed_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}

	// Persisted UI level overrides the bootstrap default.
	if lvl, err := settings.Get("log_level"); err == nil && lvl != "" {
		if serr := logger.SetLevel(lvl); serr != nil {
			logger.Warnf("ignoring invalid persisted log_level %q: %v", lvl, serr)
		}
	}

	// Admin bootstrap, applied only when no user exists.
	if cfg.AdminUser != "" {
		if n, err := users.Count(); err != nil {
			return err
		} else if n == 0 {
			hash, herr := auth.HashPassword(cfg.AdminPassword)
			if herr != nil {
				return herr
			}
			if _, cerr := users.Create(cfg.AdminUser, hash); cerr != nil {
				return cerr
			}
			logger.Infof("bootstrapped admin user %q", cfg.AdminUser)
		}
	}

	// Registry + detection + changelog + scan (read-only path).
	plat := registry.HostPlatform()
	resolver := registry.NewResolver(creds)
	cacheTTL := func() time.Duration { return settingDuration(settings, "cache_ttl_seconds", 10*time.Minute) }
	detector := detect.NewDetector(resolver, updates, images, states, events, tagCache, plat, cacheTTL)

	httpClient := &http.Client{Timeout: httpClientTTL}
	tokenFn := func() string {
		v, err := settings.GetSecret("github_token")
		if err != nil {
			return ""
		}
		return v
	}
	// Self-update check: latest stable dockbrr release from GitHub, cached 6h.
	// Reuses tokenFn (the changelog GitHub token) to lift the anonymous rate limit.
	selfUpdateChecker := selfupdate.NewChecker(httpClient, settings, version.Version, "https://api.github.com", 6*time.Hour, tokenFn)
	clResolver := changelog.NewResolver([]changelog.Source{
		changelog.NewGitHubSource(httpClient, "https://api.github.com", "https://raw.githubusercontent.com", tokenFn, changelogRepos, 24*time.Hour),
		changelog.NewRegistrySource(httpClient, "https://hub.docker.com"),
		changelog.NewOCISource(),
	})
	// Event bus: fans dashboard-refresh hints to SSE subscribers. Only main wires
	// it, so job/scan stay free of an httpapi import (no cycle).
	bus := httpapi.NewBus()
	scanner := scan.New(detector, clResolver, services, updates, images, states, func(id int64) {
		bus.Publish(httpapi.Event{Type: "detected", ServiceID: id})
	})

	// Job engine (mutation path). The handler is set only when Docker is reachable.
	engine := job.NewEngine(jobs, jobLogs, claimPoll)
	engine.OnFinish = func(id int64) { bus.Publish(httpapi.Event{Type: "job_finished", JobID: id}) }

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	testCancelMu.Lock()
	testCancel = stop
	testCancelMu.Unlock()
	defer stop()

	// Docker (non-fatal). When unreachable the server still boots and a
	// supervisor keeps dialing; the engine and discovery start as soon as the
	// daemon answers, so starting Docker after dockbrr needs no restart.
	var (
		wg sync.WaitGroup

		dcMu sync.Mutex // guards dc: written by the supervisor, read at shutdown
		dc   *docker.Client
	)

	// startDockerServices wires the mutation path onto a live client and blocks
	// until ctx ends. Called at most once, since the engine's worker pool and
	// the reconcile loop are single-start.
	startDockerServices := func(dc *docker.Client, discoveryReady chan<- struct{}) {
		healthTimeout := func() time.Duration { return settingDuration(settings, "health_timeout_seconds", 2*time.Minute) }
		healthPoll := func() time.Duration { return settingDuration(settings, "health_poll_seconds", 2*time.Second) }
		concurrency := settingInt(settings, "concurrency", 2) // worker pools don't resize live; start-time only

		locator := discovery.NewLocator(dc)
		applier := job.NewApplier(
			jobs, updates, services, projects, snapshots, events, settings,
			compose.NewExecRunner(), resolver, dc, locator, job.RealComposer{}, engine,
			plat, healthTimeout, healthPoll,
		)
		lifecycle := job.NewLifecycle(jobs, services, projects, events, dc, job.RealComposer{}, locator, engine)
		standalone := job.NewStandaloneApplier(
			jobs, updates, services, projects, snapshots, events,
			resolver, dc, plat, healthTimeout, healthPoll, engine,
		)
		dispatcher := job.NewDispatcher(applier, lifecycle, standalone, projects)
		if selfID := job.SelfContainerID(); selfID != "" {
			logger.Infof("job engine: self-guard armed (own container %s)", selfID)
			dispatcher.SetSelfGuard(selfID, services, jobs, engine)
		}

		// Self-update: pull the new image in-process, then a detached helper swaps
		// this container. Wired only when Docker is reachable (here). Clean up any
		// leftover helper from a prior (possibly failed) self-update first.
		dc.RemoveLeftoverUpdater(ctx)
		if selfID := job.SelfContainerID(); selfID != "" {
			dispatcher.SetSelfUpdater(job.NewSelfUpdater(jobs, engine, dc, selfUpdateChecker, selfID, cfg.DockerSocket))
		}

		engine.SetHandler(dispatcher)
		if n, rerr := engine.ResumeInterrupted(); rerr != nil {
			logger.Errorf("job engine: resume interrupted: %v", rerr)
		} else if n > 0 {
			logger.Infof("job engine: re-queued %d interrupted job(s)", n)
		}
		reconciler := discovery.NewReconciler(dc, projects, services, 1, settings, states)

		var inner sync.WaitGroup
		inner.Add(2)
		go func() { defer inner.Done(); engine.Start(ctx, concurrency) }()
		go func() { defer inner.Done(); reconcileLoop(ctx, reconciler, dc, bus, discoveryReady) }()
		inner.Wait()
	}

	// discoveryReady closes after discovery's first boot-time reconcile pass
	// (success or failure), letting the scheduler's scan_on_start wait for fresh
	// service rows instead of racing discovery on a cold boot. Never closed if
	// Docker never comes up; schedulerLoop bounds its wait on it separately.
	discoveryReady := make(chan struct{})

	bootClient, bootErr := dialDocker(ctx, cfg.DockerSocket)
	if bootErr != nil {
		logger.Warnf("docker unreachable: %v", bootErr)
		logger.Warnf("job engine idle + discovery disabled: retrying docker every %s", dockerRetryInterval)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		client := bootClient
		if client == nil {
			var ok bool
			client, ok = waitForDocker(ctx, dockerRetryInterval, func(ctx context.Context) (*docker.Client, error) {
				return dialDocker(ctx, cfg.DockerSocket)
			})
			if !ok {
				return // shutting down; Docker never came back
			}
			logger.Infof("docker reachable: starting job engine + discovery")
		}
		dcMu.Lock()
		dc = client
		dcMu.Unlock()
		startDockerServices(client, discoveryReady)
	}()

	// Dedicated liveness prober for /api/status, independent of the engine client
	// so the dashboard tile reflects Docker recovering or dying after boot rather
	// than a fixed boot-time snapshot. docker.New does not dial, so this succeeds
	// even when the daemon is down; the live Ping in the handler is what decides.
	var dockerProbe *docker.Client
	if p, perr := docker.New(cfg.DockerSocket); perr == nil {
		dockerProbe = p
		defer func() { _ = dockerProbe.Close() }()

		// Log daemon up/down edges so the log says when Docker came back, not just
		// that it was missing at boot. Edges only, steady state stays silent.
		wg.Add(1)
		go func() {
			defer wg.Done()
			watchDockerLiveness(ctx, dockerProbe, dockerProbeInterval, bootErr == nil,
				func(error) { logger.Infof("docker connection restored") },
				func(err error) { logger.Warnf("docker connection lost: %v", err) },
			)
		}()
	}

	// Scheduler: periodic read-only detection + gated auto-apply. Safe without
	// Docker (empty service set → no-op). First tick is one interval out, so a
	// short-lived boot (tests) performs no network I/O.
	//
	// nextScan carries the loop's next tick time (unix seconds, 0 = unset) out to
	// /api/status. It cannot be derived from last_check_all + poll_interval: a
	// manual scan stamps last_check_all without resetting the ticker.
	var nextScan atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		schedulerLoop(ctx, settings, scanner, services, projects, updates, engine, bus, &nextScan, discoveryReady)
	}()

	// Pruner: ages out finished job history. Store-only, no Docker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		pruneLoop(ctx, settings, jobs)
	}()

	// Session GC: ages out expired session rows. Store-only, no Docker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		sessionGCLoop(ctx, sessions)
	}()

	// HTTP server.
	deps := httpapi.Deps{
		Sealer: sealer, Users: users, Sessions: sessions, Credentials: creds,
		Settings: settings, Services: services, Projects: projects, Updates: updates,
		Events: events, Jobs: jobs, JobLogs: jobLogs, RemoteStates: states,
		Engine: engine, Checker: scanner, HostID: 1, Bus: bus,
		SelfUpdate: selfUpdateChecker,
		SelfID:     job.SelfContainerID(),
		StartedAt:  time.Now(),
		NextScan: func() time.Time {
			sec := nextScan.Load()
			if sec == 0 {
				return time.Time{}
			}
			return time.Unix(sec, 0).UTC()
		},
		LogConfig: httpapi.LogConfig{
			Path:       cfg.LogPath,
			MaxSizeMB:  cfg.LogMaxSizeMB,
			MaxBackups: cfg.LogMaxBackups,
		},
	}
	if dockerProbe != nil {
		deps.DockerPinger = dockerProbe
		deps.DockerVersion = dockerProbe.ServerVersion
		// DockerLogs reuses dockerProbe (not the supervisor-owned dc): docker.New
		// does not dial, so dockerProbe is available immediately at boot, and
		// unlike dc it is never mutated after construction, so no dcMu is needed
		// here. Using dc directly would race dcMu's writer and, worse, wrap a nil
		// *docker.Client in a non-nil DockerLogsReader before Docker first
		// connects, defeating the nil-Deps.DockerLogs -> 503 check in handleLogs.
		deps.DockerLogs = dockerProbe
	}
	srv := httpapi.New(cfg, db, deps)
	// BaseContext ties every request's context to the signal-cancelled ctx, so a
	// SIGINT/SIGTERM cancels in-flight requests too. Without it, long-lived SSE
	// streams (/api/events/stream, job logs) keep their request open and block
	// httpServer.Shutdown for the full graceful-timeout window: a browser tab
	// left open turns Ctrl-C into a 10s hang. Now their <-r.Context().Done()
	// fires immediately and Shutdown returns in milliseconds.
	httpServer := &http.Server{
		Addr:        cfg.BindAddr,
		Handler:     srv.Handler(),
		BaseContext: func(net.Listener) context.Context { return ctx },
		// Slowloris guard. No ReadTimeout/WriteTimeout: SSE streams
		// (/api/events/stream, job logs) are long-lived responses and a
		// blanket deadline would sever them; per-request cancellation comes
		// from BaseContext instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Infof("dockbrr %s listening on %s", version.Version, cfg.BindAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Warm the self-update cache off the request path (best-effort).
	go func() { _, _ = selfUpdateChecker.Check(ctx) }()

	// closeDocker runs only after wg.Wait: the supervisor owns dc until then.
	closeDocker := func() {
		dcMu.Lock()
		defer dcMu.Unlock()
		if dc != nil {
			_ = dc.Close()
		}
	}

	select {
	case err := <-errCh:
		stop()
		wg.Wait()
		closeDocker()
		return err
	case <-ctx.Done():
		wg.Wait()
		closeDocker()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutCtx)
	}
}

// reconcileLoop runs discovery immediately, then on every debounced Docker
// container event, and every syncInterval as a fallback. A reconcile publishes
// a "reconciled" refresh hint only when the discovered surface actually changed
// (Reconcile reports this), so idle cycles stay silent. If the
// Docker event stream dies (channel closes), events is set to nil so the
// select falls back to timer-only (a nil channel blocks forever).
//
// discoveryReady closes right after the first reconcile pass (success or
// failure) so schedulerLoop's scan_on_start can wait for fresh service rows
// instead of racing this boot-time reconcile. May be nil (tests).
func reconcileLoop(ctx context.Context, reconciler *discovery.Reconciler, dc *docker.Client, bus *httpapi.Bus, discoveryReady chan<- struct{}) {
	run := func() {
		changed, err := reconciler.Reconcile(ctx)
		if err != nil {
			logger.Errorf("discovery: reconcile error: %v", err)
		} else if changed {
			logger.Debugf("discovery: reconcile applied changes")
			bus.Publish(httpapi.Event{Type: "reconciled"})
		}
	}
	run()
	if discoveryReady != nil {
		close(discoveryReady)
	}
	events := debounce(ctx, dc.ContainerEvents(ctx), 2*time.Second)
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		case _, ok := <-events:
			if !ok {
				events = nil // stream died: fall back to timer-only (nil chan blocks forever)
				continue
			}
			run()
		}
	}
}

// debounce coalesces bursts on in: after a signal arrives, it waits d for the
// burst to settle, then emits exactly one signal on out. It terminates when
// ctx is cancelled OR when in is closed, closing out in both cases (no
// goroutine leak, and no busy-loop once the upstream channel is closed).
func debounce(ctx context.Context, in <-chan struct{}, d time.Duration) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-in:
				if !ok {
					return
				}
			}
			timer := time.NewTimer(d)
		settle:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case _, ok := <-in:
					if !ok {
						timer.Stop()
						return
					}
					if !timer.Stop() {
						<-timer.C
					}
					timer.Reset(d)
				case <-timer.C:
					break settle
				}
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out
}

// schedulerLoop drives detection + gated auto-apply on a ticker. With
// scan_on_start (default on) it also runs one check-and-apply pass as soon as
// it boots, so a fresh start doesn't show stale data (or an unapplied,
// already-eligible update) for a whole interval; turning it off restores the
// old behaviour of no boot-time network I/O. poll_interval_seconds is re-read
// after every tick and the ticker reset on change, so a live settings edit
// takes effect without a restart.
//
// discoveryReady is discovery's boot-reconcile-done signal (see reconcileLoop):
// scan_on_start waits on it, bounded by discoveryReadyTimeout, so a fresh boot
// after a stack recreate checks the just-reconciled service rows instead of
// racing discovery and silently scanning stale ones. May be nil (tests).
func schedulerLoop(ctx context.Context, settings *store.Settings, scanner *scan.Scanner,
	services *store.Services, projects *store.Projects, updates *store.Updates, engine *job.Engine, bus *httpapi.Bus,
	nextScan *atomic.Int64, discoveryReady <-chan struct{}) {
	interval := settingDuration(settings, "poll_interval_seconds", 15*time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	nextScan.Store(time.Now().Add(interval).Unix())

	// The boot scan also auto-applies: any project/service that already has
	// auto-update enabled (store.EffectiveAutoUpdate) gets its eligible update
	// applied immediately rather than waiting up to one poll interval. It's the
	// same gate the ticker uses, so this only changes behavior for projects a
	// user already opted into auto-update. It does not reset the ticker: the
	// next tick stays one full interval from boot, just with fresh data (and,
	// where configured, a fresh apply) in the meantime.
	if settings.GetBoolDefault("scan_on_start", true) {
		waitForDiscovery(ctx, discoveryReady, discoveryReadyTimeout)
		logger.Infof("scheduler: running startup check (scan_on_start)")
		runCheck(ctx, settings, scanner, bus)
		autoApply(services, projects, updates, engine)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logger.Infof("scheduler: running scheduled check")
			runCheck(ctx, settings, scanner, bus)
			autoApply(services, projects, updates, engine)
			if next := settingDuration(settings, "poll_interval_seconds", 15*time.Minute); next != interval {
				interval = next
				ticker.Reset(interval)
				logger.Infof("scheduler: poll interval now %s", interval)
			}
			nextScan.Store(time.Now().Add(interval).Unix())
		}
	}
}

// waitForDiscovery blocks until discovery's first boot reconcile completes
// (ready closes), ctx ends, or timeout elapses, whichever comes first. A nil
// ready (tests, or Docker never dialed) returns immediately. Bounded so a
// down/slow Docker daemon can't stall scan_on_start: it must still run when
// Docker is unreachable.
func waitForDiscovery(ctx context.Context, ready <-chan struct{}, timeout time.Duration) {
	if ready == nil {
		return
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ready:
	case <-ctx.Done():
	case <-t.C:
		logger.Warnf("scheduler: discovery not ready after %s, running scan_on_start against current store state", timeout)
	}
}

// runCheck is the read-only half of a scheduler pass: detect drift across every
// service, stamp last_check_all, tell the UI. Mutation (auto-apply) is a
// separate call the caller makes afterward; both the ticker and the boot
// scan do it.
func runCheck(ctx context.Context, settings *store.Settings, scanner *scan.Scanner, bus *httpapi.Bus) {
	if err := scanner.CheckAll(ctx); err != nil {
		logger.Errorf("scheduler: check-all: %v", err)
	}
	if err := settings.Set("last_check_all", time.Now().UTC().Format(time.RFC3339)); err != nil {
		logger.Errorf("scheduler: record last_check_all: %v", err)
	}
	// Push a refresh hint so the dashboard's "Last scan" tile updates
	// immediately instead of waiting for its 60s poll (or a page reload).
	bus.Publish(httpapi.Event{Type: "scanned"})
}

// pruneLoop ages out finished job history: it prunes once at boot, then every
// pruneInterval. job_retention_days is re-read on every run, so a settings edit
// takes effect without a restart; 0 (or negative) disables pruning entirely.
// Only terminal jobs are removed (queued/running ones belong to the worker),
// and their logs go with them via ON DELETE CASCADE.
func pruneLoop(ctx context.Context, settings *store.Settings, jobs *store.Jobs) {
	run := func() {
		days := settingInt(settings, "job_retention_days", defaultJobRetentionDays)
		if days <= 0 {
			return // retention disabled
		}
		n, err := jobs.DeleteFinishedBefore(time.Now().UTC().AddDate(0, 0, -days))
		if err != nil {
			logger.Errorf("pruner: delete finished jobs: %v", err)
			return
		}
		if n > 0 {
			logger.Infof("pruner: removed %d finished job(s) older than %d day(s)", n, days)
		}
	}
	run()
	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// sessionGCInterval: how often expired session rows are swept. Expiry is
// enforced authoritatively on read (Sessions.Get); this loop only stops dead
// rows accumulating in the DB, so precision doesn't matter.
const sessionGCInterval = time.Hour

// sessionGCLoop ages out expired sessions. Store-only, no Docker.
func sessionGCLoop(ctx context.Context, sessions *store.Sessions) {
	run := func() {
		n, err := sessions.DeleteExpired(time.Now().UTC())
		if err != nil {
			logger.Errorf("session gc: delete expired: %v", err)
			return
		}
		if n > 0 {
			logger.Infof("session gc: removed %d expired session(s)", n)
		}
	}
	run()
	ticker := time.NewTicker(sessionGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// autoApply enqueues an apply job for every service the effective auto-update
// gate lets through (project flag on, service not vetoing (nil inherits the
// project setting) and not genuinely digest-pinned, i.e. pinned && !drifted)
// and which has an open update. Enqueue is
// idempotent-enough: the per-project lock serializes, and the Applier precheck
// no-ops when there is no open update (a prior tick already applied it).
func autoApply(services *store.Services, projects *store.Projects, updates *store.Updates, engine *job.Engine) {
	svcs, err := services.List()
	if err != nil {
		logger.Errorf("scheduler: list services: %v", err)
		return
	}
	for i := range svcs {
		svc := svcs[i]
		proj, err := projects.Get(svc.ProjectID)
		if err != nil {
			continue
		}
		if proj.Unmanaged {
			continue
		}
		if !store.EffectiveAutoUpdate(proj, svc) {
			continue
		}
		if _, err := updates.GetLatestOpenByService(svc.ID); err != nil {
			continue // no open update
		}
		pid := svc.ProjectID
		sid := svc.ID
		if _, err := engine.Enqueue(store.Job{
			Type: "apply", ServiceID: &sid, ProjectID: &pid, Scope: "service", RequestedBy: "scheduler",
		}); err != nil {
			logger.Errorf("scheduler: enqueue auto-apply (service %d (%s)): %v", svc.ID, svc.Name, err)
		}
	}
}

// settingInt reads an integer setting, falling back to def.
func settingInt(s *store.Settings, key string, def int) int {
	v, err := s.Get(key)
	if err != nil || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// settingDuration reads a seconds-valued setting, falling back to def.
func settingDuration(s *store.Settings, key string, def time.Duration) time.Duration {
	secs := settingInt(s, key, -1)
	if secs < 0 {
		return def
	}
	return time.Duration(secs) * time.Second
}
