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

// fakeChecker signals which services were checked. calledSync is set after a
// short sleep so tests can deterministically assert the handler awaited
// CheckServiceFresh before writing its response (a detached goroutine would race
// this and fail intermittently at best).
type fakeChecker struct {
	called       chan int64
	calledSync   bool
	checkAllErr  error
	checkAllCall int
}

func (f *fakeChecker) CheckServiceFresh(_ context.Context, id int64) error {
	time.Sleep(10 * time.Millisecond)
	f.calledSync = true
	if f.called != nil {
		f.called <- id
	}
	return nil
}

func (f *fakeChecker) CheckAll(_ context.Context) error {
	f.checkAllCall++
	return f.checkAllErr
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

// TestCheckIsSynchronous asserts the handler awaits CheckService before
// writing its response, returns 200 {"status":"checked"}, and routes the
// correct service id to the checker. fakeChecker.calledSync is only set after
// a 10ms sleep inside CheckService, so if the handler returned before the call
// completed (the old detached-goroutine behavior) this flag would still be
// false when the response comes back.
func TestCheckIsSynchronous(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	chk := &fakeChecker{called: make(chan int64, 1)}
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, chk))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/check", sid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("check = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"checked"`) {
		t.Fatalf("check body = %s, want status=checked", rec.Body.String())
	}
	if !chk.calledSync {
		t.Fatal("CheckService was not awaited before the handler wrote its response")
	}
	// The handler has returned, so a synchronous CheckService already sent the
	// id, no wait needed. default (not time.After) proves it happened before return.
	select {
	case got := <-chk.called:
		if got != sid {
			t.Fatalf("checked service %d, want %d", got, sid)
		}
	default:
		t.Fatal("checker was not invoked with the service id")
	}
}
