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

	mu    sync.Mutex
	state scanState
}

func NewScanRunner(checker Checker, services *store.Services, settings *store.Settings, bus *Bus) *ScanRunner {
	return &ScanRunner{checker: checker, services: services, settings: settings, bus: bus}
}

// Start begins a scan-run over scope ("all" | "project" | "service"). It
// returns the started snapshot, or ErrScanBusy if one is already running.
func (sr *ScanRunner) Start(scope string, projectID, serviceID int64) (scanState, error) {
	ids, err := sr.resolve(scope, projectID, serviceID)
	if err != nil {
		return scanState{}, err
	}
	sr.mu.Lock()
	if sr.state.Running {
		st := sr.state
		sr.mu.Unlock()
		return st, ErrScanBusy
	}
	sr.state = scanState{Running: true, Done: 0, Total: len(ids)}
	st := sr.state
	sr.mu.Unlock()

	sr.publish(Event{Type: "scan_progress", Done: 0, Total: len(ids)})
	go sr.run(scope, ids)
	return st, nil
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

func (sr *ScanRunner) run(scope string, ids []int64) {
	ctx, cancel := context.WithTimeout(context.Background(), scanRunTimeout)
	defer cancel()

	_ = sr.checker.CheckServicesFresh(ctx, ids, func(done, total int) {
		sr.mu.Lock()
		sr.state.Done = done
		sr.mu.Unlock()
		sr.publish(Event{Type: "scan_progress", Done: done, Total: total})
	})

	if scope == "all" || scope == "" {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := sr.settings.Set("last_check_all", now); err != nil {
			logger.Errorf("scan: record last_check_all: %v", err)
		}
		sr.publish(Event{Type: "scanned"})
	}

	sr.mu.Lock()
	sr.state = scanState{Running: false}
	sr.mu.Unlock()
	sr.publish(Event{Type: "scan_finished"})
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
