package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// scanRunTimeout bounds an unattended background sweep. Generous versus the
// old 60s request cap since the run no longer blocks an HTTP request.
const scanRunTimeout = 5 * time.Minute

// ErrScanBusy is returned by Start when a scan-run is already in flight.
var ErrScanBusy = errors.New("a scan is already running")

// scanState is the authoritative snapshot of the single in-flight scan-run.
type scanState struct {
	Running bool `json:"running"`
	Done    int  `json:"done"`
	Total   int  `json:"total"`
}

// ScanRunner owns the process-wide single scan-run: a read-only detection
// sweep tracked in memory and broadcast over the SSE bus. It is NOT a Job
// Engine job (detection never mutates Docker; invariant #2).
type ScanRunner struct {
	checker  Checker
	services *store.Services
	settings *store.Settings
	bus      *Bus

	mu     sync.Mutex
	state  scanState
	cancel context.CancelFunc // non-nil while a run is in flight; Abort() calls it
}

func NewScanRunner(checker Checker, services *store.Services, settings *store.Settings, bus *Bus) *ScanRunner {
	return &ScanRunner{checker: checker, services: services, settings: settings, bus: bus}
}

// tryBegin flips the runner to running with the given total under the guard.
// Returns false if a run is already in flight (single-flight).
func (sr *ScanRunner) tryBegin(total int) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.state.Running {
		return false
	}
	sr.state = scanState{Running: true, Done: 0, Total: total}
	return true
}

// Start begins an asynchronous scan-run over scope ("all" | "project" |
// "service"). Returns the started snapshot, or ErrScanBusy if one is already
// running. The run executes on a process-lifetime context so it survives the
// originating HTTP request.
func (sr *ScanRunner) Start(scope string, projectID, serviceID int64) (scanState, error) {
	ids, err := sr.resolve(scope, projectID, serviceID)
	if err != nil {
		return scanState{}, err
	}
	if !sr.tryBegin(len(ids)) {
		return sr.Snapshot(), ErrScanBusy
	}
	st := sr.Snapshot()
	go sr.execute(context.Background(), scope, ids)
	return st, nil
}

// RunScheduled runs an all-scope sweep synchronously (the scheduler's path):
// it returns only when the sweep completes, so the caller can auto-apply after.
// Returns false without running if a scan is already in flight (blocked, not
// preempting), and also false if the sweep started but did not complete
// (aborted or timed out) so the caller must not auto-apply on a partial sweep.
// The passed ctx is the scheduler's context, so a shutdown cancels the sweep too.
func (sr *ScanRunner) RunScheduled(ctx context.Context) bool {
	ids, err := sr.resolve("all", 0, 0)
	if err != nil {
		logger.Errorf("scan: scheduled resolve: %v", err)
		return false
	}
	if !sr.tryBegin(len(ids)) {
		logger.Infof("scan: scheduled tick skipped, a scan is already running")
		return false
	}
	return sr.execute(ctx, "all", ids)
}

// Abort cancels the in-flight scan-run, if any. Idempotent: a no-op when idle.
func (sr *ScanRunner) Abort() {
	sr.mu.Lock()
	c := sr.cancel
	sr.mu.Unlock()
	if c != nil {
		c()
	}
}

// Snapshot returns the current scan-run state.
func (sr *ScanRunner) Snapshot() scanState {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.state
}

func (sr *ScanRunner) resolve(scope string, projectID, serviceID int64) ([]int64, error) {
	switch scope {
	case "service":
		return []int64{serviceID}, nil
	case "project":
		svcs, err := sr.services.ListByProject(projectID)
		if err != nil {
			return nil, err
		}
		return idsOf(svcs), nil
	default: // "all"
		svcs, err := sr.services.List()
		if err != nil {
			return nil, err
		}
		return idsOf(svcs), nil
	}
}

// execute drives one sweep (shared by Start's goroutine and RunScheduled's
// inline call). It stores its cancel for Abort, reports progress, and on a
// COMPLETED all-scope sweep stamps last_check_all + publishes "scanned". An
// aborted run (ctx cancelled) leaves the "Last scan" tile untouched. It always
// publishes "scan_finished" so the UI clears the bar and re-enables buttons.
// Returns whether the sweep completed (ctx.Err() == nil), so RunScheduled can
// report completion rather than merely "ran" to the scheduler's auto-apply gate.
func (sr *ScanRunner) execute(parent context.Context, scope string, ids []int64) bool {
	ctx, cancel := context.WithTimeout(parent, scanRunTimeout)
	sr.mu.Lock()
	sr.cancel = cancel
	sr.mu.Unlock()
	defer cancel()

	sr.publish(Event{Type: "scan_progress", Done: 0, Total: len(ids)})

	// Scoped (service/project) runs lift the rolled_back suppression; an
	// all-services sweep must never reopen (see the original comment).
	reopen := scope != "all" && scope != ""
	_, _ = sr.checker.CheckServicesFresh(ctx, ids, reopen, func(done, total int) {
		sr.mu.Lock()
		sr.state.Done = done
		sr.mu.Unlock()
		sr.publish(Event{Type: "scan_progress", Done: done, Total: total})
	})

	completed := ctx.Err() == nil
	if completed && (scope == "all" || scope == "") {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := sr.settings.Set("last_check_all", now); err != nil {
			logger.Errorf("scan: record last_check_all: %v", err)
		}
		sr.publish(Event{Type: "scanned"})
	}

	sr.mu.Lock()
	sr.cancel = nil
	sr.state = scanState{Running: false}
	sr.mu.Unlock()
	sr.publish(Event{Type: "scan_finished"})
	return completed
}

func (sr *ScanRunner) publish(e Event) {
	if sr.bus != nil {
		sr.bus.Publish(e)
	}
}

func idsOf(svcs []store.Service) []int64 {
	ids := make([]int64, len(svcs))
	for i, sv := range svcs {
		ids[i] = sv.ID
	}
	return ids
}
