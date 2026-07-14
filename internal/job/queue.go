package job

import (
	"context"
	"fmt"
	"sync"
	"time"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// LogLine is one streamed output line delivered to a Stream subscriber.
type LogLine struct {
	Stream string
	Line   string
}

// Handler runs a single claimed job. It must not panic and must record a
// terminal job result before returning.
type Handler interface {
	Handle(ctx context.Context, job store.Job)
}

// Emitter persists and fans out a job's output lines. The Engine implements it;
// the Applier (Task 8) depends only on this interface.
type Emitter interface {
	Emit(jobID int64, stream, line string)
}

// Engine is the persisted queue + worker pool. It is the only component that
// drives Docker-mutating handlers, and it serializes jobs per project via a
// keyed mutex.
type Engine struct {
	jobs    *store.Jobs
	logs    *store.JobLogs
	poll    time.Duration
	locks   *keyedMutex
	handler Handler
	// OnFinish, when non-nil, is called with a job's id once its handler has
	// completed and its streams are closed. It is a plain callback so job never
	// imports httpapi (avoids an import cycle); only cmd/dockbrr wires it to the
	// event bus. Set it before Start.
	OnFinish func(jobID int64)

	mu   sync.Mutex
	subs map[int64][]chan LogLine
	// finished marks jobs whose streams have already been closed, so a
	// late Stream(id) subscriber gets an already-closed channel instead of
	// one that never closes. Grows one entry per job run for the life of
	// the process; pruning is a later concern, not needed for this phase.
	finished map[int64]bool
}

// NewEngine builds an Engine. poll is the idle interval between claim attempts
// when the queue is empty. Call SetHandler before Start.
func NewEngine(jobs *store.Jobs, logs *store.JobLogs, poll time.Duration) *Engine {
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	return &Engine{
		jobs:     jobs,
		logs:     logs,
		poll:     poll,
		locks:    newKeyedMutex(),
		subs:     make(map[int64][]chan LogLine),
		finished: make(map[int64]bool),
	}
}

// SetHandler sets the dispatch target. Safe to call before Start.
func (e *Engine) SetHandler(h Handler) { e.handler = h }

// Enqueue persists a queued job.
func (e *Engine) Enqueue(j store.Job) (int64, error) { return e.jobs.Enqueue(j) }

// ResumeInterrupted re-queues jobs left running (crash recovery). Call once on
// boot before Start.
func (e *Engine) ResumeInterrupted() (int64, error) { return e.jobs.ResumeRunning() }

// Emit persists a log line and fans it out to live subscribers. Fan-out is
// best-effort/non-blocking so a slow consumer never stalls the handler.
func (e *Engine) Emit(jobID int64, stream, line string) {
	if err := e.logs.Append(jobID, stream, line); err != nil {
		logger.Errorf("job: append log (job %d): %v", jobID, err)
	}
	e.mu.Lock()
	subs := e.subs[jobID]
	e.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- LogLine{Stream: stream, Line: line}:
		default: // drop on a full buffer rather than block the handler
		}
	}
}

// Stream subscribes to a job's live output. The channel is closed when the
// job's handler completes. Errors if the job id is unknown. If the job has
// already finished (including a race where it finishes between the existence
// check and registration), the returned channel is already closed so a late
// subscriber's range loop exits immediately instead of hanging forever.
func (e *Engine) Stream(id int64) (<-chan LogLine, error) {
	if _, err := e.jobs.Get(id); err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.finished[id] {
		e.mu.Unlock()
		ch := make(chan LogLine)
		close(ch)
		return ch, nil
	}
	ch := make(chan LogLine, 256)
	e.subs[id] = append(e.subs[id], ch)
	e.mu.Unlock()
	return ch, nil
}

// closeStreams closes and forgets every subscriber for a finished job, and
// marks the job finished so any late Stream(id) call returns a closed channel
// instead of registering one that would never be closed.
func (e *Engine) closeStreams(id int64) {
	e.mu.Lock()
	subs := e.subs[id]
	delete(e.subs, id)
	e.finished[id] = true
	e.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

// Start runs concurrency workers until ctx is cancelled, then returns once they
// have all exited.
func (e *Engine) Start(ctx context.Context, concurrency int) {
	if concurrency < 1 {
		concurrency = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.worker(ctx)
		}()
	}
	wg.Wait()
}

// worker claims and dispatches jobs until ctx is cancelled.
func (e *Engine) worker(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, ok, err := e.jobs.ClaimNext()
		if err != nil {
			logger.Errorf("job: claim-next: %v", err)
			if !sleepCtx(ctx, e.poll) {
				return
			}
			continue
		}
		if !ok {
			if !sleepCtx(ctx, e.poll) {
				return
			}
			continue
		}
		e.dispatch(ctx, job)
	}
}

// dispatch acquires the per-project lock, runs the handler, releases the lock,
// and closes the job's live streams. The keyed mutex enforces one in-flight job
// per project across all workers. Handler.Handle is documented as non-fatal,
// but a recover() guards against a misbehaving handler panicking anyway: the
// job is marked failed and the worker goroutine survives to claim more work.
func (e *Engine) dispatch(ctx context.Context, job store.Job) {
	var key int64
	if job.ProjectID != nil {
		key = *job.ProjectID
	}
	e.locks.Lock(key)
	defer func() {
		e.locks.Unlock(key)
		e.closeStreams(job.ID)
		if e.OnFinish != nil {
			e.OnFinish(job.ID)
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("job: handler panic (job %d): %v", job.ID, r)
			_ = e.jobs.Finish(job.ID, "failed", nil, fmt.Sprintf("handler panic: %v", r))
		}
	}()
	if e.handler == nil {
		logger.Warnf("job: no handler set; marking job %d failed", job.ID)
		_ = e.jobs.Finish(job.ID, "failed", nil, "no handler configured")
		return
	}
	logger.Debugf("job: dispatch %s (job %d, project %d)", job.Type, job.ID, key)
	e.handler.Handle(ctx, job)
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
