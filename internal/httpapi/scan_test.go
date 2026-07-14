package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScanAllStampsLastCheckAndPublishes(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	chk := &fakeChecker{}
	bus := NewBus()
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))
	s.deps = mergeDeps(s.deps, Deps{Checker: chk, Bus: bus})

	sub, cancel := bus.Subscribe()
	defer cancel()

	before := time.Now().UTC()
	req := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/scan = %d body=%s", rec.Code, rec.Body.String())
	}
	if chk.checkAllCall != 1 {
		t.Fatalf("CheckAll called %d times, want 1", chk.checkAllCall)
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

	select {
	case ev := <-sub:
		if ev.Type != "scanned" {
			t.Fatalf("event type = %q, want scanned", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no event published on /api/scan")
	}
}

func TestScanAllCheckerErrorSkipsStamp(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	chk := &fakeChecker{checkAllErr: errors.New("registry down")}
	s.deps = mergeDeps(s.deps, settingsDeps(t, db))
	s.deps = mergeDeps(s.deps, Deps{Checker: chk})

	req := authReq(httptest.NewRequest(http.MethodPost, "/api/scan", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("POST /api/scan = %d, want 502", rec.Code)
	}
	if _, err := s.deps.Settings.Get("last_check_all"); err == nil {
		t.Fatal("last_check_all was stamped despite CheckAll failing")
	}
}
