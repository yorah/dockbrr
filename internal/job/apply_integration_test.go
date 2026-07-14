package job_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/compose"
	"dockbrr/internal/discovery"
	"dockbrr/internal/docker"
	"dockbrr/internal/job"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// TestRealApplyRecreatesAndRecordsSuccess proves the Phase-6 hard prerequisite:
// against REAL docker, `compose up` recreates the service container with a NEW
// id, and the health gate, polling the RE-DISCOVERED ids, passes so the apply
// is recorded success (not failed), with services.current_digest refreshed.
//
// Skipped unless DOCKBRR_DOCKER_IT=1 and a working `docker compose` is present.
func TestRealApplyRecreatesAndRecordsSuccess(t *testing.T) {
	if os.Getenv("DOCKBRR_DOCKER_IT") != "1" {
		t.Skip("set DOCKBRR_DOCKER_IT=1 to run the real-docker apply test")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if out, err := exec.Command("docker", "compose", "version").CombinedOutput(); err != nil {
		t.Skipf("docker compose unavailable: %v (%s)", err, out)
	}

	const (
		oldImage = "busybox:1.36"
		newImage = "busybox:1.37"
	)
	dir := t.TempDir()
	composePath := filepath.Join(dir, "compose.yml")
	writeCompose := func(image string) {
		body := "services:\n  app:\n    image: " + image + "\n    command: [\"sleep\", \"3600\"]\n"
		if err := os.WriteFile(composePath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run := func(args ...string) {
		cmd := exec.Command("docker", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
	}

	// 1) Bring up the OLD image.
	writeCompose(oldImage)
	run("compose", "-f", composePath, "--project-directory", dir, "up", "-d")
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-f", composePath, "--project-directory", dir, "down", "-v").Run()
	})

	dc, err := docker.NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()

	// 2) Discover the running project+service (project name = compose dir base).
	locator := discovery.NewLocator(dc)
	cs, err := dc.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var projectName, oldDigest string
	var oldIDs []string
	for _, g := range discovery.Group(cs) {
		for _, s := range g.Services {
			if s.Name == "app" && len(s.ContainerIDs) > 0 {
				projectName, oldDigest, oldIDs = g.Name, s.CurrentDigest, s.ContainerIDs
			}
		}
	}
	if projectName == "" {
		t.Fatal("did not discover the app service")
	}

	// 3) Point the compose file at the NEW image and resolve its remote digest.
	writeCompose(newImage)
	resolver := registry.NewResolver(nil)
	remote, err := resolver.Resolve(context.Background(), newImage, registry.HostPlatform())
	if err != nil {
		t.Fatalf("resolve %s: %v", newImage, err)
	}

	// 4) Seed the store: project, service (current = old), open update (to = new).
	db, err := store.Open(filepath.Join(t.TempDir(), "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pid, _ := store.NewProjects(db).Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: projectName, Source: "discovered",
		WorkingDir: dir, ConfigFiles: []string{composePath},
	})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: newImage,
		ContainerIDs: oldIDs, CurrentDigest: oldDigest, State: "running",
	})
	uid, _ := store.NewUpdates(db).Upsert(store.Update{
		ServiceID: sid, ToDigest: remote.Digest, Tag: "1.37", Status: "available",
	})
	_ = uid

	// 5) Build the real apply stack + engine (as Emitter) and run the job.
	jobs := store.NewJobs(db)
	engine := job.NewEngine(jobs, store.NewJobLogs(db), 50*time.Millisecond)
	applier := job.NewApplier(
		jobs, store.NewUpdates(db), store.NewServices(db), store.NewProjects(db),
		store.NewSnapshots(db), store.NewEvents(db), store.NewSettings(db, nil),
		compose.NewExecRunner(), resolver, dc, locator, job.RealComposer{}, engine,
		registry.HostPlatform(),
		func() time.Duration { return 90 * time.Second }, func() time.Duration { return 2 * time.Second },
	)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid, Scope: "service"})
	got, _, _ := jobs.ClaimNext()
	applier.Handle(context.Background(), got)

	// 6) The apply must be recorded SUCCESS (not failed) and the digest refreshed.
	final, _ := jobs.Get(jid)
	if final.Status != "success" {
		logs, _ := store.NewJobLogs(db).ListByJob(jid)
		t.Fatalf("apply status = %q, want success; job error=%q logs=%v", final.Status, final.Error, logs)
	}
	svc, _ := store.NewServices(db).Get(sid)
	if svc.CurrentDigest == oldDigest || svc.CurrentDigest == "" {
		t.Fatalf("current_digest not refreshed after apply: %q (old %q)", svc.CurrentDigest, oldDigest)
	}
}
