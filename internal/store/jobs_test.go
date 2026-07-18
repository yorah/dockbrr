package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func seedProjectService(t *testing.T, db *store.DB) (projectID, serviceID int64) {
	t.Helper()
	pid, err := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	return pid, sid
}

func TestJobsEnqueueAndGet(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	id, err := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid, Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
	got, err := jobs.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "apply" || got.Status != "queued" || got.Scope != "service" {
		t.Fatalf("job = %+v", got)
	}
	if got.ProjectID == nil || *got.ProjectID != pid {
		t.Fatalf("project_id = %v, want %d", got.ProjectID, pid)
	}
}

func TestJobsGetMissingReturnsSentinel(t *testing.T) {
	db := openImagesStore(t)
	if _, err := store.NewJobs(db).Get(999); !errors.Is(err, store.ErrJobNotFound) {
		t.Fatalf("err = %v, want ErrJobNotFound", err)
	}
}

func TestJobsClaimNextIsFIFOAndMarksRunning(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	first, _ := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	_, _ = jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})

	claimed, ok, err := jobs.ClaimNext()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ClaimNext returned ok=false with a queued job present")
	}
	if claimed.ID != first {
		t.Fatalf("claimed id = %d, want oldest %d", claimed.ID, first)
	}
	if claimed.Status != "running" {
		t.Fatalf("claimed status = %q, want running", claimed.Status)
	}
	if claimed.StartedAt == nil {
		t.Fatal("claimed started_at is nil, want set")
	}
}

func TestJobsClaimNextEmptyReturnsFalse(t *testing.T) {
	db := openImagesStore(t)
	_, ok, err := store.NewJobs(db).ClaimNext()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ClaimNext returned ok=true on an empty queue")
	}
}

func TestJobsClaimNextConcurrentNoDoubleClaim(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	const n = 5
	for i := 0; i < n; i++ {
		if _, err := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid}); err != nil {
			t.Fatal(err)
		}
	}
	const workers = 8
	var (
		mu      sync.Mutex
		claimed = map[int64]int{}
		wg      sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, ok, err := jobs.ClaimNext()
				if err != nil {
					t.Errorf("ClaimNext: %v", err)
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				claimed[j.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claimed) != n {
		t.Fatalf("claimed %d distinct jobs, want %d", len(claimed), n)
	}
	for id, c := range claimed {
		if c != 1 {
			t.Fatalf("job %d claimed %d times, want exactly 1", id, c)
		}
	}
}

func TestJobsFinishRecordsResult(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	id, _ := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	_, _, _ = jobs.ClaimNext()
	code := 0
	if err := jobs.Finish(id, "success", &code, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := jobs.Get(id)
	if got.Status != "success" {
		t.Fatalf("status = %q, want success", got.Status)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at is nil, want set")
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("exit_code = %v, want 0", got.ExitCode)
	}
}

func TestJobsResumeRunningRequeues(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	_, _ = jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	_, _ = jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	// Claim both -> both running.
	_, _, _ = jobs.ClaimNext()
	_, _, _ = jobs.ClaimNext()
	n, err := jobs.ResumeRunning()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("resumed %d, want 2", n)
	}
	// Both claimable again.
	if _, ok, _ := jobs.ClaimNext(); !ok {
		t.Fatal("expected a queued job after ResumeRunning")
	}
}

func TestJobsListNewestFirst(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	got, err := jobs.List(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != ids[2] || got[1].ID != ids[1] {
		t.Fatalf("ids = [%d,%d], want [%d,%d] (newest first)", got[0].ID, got[1].ID, ids[2], ids[1])
	}
	if got[0].ProjectName != "p" || got[0].ServiceName != "app" {
		t.Fatalf("names = %q/%q, want p/app", got[0].ProjectName, got[0].ServiceName)
	}
}

func TestJobsListNamesEmptyWhenUnset(t *testing.T) {
	db := openImagesStore(t)
	jobs := store.NewJobs(db)
	// No project/service reference at all (e.g. a sync job).
	if _, err := jobs.Enqueue(store.Job{Type: "sync"}); err != nil {
		t.Fatal(err)
	}
	got, err := jobs.List(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ProjectName != "" || got[0].ServiceName != "" {
		t.Fatalf("names = %q/%q, want empty", got[0].ProjectName, got[0].ServiceName)
	}
}

func TestJobsListDefaultsLimit(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	if _, err := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid}); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -1, 501} {
		got, err := jobs.List(limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("limit=%d: len = %d, want 1", limit, len(got))
		}
	}
}

// finishedJob enqueues a job and drives it to a terminal status.
func finishedJob(t *testing.T, jobs *store.Jobs, pid, sid int64, status string) int64 {
	t.Helper()
	id, err := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Finish(id, status, nil, ""); err != nil {
		t.Fatal(err)
	}
	return id
}

// backdate rewrites a job's created_at to age days in the past, so the
// retention predicate has something old to find.
func backdate(t *testing.T, db *store.DB, id int64, days int) {
	t.Helper()
	ts := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`UPDATE jobs SET created_at=? WHERE id=?`, ts, id); err != nil {
		t.Fatal(err)
	}
}

func TestJobsDeleteFinishedKeepsQueuedAndRunning(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	success := finishedJob(t, jobs, pid, sid, "success")
	failed := finishedJob(t, jobs, pid, sid, "failed")
	canceled := finishedJob(t, jobs, pid, sid, "canceled")
	queued, _ := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	running, _, err := jobs.ClaimNext()
	if err != nil {
		t.Fatal(err)
	}

	n, err := jobs.DeleteFinished()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("deleted = %d, want 3", n)
	}
	for _, id := range []int64{success, failed, canceled} {
		if _, err := jobs.Get(id); !errors.Is(err, store.ErrJobNotFound) {
			t.Fatalf("job %d: err = %v, want ErrJobNotFound", id, err)
		}
	}
	// ClaimNext promoted the oldest queued job (the success one is finished, so
	// it took `queued`); both survivors must still be gettable.
	for _, id := range []int64{queued, running.ID} {
		if _, err := jobs.Get(id); err != nil {
			t.Fatalf("job %d was deleted, want kept: %v", id, err)
		}
	}
}

func TestJobsDeleteFinishedCascadesLogs(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	id := finishedJob(t, jobs, pid, sid, "success")
	if err := store.NewJobLogs(db).Append(id, "stdout", "pulling..."); err != nil {
		t.Fatal(err)
	}

	if _, err := jobs.DeleteFinished(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_logs WHERE job_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("job_logs rows = %d, want 0 (ON DELETE CASCADE)", n)
	}
}

func TestJobsDeleteFinishedKeepsSnapshotWithNullJob(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	id := finishedJob(t, jobs, pid, sid, "success")
	snaps := store.NewSnapshots(db)
	if _, err := snaps.Insert(store.Snapshot{ServiceID: sid, JobID: &id, PrevDigest: "sha256:old"}); err != nil {
		t.Fatal(err)
	}

	if _, err := jobs.DeleteFinished(); err != nil {
		t.Fatal(err)
	}
	// Rollback resolves the snapshot by service, so it must survive the purge.
	sn, err := snaps.GetLatestForService(sid)
	if err != nil {
		t.Fatalf("snapshot lost with its job: %v", err)
	}
	if sn.JobID != nil {
		t.Fatalf("snapshot job_id = %v, want NULL (ON DELETE SET NULL)", *sn.JobID)
	}
	if sn.PrevDigest != "sha256:old" {
		t.Fatalf("snapshot prev_digest = %q, want preserved", sn.PrevDigest)
	}
}

func TestJobsDeleteFinishedBeforeSpares(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	old := finishedJob(t, jobs, pid, sid, "success")
	backdate(t, db, old, 40)
	recent := finishedJob(t, jobs, pid, sid, "failed")
	oldQueued, _ := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	backdate(t, db, oldQueued, 40)

	n, err := jobs.DeleteFinishedBefore(time.Now().UTC().AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted = %d, want 1 (only the old finished job)", n)
	}
	if _, err := jobs.Get(old); !errors.Is(err, store.ErrJobNotFound) {
		t.Fatalf("old finished job: err = %v, want ErrJobNotFound", err)
	}
	if _, err := jobs.Get(recent); err != nil {
		t.Fatalf("recent finished job was deleted: %v", err)
	}
	if _, err := jobs.Get(oldQueued); err != nil {
		t.Fatalf("old queued job was deleted, want kept: %v", err)
	}
}

func TestJobLogsAppendAndList(t *testing.T) {
	db := openImagesStore(t)
	pid, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	id, _ := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	logs := store.NewJobLogs(db)
	if err := logs.Append(id, "stdout", "pulling..."); err != nil {
		t.Fatal(err)
	}
	if err := logs.Append(id, "stderr", "warning"); err != nil {
		t.Fatal(err)
	}
	got, err := logs.ListByJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Line != "pulling..." || got[0].Stream != "stdout" {
		t.Fatalf("first log = %+v, want oldest-first stdout", got[0])
	}
	if got[1].Stream != "stderr" {
		t.Fatalf("second log stream = %q, want stderr", got[1].Stream)
	}
}
