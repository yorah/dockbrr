package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/secret"
	"dockbrr/internal/store"
)

// pruneFixture opens a temp store holding one finished job backdated by
// ageDays, and returns the settings/jobs repos plus that job's id.
func pruneFixture(t *testing.T, ageDays int) (*store.Settings, *store.Jobs, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "prune.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	jobs := store.NewJobs(db)
	id, err := jobs.Enqueue(store.Job{Type: "check", Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Finish(id, "success", nil, ""); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().AddDate(0, 0, -ageDays).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`UPDATE jobs SET created_at=? WHERE id=?`, ts, id); err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.NewSealer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return store.NewSettings(db, sealer), jobs, id
}

// runPruneOnce runs pruneLoop with an already-cancelled context: the boot-time
// sweep still runs, then the loop returns immediately.
func runPruneOnce(settings *store.Settings, jobs *store.Jobs) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pruneLoop(ctx, settings, jobs)
}

func TestPruneLoopRemovesJobsPastRetention(t *testing.T) {
	settings, jobs, id := pruneFixture(t, 40)
	if err := settings.Set("job_retention_days", "30"); err != nil {
		t.Fatal(err)
	}

	runPruneOnce(settings, jobs)

	if _, err := jobs.Get(id); err == nil {
		t.Fatal("40-day-old finished job survived a 30-day retention sweep")
	}
}

func TestPruneLoopKeepsJobsWithinRetention(t *testing.T) {
	settings, jobs, id := pruneFixture(t, 10)
	if err := settings.Set("job_retention_days", "30"); err != nil {
		t.Fatal(err)
	}

	runPruneOnce(settings, jobs)

	if _, err := jobs.Get(id); err != nil {
		t.Fatalf("10-day-old job was pruned under a 30-day retention: %v", err)
	}
}

func TestPruneLoopZeroDisablesPruning(t *testing.T) {
	settings, jobs, id := pruneFixture(t, 400)
	if err := settings.Set("job_retention_days", "0"); err != nil {
		t.Fatal(err)
	}

	runPruneOnce(settings, jobs)

	if _, err := jobs.Get(id); err != nil {
		t.Fatalf("retention 0 must disable pruning, but the job was deleted: %v", err)
	}
}

func TestPruneLoopUsesDefaultWhenUnset(t *testing.T) {
	settings, jobs, id := pruneFixture(t, defaultJobRetentionDays+10)

	runPruneOnce(settings, jobs) // no job_retention_days row at all

	if _, err := jobs.Get(id); err == nil {
		t.Fatalf("with no setting, the %d-day default must still prune older jobs", defaultJobRetentionDays)
	}
}
