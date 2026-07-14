package job

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func newEngineDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedPS(t *testing.T, db *store.DB, name string) (projectID, serviceID int64) {
	t.Helper()
	pid, err := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: name, Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	return pid, sid
}

// recordingHandler tracks max concurrent Handle calls per project and signals
// completion. It marks each job success so the queue drains.
type recordingHandler struct {
	jobs      *store.Jobs
	mu        sync.Mutex
	active    map[int64]int
	maxActive map[int64]int
	work      time.Duration
	done      chan int64
}

func newRecordingHandler(jobs *store.Jobs, work time.Duration, buf int) *recordingHandler {
	return &recordingHandler{jobs: jobs, active: map[int64]int{}, maxActive: map[int64]int{}, work: work, done: make(chan int64, buf)}
}

func (h *recordingHandler) Handle(_ context.Context, job store.Job) {
	pid := int64(0)
	if job.ProjectID != nil {
		pid = *job.ProjectID
	}
	h.mu.Lock()
	h.active[pid]++
	if h.active[pid] > h.maxActive[pid] {
		h.maxActive[pid] = h.active[pid]
	}
	h.mu.Unlock()
	time.Sleep(h.work)
	h.mu.Lock()
	h.active[pid]--
	h.mu.Unlock()
	code := 0
	_ = h.jobs.Finish(job.ID, "success", &code, "")
	h.done <- job.ID
}

func TestEngineSerializesPerProject(t *testing.T) {
	db := newEngineDB(t)
	pid, sid := seedPS(t, db, "p1")
	jobs := store.NewJobs(db)
	e := NewEngine(jobs, store.NewJobLogs(db), 5*time.Millisecond)
	h := newRecordingHandler(jobs, 20*time.Millisecond, 8)
	e.SetHandler(h)

	const n = 4
	for i := 0; i < n; i++ {
		if _, err := e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go e.Start(ctx, 4) // 4 workers, but same project => must serialize
	for i := 0; i < n; i++ {
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Fatal("jobs did not all complete")
		}
	}
	cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.maxActive[pid] != 1 {
		t.Fatalf("max concurrent jobs on project %d = %d, want 1", pid, h.maxActive[pid])
	}
}

func TestEngineDifferentProjectsAllComplete(t *testing.T) {
	db := newEngineDB(t)
	jobs := store.NewJobs(db)
	e := NewEngine(jobs, store.NewJobLogs(db), 5*time.Millisecond)
	h := newRecordingHandler(jobs, 10*time.Millisecond, 8)
	e.SetHandler(h)

	var pids []int64
	for _, name := range []string{"p1", "p2", "p3"} {
		pid, sid := seedPS(t, db, name)
		pids = append(pids, pid)
		if _, err := e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Start(ctx, 3)
	for range pids {
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Fatal("not all cross-project jobs completed")
		}
	}
}

func TestEngineOnFinishFiresPerJob(t *testing.T) {
	db := newEngineDB(t)
	pid, sid := seedPS(t, db, "p1")
	jobs := store.NewJobs(db)
	e := NewEngine(jobs, store.NewJobLogs(db), 5*time.Millisecond)
	h := newRecordingHandler(jobs, 5*time.Millisecond, 4)
	e.SetHandler(h)
	finished := make(chan int64, 4)
	e.OnFinish = func(id int64) { finished <- id }

	jid, err := e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Start(ctx, 1)
	select {
	case got := <-finished:
		if got != jid {
			t.Fatalf("OnFinish got job id %d, want %d", got, jid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnFinish never fired")
	}
}

// stubHandler emits a couple of lines then finishes the job.
type stubHandler struct {
	engine *Engine
	jobs   *store.Jobs
}

func (h *stubHandler) Handle(_ context.Context, job store.Job) {
	h.engine.Emit(job.ID, "stdout", "line-1")
	h.engine.Emit(job.ID, "stdout", "line-2")
	code := 0
	_ = h.jobs.Finish(job.ID, "success", &code, "")
}

func TestEngineStreamReceivesEmittedLinesThenCloses(t *testing.T) {
	db := newEngineDB(t)
	pid, sid := seedPS(t, db, "p1")
	jobs := store.NewJobs(db)
	e := NewEngine(jobs, store.NewJobLogs(db), 5*time.Millisecond)

	id, err := e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := e.Stream(id)
	if err != nil {
		t.Fatal(err)
	}
	e.SetHandler(&stubHandler{engine: e, jobs: jobs})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Start(ctx, 1)

	var got []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				if len(got) != 2 || got[0] != "line-1" || got[1] != "line-2" {
					t.Fatalf("streamed lines = %v, want [line-1 line-2]", got)
				}
				// Also persisted.
				logs, _ := store.NewJobLogs(db).ListByJob(id)
				if len(logs) != 2 {
					t.Fatalf("persisted logs = %d, want 2", len(logs))
				}
				return
			}
			got = append(got, line.Line)
		case <-timeout:
			t.Fatalf("stream did not close; got %v", got)
		}
	}
}

func TestEngineStreamUnknownJobErrors(t *testing.T) {
	db := newEngineDB(t)
	e := NewEngine(store.NewJobs(db), store.NewJobLogs(db), 5*time.Millisecond)
	if _, err := e.Stream(999); err == nil {
		t.Fatal("expected an error streaming an unknown job")
	}
}

// TestEngineStreamAfterFinishReturnsClosedChannel covers the late-subscribe
// TOCTOU: subscribing after the job has already finished (closeStreams has
// already run) must not register a channel that never closes.
func TestEngineStreamAfterFinishReturnsClosedChannel(t *testing.T) {
	db := newEngineDB(t)
	pid, sid := seedPS(t, db, "p1")
	jobs := store.NewJobs(db)
	e := NewEngine(jobs, store.NewJobLogs(db), 5*time.Millisecond)
	e.SetHandler(&stubHandler{engine: e, jobs: jobs})

	id, err := e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Start(ctx, 1)

	// Wait for the job to actually finish before subscribing.
	deadline := time.After(5 * time.Second)
	for {
		j, err := jobs.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == "success" || j.Status == "failed" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("job did not finish in time")
		case <-time.After(time.Millisecond):
		}
	}

	// Late subscribe: closeStreams has already run for this job.
	ch, err := e.Stream(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected a closed channel with no values, got a value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late Stream(id) channel never became receivable; leaked/hanging subscriber")
	}
}

// panicHandler panics on the configured job id and finishes any other job
// normally, so a second enqueued job proves the worker survived.
type panicHandler struct {
	jobs      *store.Jobs
	panicID   int64
	otherDone chan int64
}

func (h *panicHandler) Handle(_ context.Context, job store.Job) {
	if job.ID == h.panicID {
		panic("boom")
	}
	code := 0
	_ = h.jobs.Finish(job.ID, "success", &code, "")
	h.otherDone <- job.ID
}

// TestEngineHandlerPanicDoesNotKillWorker covers panic hardening in dispatch:
// a panicking handler must mark its job failed, close its stream, and leave
// the worker able to process subsequent jobs.
func TestEngineHandlerPanicDoesNotKillWorker(t *testing.T) {
	db := newEngineDB(t)
	pid, sid := seedPS(t, db, "p1")
	jobs := store.NewJobs(db)
	e := NewEngine(jobs, store.NewJobLogs(db), 5*time.Millisecond)

	panicID, err := e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := e.Stream(panicID)
	if err != nil {
		t.Fatal(err)
	}

	h := &panicHandler{jobs: jobs, panicID: panicID, otherDone: make(chan int64, 1)}
	e.SetHandler(h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Start(ctx, 1)

	// The panicking job's stream must still close.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected stream to close with no values after handler panic")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream for panicking job never closed")
	}

	// The job must be recorded failed, not left running/queued.
	j, err := jobs.Get(panicID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "failed" {
		t.Fatalf("panicking job status = %q, want failed", j.Status)
	}

	// A second job (same project, so it also needs the freed per-project lock)
	// must still be processed by the surviving worker.
	otherID, err := e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case done := <-h.otherDone:
		if done != otherID {
			t.Fatalf("done job id = %d, want %d", done, otherID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not process the second job after a handler panic")
	}
}

func TestEngineResumeInterrupted(t *testing.T) {
	db := newEngineDB(t)
	pid, sid := seedPS(t, db, "p1")
	jobs := store.NewJobs(db)
	e := NewEngine(jobs, store.NewJobLogs(db), 5*time.Millisecond)
	_, _ = e.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid})
	// Simulate a crash mid-run: claim leaves it running.
	if _, ok, _ := jobs.ClaimNext(); !ok {
		t.Fatal("expected to claim the enqueued job")
	}
	n, err := e.ResumeInterrupted()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("resumed %d, want 1", n)
	}
	var running int32
	atomic.StoreInt32(&running, 0)
	if _, ok, _ := jobs.ClaimNext(); !ok {
		t.Fatal("job not re-queued after ResumeInterrupted")
	}
}
