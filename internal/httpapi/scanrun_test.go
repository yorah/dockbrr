package httpapi

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/secret"
	"dockbrr/internal/store"
)

// seedProjectServices opens a fresh temp DB, seeds one project, and seeds n
// services under it. It returns the db, the project id, and the seeded
// service ids in insertion order.
func seedProjectServices(t *testing.T, n int) (*store.DB, int64, []int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pid, err := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	svcs := store.NewServices(db)
	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		sid, err := svcs.Upsert(store.Service{ProjectID: pid, Name: fmt.Sprintf("app%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = sid
	}
	return db, pid, ids
}

func storeServices(db *store.DB) *store.Services { return store.NewServices(db) }

// storeSettings wires a *store.Settings with a fixed all-zero 32-byte sealer
// key. Tests only exercise Get/Set (never SetSecret/GetSecret), so the key
// material itself doesn't matter, only that it's a valid 32-byte AES key.
func storeSettings(db *store.DB) *store.Settings {
	sealer, err := secret.NewSealer(make([]byte, 32))
	if err != nil {
		panic(err)
	}
	return store.NewSettings(db, sealer)
}

// drainEventTypes collects event Types off ch until quiet (no new event
// within the given window), then returns them.
func drainEventTypes(t *testing.T, ch <-chan Event, quiet time.Duration) []string {
	t.Helper()
	var types []string
	for {
		select {
		case ev := <-ch:
			types = append(types, ev.Type)
		case <-time.After(quiet):
			return types
		}
	}
}

// blockingChecker lets the test hold a run "in flight" to exercise single-flight.
type blockingChecker struct {
	release chan struct{}
	started chan struct{}
}

func (b *blockingChecker) CheckServiceFresh(context.Context, int64) error { return nil }
func (b *blockingChecker) CheckAllFresh(context.Context) error            { return nil }
func (b *blockingChecker) CheckServicesFresh(_ context.Context, ids []int64, _ bool, onDone func(done, total int)) error {
	close(b.started)
	<-b.release
	for i := range ids {
		if onDone != nil {
			onDone(i+1, len(ids))
		}
	}
	return nil
}

func TestScanRunnerSingleFlight(t *testing.T) {
	db, projectID, svcIDs := seedProjectServices(t, 2) // helper: returns db + one project + 2 service ids
	bus := NewBus()
	bc := &blockingChecker{release: make(chan struct{}), started: make(chan struct{})}
	sr := NewScanRunner(bc, storeServices(db), storeSettings(db), bus)

	_, err := sr.Start("all", 0, 0)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	<-bc.started
	if _, err := sr.Start("all", 0, 0); !errors.Is(err, ErrScanBusy) {
		t.Fatalf("second Start err = %v, want ErrScanBusy", err)
	}
	if snap := sr.Snapshot(); !snap.Running || snap.Total != 2 {
		t.Fatalf("snapshot = %+v, want running total=2", snap)
	}
	close(bc.release)
	// Wait for the background run to finish so its DB write (last_check_all)
	// completes before t.Cleanup closes the db out from under it; otherwise the
	// goroutine logs a spurious "database is closed" error after this test
	// returns.
	deadline := time.Now().Add(2 * time.Second)
	for sr.Snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sr.Snapshot().Running {
		t.Fatal("background scan-run did not finish before deadline")
	}
	_ = projectID
	_ = svcIDs
}

func TestScanRunnerAllScopeStampsLastCheckAllAndPublishes(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	settings := storeSettings(db)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), settings, bus) // fakeChecker from Task 2 auto-completes

	if _, err := sr.Start("all", 0, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	types := drainEventTypes(t, ch, 300*time.Millisecond) // helper: collect event types until quiet
	if !contains(types, "scan_finished") || !contains(types, "scanned") {
		t.Fatalf("event types = %v, want scanned + scan_finished", types)
	}
	if v, _ := settings.Get("last_check_all"); v == "" {
		t.Fatalf("last_check_all not stamped for all scope")
	}
}

func TestScanRunnerServiceScopeDoesNotStampLastCheckAll(t *testing.T) {
	db, _, svcIDs := seedProjectServices(t, 1)
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	settings := storeSettings(db)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), settings, bus)

	if _, err := sr.Start("service", 0, svcIDs[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	types := drainEventTypes(t, ch, 300*time.Millisecond)
	if contains(types, "scanned") {
		t.Fatalf("service scope must NOT publish scanned; got %v", types)
	}
	if v, _ := settings.Get("last_check_all"); v != "" {
		t.Fatalf("service scope must not stamp last_check_all, got %q", v)
	}
}

// abortableChecker blocks in CheckServicesFresh until either release is closed
// or ctx is cancelled. cancelled records that the context path won (i.e. Abort
// reached the sweep).
type abortableChecker struct {
	started   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
}

func newAbortableChecker() *abortableChecker {
	return &abortableChecker{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (a *abortableChecker) CheckServiceFresh(context.Context, int64) error { return nil }
func (a *abortableChecker) CheckAllFresh(context.Context) error            { return nil }
func (a *abortableChecker) CheckServicesFresh(ctx context.Context, _ []int64, _ bool, _ func(done, total int)) error {
	close(a.started)
	select {
	case <-a.release:
	case <-ctx.Done():
		close(a.cancelled)
	}
	return nil
}

func TestScanRunnerAbortCancelsRunAndSkipsStamp(t *testing.T) {
	db, _, _ := seedProjectServices(t, 2)
	bus := NewBus()
	ch, cancelSub := bus.Subscribe()
	defer cancelSub()
	settings := storeSettings(db)
	ac := newAbortableChecker()
	sr := NewScanRunner(ac, storeServices(db), settings, bus)

	if _, err := sr.Start("all", 0, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-ac.started
	sr.Abort()

	select {
	case <-ac.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort did not cancel the sweep context")
	}

	// Run should wind down: not running, scan_finished published, NO scanned,
	// last_check_all left blank.
	types := drainEventTypes(t, ch, 300*time.Millisecond)
	if !contains(types, "scan_finished") {
		t.Fatalf("want scan_finished after abort, got %v", types)
	}
	if contains(types, "scanned") {
		t.Fatalf("aborted run must not publish scanned, got %v", types)
	}
	if v, _ := settings.Get("last_check_all"); v != "" {
		t.Fatalf("aborted run must not stamp last_check_all, got %q", v)
	}
	if sr.Snapshot().Running {
		t.Fatal("runner still marked running after abort")
	}
}

func TestScanRunnerAbortIdleIsNoOp(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), storeSettings(db), NewBus())
	sr.Abort() // must not panic
}

func TestRunScheduledRunsSynchronouslyAndStamps(t *testing.T) {
	db, _, _ := seedProjectServices(t, 2)
	settings := storeSettings(db)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), settings, NewBus())

	if ran := sr.RunScheduled(context.Background()); !ran {
		t.Fatal("RunScheduled returned false, want true")
	}
	// Synchronous: by return, the run is done and last_check_all is stamped.
	if sr.Snapshot().Running {
		t.Fatal("RunScheduled returned while still running (must be synchronous)")
	}
	if v, _ := settings.Get("last_check_all"); v == "" {
		t.Fatal("RunScheduled did not stamp last_check_all")
	}
}

func TestRunScheduledSkipsWhenBusy(t *testing.T) {
	db, _, _ := seedProjectServices(t, 2)
	bc := &blockingChecker{release: make(chan struct{}), started: make(chan struct{})}
	sr := NewScanRunner(bc, storeServices(db), storeSettings(db), NewBus())

	if _, err := sr.Start("all", 0, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-bc.started
	if ran := sr.RunScheduled(context.Background()); ran {
		t.Fatal("RunScheduled ran while a manual scan held the guard, want skip")
	}
	close(bc.release)
	deadline := time.Now().Add(2 * time.Second)
	for sr.Snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}
