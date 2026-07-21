package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// waitForEvent blocks on sub until an event of the given type arrives, or
// fails the test after timeout. It skips past unrelated events (e.g.
// scan_progress) published before the one under test.
func waitForEvent(t *testing.T, sub <-chan Event, wantType string, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-sub:
			if ev.Type == wantType {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %q event within %s", wantType, timeout)
			return Event{}
		}
	}
}

// TestScanAllStampsLastCheckAndPublishes: s.scan is built inside New from the
// deps present at construction, so Checker/Bus/Settings must be passed into
// authedServer directly, a post-construction mergeDeps never reaches the
// already-built ScanRunner.
func TestScanAllStampsLastCheckAndPublishes(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	chk := &fakeChecker{}
	bus := NewBus()
	deps := mergeDeps(settingsDeps(t, db), Deps{Checker: chk, Bus: bus})
	s, _, tok, csrf := authedServer(t, deps)

	sub, cancel := bus.Subscribe()
	defer cancel()

	before := time.Now().UTC()
	req := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/scan = %d body=%s", rec.Code, rec.Body.String())
	}

	waitForEvent(t, sub, "scanned", time.Second)

	if len(chk.servicesFreshCalls) != 1 {
		t.Fatalf("CheckServicesFresh called %d times, want 1", len(chk.servicesFreshCalls))
	}

	last, err := s.deps.Settings.Get("last_check_all")
	if err != nil {
		t.Fatalf("last_check_all not set: %v", err)
	}
	stamped, err := time.Parse(time.RFC3339, last)
	if err != nil {
		t.Fatalf("last_check_all = %q not RFC3339: %v", last, err)
	}
	// last_check_all is RFC3339 (second precision); truncate `before` the same
	// way so a same-second stamp doesn't false-negative here.
	if stamped.Before(before.Truncate(time.Second)) {
		t.Fatalf("last_check_all %v is before the request started %v", stamped, before)
	}
}

// TestScanAllStartsAndReports202 is the plain happy path: no body, nothing in
// flight, so the request is accepted and the run starts immediately.
func TestScanAllStartsAndReports202(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	deps := mergeDeps(settingsDeps(t, db), Deps{Checker: &fakeChecker{}, Bus: NewBus()})
	s, _, tok, csrf := authedServer(t, deps)

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
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
}

// TestScanStatusSnapshot asserts a fresh server reports a not-running snapshot.
func TestScanStatusSnapshot(t *testing.T) {
	s, _, tok, csrf := authedServer(t, Deps{})

	req := authReq(httptest.NewRequest(http.MethodGet, "/api/scan", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Running {
		t.Fatalf("fresh server should report running=false")
	}
}

// TestScanAllBusyReturns409 drives the ScanRunner with a blocking checker so a
// run stays in flight, then asserts a second POST /api/scan returns 409.
func TestScanAllBusyReturns409(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	bc := &blockingChecker{release: make(chan struct{}), started: make(chan struct{})}
	deps := mergeDeps(settingsDeps(t, db), Deps{Checker: bc, Bus: NewBus()})
	s, _, tok, csrf := authedServer(t, deps)

	first := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
	firstRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("first POST /api/scan = %d, want 202", firstRec.Code)
	}
	<-bc.started

	second := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
	secondRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusConflict {
		t.Fatalf("second POST /api/scan = %d, want 409; body=%s", secondRec.Code, secondRec.Body.String())
	}
	close(bc.release)
}

// TestScanReopenFlagByScope asserts the reopen flag passed to
// Checker.CheckServicesFresh matches the old per-endpoint semantics: scoped
// (service/project) manual checks reopen rolled_back updates (the "look
// again" gesture); an all-services sweep must not (reopening there would make
// every just-rolled-back update auto-apply-eligible again).
func TestScanReopenFlagByScope(t *testing.T) {
	t.Run("service scope reopens", func(t *testing.T) {
		db, _, svcIDs := seedProjectServices(t, 1)
		chk := &fakeChecker{}
		bus := NewBus()
		deps := mergeDeps(settingsDeps(t, db), Deps{Checker: chk, Bus: bus})
		s, _, tok, csrf := authedServer(t, deps)

		sub, cancel := bus.Subscribe()
		defer cancel()

		req := authReq(httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/services/%d/check", svcIDs[0]), nil), tok, csrf)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST check = %d body=%s", rec.Code, rec.Body.String())
		}
		waitForEvent(t, sub, "scan_finished", time.Second)

		if !chk.servicesFreshReopen {
			t.Fatalf("service scope: reopen = false, want true")
		}
	})

	t.Run("project scope reopens", func(t *testing.T) {
		db, projectID, _ := seedProjectServices(t, 1)
		chk := &fakeChecker{}
		bus := NewBus()
		deps := mergeDeps(settingsDeps(t, db), Deps{Checker: chk, Bus: bus})
		s, _, tok, csrf := authedServer(t, deps)

		sub, cancel := bus.Subscribe()
		defer cancel()

		body, _ := json.Marshal(map[string]int64{"project_id": projectID})
		req := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", bytes.NewReader(body)), tok, csrf)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /api/scan (project) = %d body=%s", rec.Code, rec.Body.String())
		}
		waitForEvent(t, sub, "scan_finished", time.Second)

		if !chk.servicesFreshReopen {
			t.Fatalf("project scope: reopen = false, want true")
		}
	})

	t.Run("all scope does not reopen", func(t *testing.T) {
		db, _, _ := seedProjectServices(t, 1)
		chk := &fakeChecker{}
		bus := NewBus()
		deps := mergeDeps(settingsDeps(t, db), Deps{Checker: chk, Bus: bus})
		s, _, tok, csrf := authedServer(t, deps)

		sub, cancel := bus.Subscribe()
		defer cancel()

		req := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /api/scan (all) = %d body=%s", rec.Code, rec.Body.String())
		}
		waitForEvent(t, sub, "scan_finished", time.Second)

		if chk.servicesFreshReopen {
			t.Fatalf("all scope: reopen = true, want false")
		}
	})
}

// TestScanAllFinishesDespiteCheckerError proves a background checker error is
// swallowed rather than hanging or crashing the run: the endpoint still 202s
// and the run still reaches scan_finished (ScanRunner discards the
// CheckServicesFresh error and stamps last_check_all regardless; see
// scanrun.go's run()).
func TestScanAllFinishesDespiteCheckerError(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	chk := &fakeChecker{servicesFreshErr: errors.New("registry down")}
	bus := NewBus()
	deps := mergeDeps(settingsDeps(t, db), Deps{Checker: chk, Bus: bus})
	s, _, tok, csrf := authedServer(t, deps)

	sub, cancel := bus.Subscribe()
	defer cancel()

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/scan = %d, want 202", rec.Code)
	}

	waitForEvent(t, sub, "scan_finished", time.Second)
}
