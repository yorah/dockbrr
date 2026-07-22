package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dockbrr/internal/job"
	"dockbrr/internal/store"
)

// fakeEngine records enqueued jobs and serves a canned stream.
type fakeEngine struct {
	enqueued  []store.Job
	nextID    int64
	ch        <-chan job.LogLine
	streamErr error
}

func (f *fakeEngine) Enqueue(j store.Job) (int64, error) {
	f.nextID++
	f.enqueued = append(f.enqueued, j)
	return f.nextID, nil
}
func (f *fakeEngine) Stream(id int64) (<-chan job.LogLine, error) { return f.ch, f.streamErr }

// fakeChecker signals which services were checked via CheckServicesFresh.
type fakeChecker struct {
	servicesFreshCalls [][]int64
	servicesFreshErr   error
	// servicesFreshReopen records the reopen flag from the most recent
	// CheckServicesFresh call, so tests can assert scope->reopen wiring.
	servicesFreshReopen bool
}

func (f *fakeChecker) CheckServicesFresh(_ context.Context, ids []int64, reopen bool, onDone func(done, total int)) (bool, error) {
	f.servicesFreshCalls = append(f.servicesFreshCalls, ids)
	f.servicesFreshReopen = reopen
	for i := range ids {
		if onDone != nil {
			onDone(i+1, len(ids))
		}
	}
	return false, f.servicesFreshErr
}

func actionDeps(db *store.DB, eng *fakeEngine, chk *fakeChecker) Deps {
	return Deps{
		Updates:  store.NewUpdates(db),
		Jobs:     store.NewJobs(db),
		Services: store.NewServices(db),
		Projects: store.NewProjects(db),
		Events:   store.NewEvents(db),
		Engine:   eng,
		Checker:  chk,
		HostID:   1,
	}
}

func TestApplyEnqueuesJobNoDocker(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	s.deps = mergeDeps(s.deps, actionDeps(db, eng, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	uid, _ := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "available"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/updates/%d/apply", uid),
		strings.NewReader(`{"scope":"service"}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("apply = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(eng.enqueued) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(eng.enqueued))
	}
	j := eng.enqueued[0]
	if j.Type != "apply" || j.ServiceID == nil || *j.ServiceID != sid || j.ProjectID == nil || *j.ProjectID != pid || j.Scope != "service" {
		t.Fatalf("enqueued job = %+v", j)
	}
	if !strings.Contains(rec.Body.String(), `"job_id":1`) {
		t.Fatalf("apply body = %s", rec.Body.String())
	}
}

func TestApplyUnknownUpdate404(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, &fakeChecker{}))
	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/999/apply", strings.NewReader(`{}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("apply unknown = %d, want 404", rec.Code)
	}
}

func TestApplyRefusedForUnmanagedProject(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	s.deps = mergeDeps(s.deps, actionDeps(db, eng, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err := store.NewProjects(db).SetUnmanaged(pid, true); err != nil {
		t.Fatal(err)
	}
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	uid, _ := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "available"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/updates/%d/apply", uid),
		strings.NewReader(`{"scope":"service"}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply on unmanaged project = %d, want 409 body=%s", rec.Code, rec.Body.String())
	}
	if len(eng.enqueued) != 0 {
		t.Fatalf("enqueued %d jobs, want 0 for unmanaged project", len(eng.enqueued))
	}
}

func TestApplyRefusedForGoneService(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	s.deps = mergeDeps(s.deps, actionDeps(db, eng, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", State: "gone"})
	uid, _ := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "available"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/updates/%d/apply", uid),
		strings.NewReader(`{"scope":"service"}`)), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply on gone service = %d, want 409 body=%s", rec.Code, rec.Body.String())
	}
	if len(eng.enqueued) != 0 {
		t.Fatalf("enqueued %d jobs, want 0 for gone service", len(eng.enqueued))
	}
}

// TestListUpdatesIncludesDismissed asserts GET /api/updates surfaces dismissed
// updates alongside available ones. The dashboard needs both so a dismissed
// row can still render its Dismissed badge and Restore action.
func TestListUpdatesIncludesDismissed(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	if _, err := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:avail", Status: "available"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:dismissed", Status: "dismissed"}); err != nil {
		t.Fatal(err)
	}

	req := authReq(httptest.NewRequest(http.MethodGet, "/api/updates", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list updates = %d body=%s", rec.Code, rec.Body.String())
	}
	var got []updateDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v, body=%s", err, rec.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d updates, want 2: %+v", len(got), got)
	}
	var sawDismissed bool
	for _, u := range got {
		if u.Status == "dismissed" {
			sawDismissed = true
		}
	}
	if !sawDismissed {
		t.Fatalf("no dismissed update in response: %+v", got)
	}
}

func TestDismissSetsStatus(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	uid, _ := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "available"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/updates/%d/dismiss", uid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss = %d", rec.Code)
	}
	open, _ := store.NewUpdates(db).ListOpen()
	if len(open) != 0 {
		t.Fatalf("update still open after dismiss: %+v", open)
	}
}

func TestRestoreSetsStatusAvailable(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	uid, _ := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "dismissed"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/updates/%d/restore", uid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore = %d", rec.Code)
	}
	// ListOpen returns only status='available' rows, so a restored update reappears.
	open, _ := store.NewUpdates(db).ListOpen()
	if len(open) != 1 {
		t.Fatalf("update not open after restore: %+v", open)
	}
}

func TestRestoreUnknownIDReturns404(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, &fakeChecker{}))
	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/9999/restore", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("restore unknown id = %d, want 404", rec.Code)
	}
}

func TestRollbackEnqueuesJob(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	eng := &fakeEngine{}
	s.deps = mergeDeps(s.deps, actionDeps(db, eng, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	origID, _ := store.NewJobs(db).Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid, Scope: "service"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/jobs/%d/rollback", origID), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rollback = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(eng.enqueued) != 1 || eng.enqueued[0].Type != "rollback" || *eng.enqueued[0].ServiceID != sid {
		t.Fatalf("rollback enqueue = %+v", eng.enqueued)
	}
}

// TestCheckStartsScanRunAndReports202 asserts the handler starts a
// service-scoped scan-run and returns 202 immediately (progress + completion
// now arrive over SSE, not in the response). s.scan is built inside New from
// the deps present at construction, so the checker must be passed into
// authedServer directly rather than merged in afterward.
func TestCheckStartsScanRunAndReports202(t *testing.T) {
	db, pid, sids := seedProjectServices(t, 1)
	sid := sids[0]
	bus := NewBus()
	chk := &fakeChecker{}
	deps := mergeDeps(actionDeps(db, &fakeEngine{}, chk), Deps{Bus: bus})
	s, _, tok, csrf := authedServer(t, deps)
	_ = pid

	sub, cancel := bus.Subscribe()
	defer cancel()

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/check", sid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("check = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Running bool `json:"running"`
		Total   int  `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Running || got.Total != 1 {
		t.Fatalf("snapshot = %+v, want running=true total=1", got)
	}

	waitForEvent(t, sub, "scan_finished", time.Second)

	if len(chk.servicesFreshCalls) != 1 || len(chk.servicesFreshCalls[0]) != 1 || chk.servicesFreshCalls[0][0] != sid {
		t.Fatalf("servicesFreshCalls = %+v, want a single call with [%d]", chk.servicesFreshCalls, sid)
	}
}

// TestCheckBusyReturns409 asserts a service check started while another
// scan-run is already in flight is rejected with 409, not queued or dropped.
func TestCheckBusyReturns409(t *testing.T) {
	db, _, sids := seedProjectServices(t, 1)
	sid := sids[0]
	bc := &blockingChecker{release: make(chan struct{}), started: make(chan struct{})}
	deps := mergeDeps(actionDeps(db, &fakeEngine{}, &fakeChecker{}), Deps{Checker: bc, Bus: NewBus()})
	s, _, tok, csrf := authedServer(t, deps)

	first := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/check", sid), nil), tok, csrf)
	firstRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("first check = %d, want 202", firstRec.Code)
	}
	<-bc.started

	second := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/check", sid), nil), tok, csrf)
	secondRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusConflict {
		t.Fatalf("second check = %d, want 409; body=%s", secondRec.Code, secondRec.Body.String())
	}
	close(bc.release)
}
