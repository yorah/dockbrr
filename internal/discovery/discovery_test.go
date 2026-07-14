package discovery_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/discovery"
	"dockbrr/internal/docker"
	"dockbrr/internal/secret"
	"dockbrr/internal/store"
)

// fakeCollector implements discovery.Collector for tests.
type fakeCollector struct {
	containers []docker.Container
	err        error
}

func (f *fakeCollector) Collect(_ context.Context) ([]docker.Container, error) {
	return f.containers, f.err
}

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newSettingsFor builds a *store.Settings backed by db (so it shares state
// with the projects/services repositories under test).
func newSettingsFor(t *testing.T, db *store.DB) *store.Settings {
	t.Helper()
	key, err := secret.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return store.NewSettings(db, sealer)
}

// ── Group tests ──────────────────────────────────────────────────────────────

func TestGroupTwoServicesOneProject(t *testing.T) {
	cs := []docker.Container{
		{
			ID:          "c1",
			Project:     "myapp",
			Service:     "web",
			WorkingDir:  "/srv/myapp",
			ConfigFiles: []string{"docker-compose.yml"},
			Name:        "myapp_web_1",
			ImageRef:    "nginx:latest",
			RepoDigest:  "sha256:abc",
			ImageID:     "sha256:def",
			State:       "running",
		},
		{
			ID:          "c2",
			Project:     "myapp",
			Service:     "db",
			WorkingDir:  "/srv/myapp",
			ConfigFiles: []string{"docker-compose.yml"},
			Name:        "myapp_db_1",
			ImageRef:    "postgres:15",
			RepoDigest:  "sha256:ghi",
			ImageID:     "sha256:jkl",
			State:       "running",
		},
	}

	groups := discovery.Group(cs)
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	p := groups[0]
	if p.Name != "myapp" {
		t.Fatalf("name = %q, want myapp", p.Name)
	}
	if p.Kind != "compose" {
		t.Fatalf("kind = %q, want compose", p.Kind)
	}
	if p.WorkingDir != "/srv/myapp" {
		t.Fatalf("working_dir = %q, want /srv/myapp", p.WorkingDir)
	}
	if len(p.ConfigFiles) != 1 || p.ConfigFiles[0] != "docker-compose.yml" {
		t.Fatalf("config_files = %v", p.ConfigFiles)
	}
	if len(p.Services) != 2 {
		t.Fatalf("len(services) = %d, want 2", len(p.Services))
	}
	// Services must be sorted by name: db < web
	if p.Services[0].Name != "db" || p.Services[1].Name != "web" {
		t.Fatalf("service names = [%q %q], want [db web]", p.Services[0].Name, p.Services[1].Name)
	}
}

func TestGroupReplicasMerge(t *testing.T) {
	cs := []docker.Container{
		{ID: "r1", Project: "stack", Service: "worker", Name: "stack_worker_1", ImageRef: "worker:1", RepoDigest: "sha256:aaa", ImageID: "sha256:bbb", State: "running"},
		{ID: "r2", Project: "stack", Service: "worker", Name: "stack_worker_2", ImageRef: "worker:1", State: "running"},
	}

	groups := discovery.Group(cs)
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	if len(groups[0].Services) != 1 {
		t.Fatalf("len(services) = %d, want 1 (replicas must merge)", len(groups[0].Services))
	}
	svc := groups[0].Services[0]
	if len(svc.ContainerIDs) != 2 {
		t.Fatalf("container_ids = %v, want 2 ids", svc.ContainerIDs)
	}
	// First container's fields should be used.
	if svc.ImageRef != "worker:1" {
		t.Fatalf("image_ref = %q", svc.ImageRef)
	}
	if svc.CurrentDigest != "sha256:aaa" {
		t.Fatalf("current_digest = %q, want sha256:aaa", svc.CurrentDigest)
	}
	if svc.CurrentImageID != "sha256:bbb" {
		t.Fatalf("current_image_id = %q, want sha256:bbb", svc.CurrentImageID)
	}
}

func TestGroupStandalone(t *testing.T) {
	cs := []docker.Container{
		{
			ID:       "s1",
			Project:  "", // standalone
			Name:     "mycontainer",
			ImageRef: "redis:7",
			State:    "running",
		},
	}

	groups := discovery.Group(cs)
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	p := groups[0]
	if p.Kind != "standalone" {
		t.Fatalf("kind = %q, want standalone", p.Kind)
	}
	if p.Name != "mycontainer" {
		t.Fatalf("name = %q, want mycontainer", p.Name)
	}
	if len(p.Services) != 1 {
		t.Fatalf("len(services) = %d, want 1", len(p.Services))
	}
	if p.Services[0].Name != "mycontainer" {
		t.Fatalf("service name = %q, want mycontainer", p.Services[0].Name)
	}
	if p.Services[0].ImageRef != "redis:7" {
		t.Fatalf("service image_ref = %q, want redis:7", p.Services[0].ImageRef)
	}
	if len(p.Services[0].ContainerIDs) != 1 || p.Services[0].ContainerIDs[0] != "s1" {
		t.Fatalf("container_ids = %v", p.Services[0].ContainerIDs)
	}
}

func TestGroupComposeEmptyServiceSkipped(t *testing.T) {
	// A compose container (Project set, Service empty) must not produce a
	// DiscoveredService with Name == "".
	cs := []docker.Container{
		{ID: "c1", Project: "myapp", Service: "", Name: "myapp_1", ImageRef: "nginx:latest", State: "running"},
	}
	groups := discovery.Group(cs)
	for _, g := range groups {
		for _, svc := range g.Services {
			if svc.Name == "" {
				t.Fatalf("Group produced a service with empty Name in project %q", g.Name)
			}
		}
	}
}

func TestGroupDeterministicOrder(t *testing.T) {
	// Multiple projects and services in reverse alphabetical order as input.
	cs := []docker.Container{
		{ID: "c3", Project: "zebra", Service: "svc-z", Name: "zebra_svc-z_1", ImageRef: "img:z", State: "running"},
		{ID: "c2", Project: "alpha", Service: "svc-b", Name: "alpha_svc-b_1", ImageRef: "img:b", State: "running"},
		{ID: "c1", Project: "alpha", Service: "svc-a", Name: "alpha_svc-a_1", ImageRef: "img:a", State: "running"},
	}

	groups := discovery.Group(cs)
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2", len(groups))
	}
	if groups[0].Name != "alpha" || groups[1].Name != "zebra" {
		t.Fatalf("project order = [%q %q], want [alpha zebra]", groups[0].Name, groups[1].Name)
	}
	if groups[0].Services[0].Name != "svc-a" || groups[0].Services[1].Name != "svc-b" {
		t.Fatalf("service order = [%q %q], want [svc-a svc-b]", groups[0].Services[0].Name, groups[0].Services[1].Name)
	}
}

// ── Reconcile tests ──────────────────────────────────────────────────────────

func TestReconcilePopulatesStore(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID:          "c1",
				Project:     "app",
				Service:     "web",
				WorkingDir:  "/srv/app",
				ConfigFiles: []string{"docker-compose.yml"},
				Name:        "app_web_1",
				ImageRef:    "nginx:latest",
				RepoDigest:  "sha256:abc",
				ImageID:     "sha256:def",
				State:       "running",
				Healthcheck: true,
			},
			{
				ID:       "s1",
				Project:  "",
				Name:     "redis",
				ImageRef: "redis:7",
				State:    "running",
			},
		},
	}

	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("project count = %d, want 2", len(all))
	}
	// Projects ordered by name: app < redis
	if all[0].Name != "app" || all[0].Kind != "compose" {
		t.Fatalf("project[0] = {Name:%q Kind:%q}", all[0].Name, all[0].Kind)
	}
	if all[0].Source != "discovered" {
		t.Fatalf("project[0].Source = %q, want discovered", all[0].Source)
	}
	if all[0].LastSyncedAt == nil {
		t.Fatal("project[0].LastSyncedAt is nil, want non-nil")
	}
	if all[1].Name != "redis" || all[1].Kind != "standalone" {
		t.Fatalf("project[1] = {Name:%q Kind:%q}", all[1].Name, all[1].Kind)
	}

	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count for app = %d, want 1", len(svcs))
	}
	svc := svcs[0]
	if svc.Name != "web" {
		t.Fatalf("service name = %q, want web", svc.Name)
	}
	if svc.CurrentDigest != "sha256:abc" {
		t.Fatalf("current_digest = %q, want sha256:abc", svc.CurrentDigest)
	}
	if svc.CurrentImageID != "sha256:def" {
		t.Fatalf("current_image_id = %q", svc.CurrentImageID)
	}
	if !svc.Healthcheck {
		t.Fatal("healthcheck = false, want true")
	}
	if svc.AutoUpdateEnabled != nil {
		t.Fatalf("auto_update_enabled = %v, want nil (discovery must not set it)", svc.AutoUpdateEnabled)
	}

	// Standalone redis service.
	redisSvcs, err := services.ListByProject(all[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(redisSvcs) != 1 || redisSvcs[0].Name != "redis" {
		t.Fatalf("redis service = %+v", redisSvcs)
	}
}

func TestReconcileNewProjectRespectsDefaultAutoUpdateSetting(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db)
	if err := settings.Set("default_auto_update_enabled", "true"); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID:          "c1",
				Project:     "app",
				Service:     "web",
				WorkingDir:  "/srv/app",
				ConfigFiles: []string{"docker-compose.yml"},
				Name:        "app_web_1",
				ImageRef:    "nginx:latest",
				State:       "running",
			},
		},
	}

	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "app" {
		t.Fatalf("projects = %+v", all)
	}
	if !all[0].AutoUpdateEnabled {
		t.Fatal("AutoUpdateEnabled = false, want true (default_auto_update_enabled=true must apply to a brand-new discovered project)")
	}
}

func TestReconcileNewProjectDefaultsAutoUpdateOffWhenUnset(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db) // default_auto_update_enabled left unset

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID:          "c1",
				Project:     "app",
				Service:     "web",
				WorkingDir:  "/srv/app",
				ConfigFiles: []string{"docker-compose.yml"},
				Name:        "app_web_1",
				ImageRef:    "nginx:latest",
				State:       "running",
			},
		},
	}

	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("projects = %+v", all)
	}
	if all[0].AutoUpdateEnabled {
		t.Fatal("AutoUpdateEnabled = true, want false (shipped default must stay off)")
	}
}

func TestReconcileMarksGone(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	fc := &fakeCollector{
		containers: []docker.Container{
			{ID: "c1", Project: "app", Service: "svc-a", Name: "app_svc-a_1", ImageRef: "img:1", State: "running"},
			{ID: "c2", Project: "app", Service: "svc-b", Name: "app_svc-b_1", ImageRef: "img:2", State: "running"},
		},
	}

	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Second reconcile: only svc-a present.
	fc.containers = []docker.Container{
		{ID: "c1", Project: "app", Service: "svc-a", Name: "app_svc-a_1", ImageRef: "img:1", State: "running"},
	}
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("project count = %d, want 1", len(all))
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 {
		t.Fatalf("service count = %d, want 2", len(svcs))
	}
	// Sorted by name: svc-a, svc-b
	if svcs[0].Name != "svc-a" {
		t.Fatalf("svcs[0].Name = %q, want svc-a", svcs[0].Name)
	}
	if svcs[0].State != "running" {
		t.Fatalf("svc-a state = %q, want running", svcs[0].State)
	}
	if svcs[1].Name != "svc-b" {
		t.Fatalf("svcs[1].Name = %q, want svc-b", svcs[1].Name)
	}
	if svcs[1].State != "gone" {
		t.Fatalf("svc-b state = %q, want gone", svcs[1].State)
	}
}

func TestReconcileManualProjectUntouched(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	// Pre-insert a manual project + service.
	pid, err := projects.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "manual-app", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = services.Upsert(store.Service{
		ProjectID:    pid,
		Name:         "api",
		ImageRef:     "myapi:1",
		State:        "running",
		ContainerIDs: []string{"m1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reconcile with empty discovery.
	fc := &fakeCollector{containers: nil}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	svcs, err := services.ListByProject(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1", len(svcs))
	}
	if svcs[0].State == "gone" {
		t.Fatal("manual service state = gone, want untouched (not gone)")
	}
}

func TestReconcileSkipsManualNameCollision(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	// Pre-insert a manual project named "web" with WorkingDir "/manual".
	pid, err := projects.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web", Source: "manual", WorkingDir: "/manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = services.Upsert(store.Service{
		ProjectID:    pid,
		Name:         "manualsvc",
		ImageRef:     "myimg:1",
		State:        "running",
		ContainerIDs: []string{"m1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Discovered group also named "web" but with different WorkingDir and service.
	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID:         "d1",
				Project:    "web",
				Service:    "app",
				WorkingDir: "/discovered",
				Name:       "web_app_1",
				ImageRef:   "nginx:latest",
				State:      "running",
			},
		},
	}

	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The "web" project ROW must still be manual and must not have its
	// WorkingDir overwritten by the discovered group's metadata.
	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("project count = %d, want 1 (no new discovered project should be created)", len(all))
	}
	p := all[0]
	if p.Source != "manual" {
		t.Fatalf("project source = %q, want manual", p.Source)
	}
	if p.WorkingDir != "/manual" {
		t.Fatalf("project working_dir = %q, want /manual (must not be overwritten by discovered)", p.WorkingDir)
	}

	// The manual project's SERVICES are refreshed from the live containers
	// (this is the new behavior: reconciler no longer skips manual projects
	// wholesale, only their project row). The pre-existing manual service
	// that has no matching container is left untouched (not marked gone,
	// mark-gone only runs for source=="discovered" projects).
	svcs, err := services.ListByProject(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 {
		t.Fatalf("service count = %d, want 2 (manualsvc untouched + app refreshed from discovery)", len(svcs))
	}
	// Sorted by name: app < manualsvc
	if svcs[0].Name != "app" {
		t.Fatalf("svcs[0].Name = %q, want app", svcs[0].Name)
	}
	if svcs[0].ImageRef != "nginx:latest" || len(svcs[0].ContainerIDs) != 1 || svcs[0].ContainerIDs[0] != "d1" {
		t.Errorf("discovered service under manual project not refreshed: %+v", svcs[0])
	}
	if svcs[1].Name != "manualsvc" {
		t.Fatalf("svcs[1].Name = %q, want manualsvc", svcs[1].Name)
	}
	if svcs[1].State == "gone" {
		t.Fatal("manual service state = gone, want untouched")
	}
}

func TestReconcileRefreshesServicesInsideManualProjects(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	// Pre-insert a manual project "stack" with a seeded service "web" that has
	// no runtime data yet (as handleCreateProject seeds it: empty digest, no
	// container ids), mirrors part (a) of this task.
	manualProjectID, err := projects.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "stack", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.Upsert(store.Service{
		ProjectID: manualProjectID,
		Name:      "web",
		ImageRef:  "nginx:1.27",
	}); err != nil {
		t.Fatal(err)
	}

	// A running container now exists for that manual project's "web" service.
	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID:         "c1",
				Project:    "stack",
				Service:    "web",
				WorkingDir: "/srv/stack",
				Name:       "stack_web_1",
				ImageRef:   "nginx:1.27",
				RepoDigest: "sha256:live",
				State:      "running",
			},
		},
	}

	reconciler := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Assert 1: the service row got runtime data.
	svcs, err := services.ListByProject(manualProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1", len(svcs))
	}
	if svcs[0].CurrentDigest != "sha256:live" || len(svcs[0].ContainerIDs) != 1 {
		t.Errorf("manual project's service not refreshed: %+v", svcs[0])
	}

	// Assert 2: the manual project ROW is untouched (still source=manual).
	p, err := projects.Get(manualProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "manual" {
		t.Errorf("manual project row must stay manual, got %q", p.Source)
	}
}

// ── drift detection ──────────────────────────────────────────────────────────

func writeComposeFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReconcileDriftNotDriftedWhenImageMatchesDeclared(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	dir := t.TempDir()
	cfgPath := writeComposeFile(t, dir, "services:\n  web:\n    image: nginx:1.31\n")

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID: "c1", Project: "app", Service: "web",
				WorkingDir: dir, ConfigFiles: []string{cfgPath},
				Name: "app_web_1", ImageRef: "nginx:1.31", State: "running",
			},
		},
	}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1", len(svcs))
	}
	if svcs[0].Drifted {
		t.Fatal("Drifted = true, want false (declared matches running)")
	}
}

func TestReconcileDriftDetectedWhenImageDiffersFromDeclared(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	dir := t.TempDir()
	cfgPath := writeComposeFile(t, dir, "services:\n  web:\n    image: nginx:1.31.2\n")

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID: "c1", Project: "app", Service: "web",
				WorkingDir: dir, ConfigFiles: []string{cfgPath},
				Name: "app_web_1", ImageRef: "nginx:1.31.2@sha256:x", State: "running",
			},
		},
	}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1", len(svcs))
	}
	if !svcs[0].Drifted {
		t.Fatal("Drifted = false, want true (running ref carries a digest the file doesn't declare)")
	}
}

func TestReconcileDriftStandaloneNeverDrifted(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	fc := &fakeCollector{
		containers: []docker.Container{
			{ID: "s1", Project: "", Name: "redis", ImageRef: "redis:7@sha256:y", State: "running"},
		},
	}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1", len(svcs))
	}
	if svcs[0].Drifted {
		t.Fatal("Drifted = true, want false (standalone service has no compose declaration)")
	}
}

func TestReconcileDriftOddNameDriftedWhenImageDiffersFromDeclared(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	dir := t.TempDir()
	cfgPath := writeComposeFile(t, dir, "services:\n  web_app_db:\n    image: postgres:15\n")

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID: "c1", Project: "app", Service: "web_app_db",
				WorkingDir: dir, ConfigFiles: []string{cfgPath},
				Name: "app_web_app_db_1", ImageRef: "postgres:16", State: "running",
			},
		},
	}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1", len(svcs))
	}
	if !svcs[0].Drifted {
		t.Fatal("Drifted = false, want true (running ref differs from declared for service with underscore in name)")
	}
}

func TestReconcileDriftOddNameNotDriftedWhenImageMatchesDeclared(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	dir := t.TempDir()
	cfgPath := writeComposeFile(t, dir, "services:\n  web_app_db:\n    image: postgres:15\n")

	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID: "c1", Project: "app", Service: "web_app_db",
				WorkingDir: dir, ConfigFiles: []string{cfgPath},
				Name: "app_web_app_db_1", ImageRef: "postgres:15", State: "running",
			},
		},
	}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1", len(svcs))
	}
	if svcs[0].Drifted {
		t.Fatal("Drifted = true, want false (declared matches running for service with underscore in name)")
	}
}

// ── goneServiceIDs pure unit test ────────────────────────────────────────────

// ── unmanaged detection ──────────────────────────────────────────────────────

func TestReconcileFlagsUnmanagedWhenConfigFilesMissing(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	missingPath := filepath.Join(t.TempDir(), "docker-compose.yml")
	fc := &fakeCollector{
		containers: []docker.Container{
			{
				ID: "c1", Project: "app", Service: "web",
				WorkingDir: filepath.Dir(missingPath), ConfigFiles: []string{missingPath},
				Name: "app_web_1", ImageRef: "nginx:latest", State: "running",
			},
		},
	}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].Unmanaged {
		t.Fatalf("project = %+v, want Unmanaged=true", all[0])
	}

	// Create the file, next reconcile clears the flag.
	if err := os.WriteFile(missingPath, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	all, err = projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Unmanaged {
		t.Fatalf("project = %+v, want Unmanaged=false after file created", all[0])
	}
}

func TestReconcileStandaloneNeverUnmanaged(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	fc := &fakeCollector{
		containers: []docker.Container{
			{ID: "s1", Project: "", Name: "redis", ImageRef: "redis:7", State: "running"},
		},
	}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Kind != "standalone" {
		t.Fatalf("project = %+v, want standalone", all[0])
	}
	if all[0].Unmanaged {
		t.Fatal("standalone project must never be flagged unmanaged")
	}
}

func TestGoneServiceIDs(t *testing.T) {
	stored := []store.Service{
		{ID: 1, Name: "a"},
		{ID: 2, Name: "b"},
		{ID: 3, Name: "c"},
	}
	present := map[string]bool{"a": true, "c": true}

	got := discovery.GoneServiceIDs(stored, present)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("GoneServiceIDs = %v, want [2]", got)
	}
}

func TestReconcileReportsChangedOnlyOnRealChange(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	fc := &fakeCollector{containers: []docker.Container{
		{ID: "c1", Project: "app", Service: "web", ConfigFiles: []string{"docker-compose.yml"},
			Name: "app_web_1", ImageRef: "nginx:latest", RepoDigest: "sha256:abc", State: "running"},
	}}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)

	must := func(wantChanged bool, label string) {
		t.Helper()
		changed, err := r.Reconcile(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if changed != wantChanged {
			t.Fatalf("%s: changed=%v, want %v", label, changed, wantChanged)
		}
	}

	must(true, "first reconcile")             // nothing → something
	must(false, "idle re-reconcile")          // identical surface → no hint
	fc.containers[0].RepoDigest = "sha256:xyz" // image digest moved
	must(true, "digest changed")
	must(false, "idle after digest change")
	fc.containers = nil // container vanished → service goes gone
	must(true, "container removed")
	must(false, "idle after removal")
}

// ── detect-cache invalidation on recreate (running digest change) ───────────

func TestReconcileInvalidatesRemoteStateOnDigestChange(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	states := store.NewRemoteStates(db)

	if err := states.Upsert(store.RemoteState{Repo: "nginx", Tag: "latest", RemoteDigest: "sha256:cached", Status: "ok"}); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{containers: []docker.Container{
		{ID: "c1", Project: "app", Service: "web", Name: "app_web_1", ImageRef: "nginx:latest", RepoDigest: "sha256:abc", State: "running"},
	}}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, states)

	// First reconcile: brand-new service (no prior stored digest) must NOT invalidate.
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := states.Get("nginx", "latest"); err != nil {
		t.Fatalf("after first reconcile (new service): Get = %v, want cached row still present", err)
	}

	// Second reconcile with the SAME digest must NOT invalidate.
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := states.Get("nginx", "latest"); err != nil {
		t.Fatalf("after unchanged-digest reconcile: Get = %v, want cached row still present", err)
	}

	// Third reconcile with a DIFFERENT digest (recreate) must invalidate.
	fc.containers[0].RepoDigest = "sha256:xyz"
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := states.Get("nginx", "latest"); !errors.Is(err, store.ErrRemoteStateNotFound) {
		t.Fatalf("after digest-change reconcile: Get err = %v, want ErrRemoteStateNotFound", err)
	}
}

func TestReconcileNilStatesDisablesInvalidation(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	fc := &fakeCollector{containers: []docker.Container{
		{ID: "c1", Project: "app", Service: "web", Name: "app_web_1", ImageRef: "nginx:latest", RepoDigest: "sha256:abc", State: "running"},
	}}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.containers[0].RepoDigest = "sha256:xyz"
	// Must not panic with nil states, and must complete successfully.
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileInvalidatesRemoteStateOnDigestChangeInManualProject(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	states := store.NewRemoteStates(db)

	pid, err := projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "manual-app", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.Upsert(store.Service{
		ProjectID: pid, Name: "api", ImageRef: "myapi:1", CurrentDigest: "sha256:old",
		State: "running", ContainerIDs: []string{"m1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := states.Upsert(store.RemoteState{Repo: "myapi", Tag: "1", RemoteDigest: "sha256:cached", Status: "ok"}); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{containers: []docker.Container{
		{ID: "m1", Project: "manual-app", Service: "api", Name: "manual-app_api_1", ImageRef: "myapi:1", RepoDigest: "sha256:new", State: "running"},
	}}
	r := discovery.NewReconciler(fc, projects, services, 1, nil, states)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := states.Get("myapi", "1"); !errors.Is(err, store.ErrRemoteStateNotFound) {
		t.Fatalf("manual project recreate: Get err = %v, want ErrRemoteStateNotFound", err)
	}
}

// ── auto-prune (gone service + empty project) ───────────────────────────────

func TestReconcileAutoPrunesGoneServicePastGraceAndEmptyProject(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db)
	if err := settings.Set("gone_grace_seconds", "60"); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{containers: []docker.Container{
		{ID: "c1", Project: "app", Service: "svc-a", Name: "app_svc-a_1", ImageRef: "img:1", State: "running"},
	}}
	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Container vanishes -> service goes gone.
	fc.containers = nil
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("project count = %d, want 1", len(all))
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].State != "gone" {
		t.Fatalf("expected exactly 1 gone service, got %+v", svcs)
	}

	// Back-date gone_since past the 60s grace window (deterministic, no sleep).
	past := time.Now().UTC().Add(-2 * time.Minute)
	if _, err := db.Exec(`UPDATE services SET gone_since=? WHERE id=?`, past, svcs[0].ID); err != nil {
		t.Fatal(err)
	}

	changed, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true (a prune deletion must report a change)")
	}

	remainingSvcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingSvcs) != 0 {
		t.Fatalf("service count = %d, want 0 (gone service past grace must be hard-deleted)", len(remainingSvcs))
	}

	remainingProjects, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingProjects) != 0 {
		t.Fatalf("project count = %d, want 0 (discovered project left empty must be pruned)", len(remainingProjects))
	}
}

func TestReconcileGoneWithinGraceSurvives(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db)
	if err := settings.Set("gone_grace_seconds", "3600"); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{containers: []docker.Container{
		{ID: "c1", Project: "app", Service: "svc-a", Name: "app_svc-a_1", ImageRef: "img:1", State: "running"},
	}}
	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	fc.containers = nil
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err) // marks gone; gone_since is set to "now" by MarkGone
	}

	// Reconcile again without back-dating: gone_since is fresh, well inside the
	// 1h grace window. The gone service (and its project) must survive.
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("project count = %d, want 1 (must survive within grace)", len(all))
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1 (gone-within-grace service must survive)", len(svcs))
	}
}

func TestReconcileAutoRemoveGoneDisabledKeepsAll(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db)
	if err := settings.Set("auto_remove_gone", "false"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set("gone_grace_seconds", "60"); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{containers: []docker.Container{
		{ID: "c1", Project: "app", Service: "svc-a", Name: "app_svc-a_1", ImageRef: "img:1", State: "running"},
	}}
	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	fc.containers = nil
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatal("service should exist before back-dating")
	}

	// Back-date well past the grace window; with auto_remove_gone=false this
	// must have no effect at all.
	past := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := db.Exec(`UPDATE services SET gone_since=? WHERE id=?`, past, svcs[0].ID); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	remainingProjects, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingProjects) != 1 {
		t.Fatalf("project count = %d, want 1 (auto_remove_gone=false must preserve everything)", len(remainingProjects))
	}
	remainingSvcs, err := services.ListByProject(remainingProjects[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingSvcs) != 1 {
		t.Fatalf("service count = %d, want 1 (auto_remove_gone=false must keep the gone service)", len(remainingSvcs))
	}
}

func TestReconcileManualEmptyProjectNeverPruned(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db)
	if err := settings.Set("gone_grace_seconds", "60"); err != nil {
		t.Fatal(err)
	}

	// Manual project with zero services (e.g. all of its services were removed
	// out-of-band). Even though it's empty, it must never be auto-deleted;
	// only discovered projects are eligible for empty-project pruning.
	if _, err := projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "manual-app", Source: "manual"}); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{containers: nil}
	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Source != "manual" {
		t.Fatalf("manual empty project must never be auto-pruned, got %+v", all)
	}
}

// TestReconcileNegativeGraceClampedNotPushedIntoFuture guards against a
// negative gone_grace_seconds pushing the prune cutoff into the future
// (now - negative = future), which would sweep up every gone service
// including ones that only just went gone.
//
// A real gone_since is always <= "now" at the instant it is recorded, so a
// realistic "just went gone" timestamp would be deleted under *any*
// non-positive grace (clamped-to-0 included) the moment any wall-clock time
// elapses, that's expected: 0 grace legitimately means "no grace period".
// The bug this guards is specifically that a negative grace lets the cutoff
// exceed "now" at all. To make that assertion deterministic (not racing the
// clock), we plant gone_since slightly *ahead* of "now": under the bug,
// cutoff = now+100s comfortably clears it (deleted); once clamped to 0,
// cutoff = now, which this service's gone_since is never before (survives).
func TestReconcileNegativeGraceClampedNotPushedIntoFuture(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)

	// Establish the gone service with pruning disabled (nil settings), so we
	// control exactly when the negative-grace prune pass runs and against
	// what gone_since value, isolating the cutoff-computation bug from the
	// unrelated (and separately covered) "0 grace deletes immediately" case.
	fc := &fakeCollector{containers: []docker.Container{
		{ID: "c1", Project: "app", Service: "svc-a", Name: "app_svc-a_1", ImageRef: "img:1", State: "running"},
	}}
	setupR := discovery.NewReconciler(fc, projects, services, 1, nil, nil)
	if _, err := setupR.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.containers = nil
	if _, err := setupR.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("project count = %d, want 1", len(all))
	}
	svcs, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].State != "gone" {
		t.Fatalf("expected exactly 1 gone service, got %+v", svcs)
	}

	// Plant gone_since just ahead of "now" (see comment above) so the
	// clamp-vs-no-clamp difference is deterministic.
	justAhead := time.Now().UTC().Add(30 * time.Second)
	if _, err := db.Exec(`UPDATE services SET gone_since=? WHERE id=?`, justAhead, svcs[0].ID); err != nil {
		t.Fatal(err)
	}

	// Now enable pruning with the negative grace and reconcile once more.
	settings := newSettingsFor(t, db)
	if err := settings.Set("gone_grace_seconds", "-100"); err != nil {
		t.Fatal(err)
	}
	pruneR := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := pruneR.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	remaining, err := services.ListByProject(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("service count = %d, want 1 (negative grace must be clamped to 0, not pushed into the future)", len(remaining))
	}

	remainingProjects, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingProjects) != 1 {
		t.Fatalf("project count = %d, want 1 (must survive alongside its only service)", len(remainingProjects))
	}
}

// TestReconcileGoneServiceInManualProjectNeverAutoDeleted is defense-in-depth
// for the prune pass's service-delete loop: manual services can never
// actually reach state="gone" through Reconcile (mark-gone only runs for
// source=="discovered" projects), so this scenario is constructed directly
// via SQL. It documents and guards the invariant that the delete loop is
// gated by p.Source == "discovered", matching the existing empty-project
// deletion gate, so a manual project's services are never hard-deleted even
// if they somehow end up gone+stale.
func TestReconcileGoneServiceInManualProjectNeverAutoDeleted(t *testing.T) {
	db := openDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	settings := newSettingsFor(t, db)
	if err := settings.Set("gone_grace_seconds", "60"); err != nil {
		t.Fatal(err)
	}

	pid, err := projects.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "manual-app", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	svcID, err := services.Upsert(store.Service{
		ProjectID:    pid,
		Name:         "api",
		ImageRef:     "myapi:1",
		State:        "running",
		ContainerIDs: []string{"m1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Directly force the service into a gone+past-grace state. This can
	// never happen through normal Reconcile flow for a manual project, but
	// simulates it to prove the delete loop wouldn't touch it even if it did.
	past := time.Now().UTC().Add(-2 * time.Minute)
	if _, err := db.Exec(`UPDATE services SET state='gone', gone_since=? WHERE id=?`, past, svcID); err != nil {
		t.Fatal(err)
	}

	fc := &fakeCollector{containers: nil}
	r := discovery.NewReconciler(fc, projects, services, 1, settings, nil)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	svcs, err := services.ListByProject(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("service count = %d, want 1 (manual project's gone+stale service must never be auto-deleted)", len(svcs))
	}

	all, err := projects.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Source != "manual" {
		t.Fatalf("manual project must survive, got %+v", all)
	}
}
