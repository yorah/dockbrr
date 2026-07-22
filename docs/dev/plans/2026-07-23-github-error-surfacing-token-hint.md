# GitHub Error Surfacing with Token Hint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface GitHub API failures (chiefly the unauthenticated rate limit) to the user on the self-update check and manual scan paths, with an "add a GitHub token" hint on rate-limits.

**Architecture:** Backend classifies a GitHub rate-limit (403/429 + `X-RateLimit-Remaining: 0`) as a typed sentinel on both paths. The self-update checker carries a soft `FetchErr` the read endpoint turns into an `error_kind` in its JSON. The scan sweep bubbles a `rateLimited` bool that a manual run publishes on the `scan_finished` SSE event. The frontend renders a toast (+ inline line for self-update) with copy chosen by kind.

**Tech Stack:** Go 1.26 (CGO-free), net/http, SQLite settings store, SSE bus; React + TypeScript + TanStack Query, vitest + msw + testing-library, sonner toasts.

## Global Constraints

- `CGO_ENABLED=0` must stay buildable; no new cgo deps.
- Rate-limit hint trigger is **only** HTTP `403` or `429` with header `X-RateLimit-Remaining: 0`. Every other GitHub failure is a generic "unreachable" error, never the token hint.
- Self-update read endpoint stays **HTTP 200** on a GitHub error (best-effort contract); errors ride in the JSON body, never a 5xx.
- Do NOT change `Check()`/`CheckFresh()`'s existing `(Result, error)` contract: a stale-cache serve still returns a `nil` top-level error, so `enqueueSelfUpdate` keeps enqueuing off a cached verdict during a transient outage.
- Scheduled sweeps never toast; only a manual scan (`Start`) publishes the rate-limit flag.
- Hint copy is NOT gated on whether a token is already set (a `github_token_set` signal exists in the Settings API but is intentionally unused here, matching the existing `web/src/components/Changelog.tsx` precedent). Rate-limit hint links to `/settings/registries`.
- No em-dashes in prose/comments/copy.
- TS typecheck via `cd web && npm run typecheck` (NOT `npx tsc`). Full check: `mise run check`.

## Task Summary

| # | Task | Deps | Model | Reviewer | Plan section |
|---|------|------|-------|----------|--------------|
| 1 | selfupdate rate-limit classification + `FetchErr` | - | sonnet | sonnet | Task 1 |
| 2 | `handleSelfUpdate` emits `error_kind` | 1 | sonnet | sonnet | Task 2 |
| 3 | scan sweep bubbles `rateLimited` | - | sonnet | sonnet | Task 3 |
| 4 | `scan_finished.rate_limited` event, manual-only | 3 | opus | opus | Task 4 |
| 5 | frontend self-update toast + inline line | 2 | sonnet | sonnet | Task 5 |
| 6 | frontend scan rate-limit toast | 4 | sonnet | sonnet | Task 6 |

Final whole-branch review: opus.

---

## Task 1: selfupdate rate-limit classification + `FetchErr`

**Files:**
- Modify: `internal/selfupdate/checker.go`
- Test: `internal/selfupdate/checker_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `selfupdate.ErrRateLimited` (`error` sentinel).
  - `selfupdate.Result.FetchErr error` (`json:"-"`), populated on any refresh failure whether or not a stale cache was served.

- [ ] **Step 1: Write the failing tests**

Add `"errors"` to the import block of `internal/selfupdate/checker_test.go`, then append these two tests:

```go
func TestCheckRateLimitNoCacheClassified(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(gh.Close)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if !errors.Is(err, selfupdate.ErrRateLimited) {
		t.Fatalf("want ErrRateLimited top-level error, got %v", err)
	}
	if !errors.Is(res.FetchErr, selfupdate.ErrRateLimited) {
		t.Errorf("FetchErr should carry ErrRateLimited: %+v", res)
	}
}

func TestCheckRateLimitStaleServeStampsFetchErr(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(gh.Close)
	s := newSettings(t)
	seedCache(t, s, "v0.5.0", "https://stale", time.Now().Add(-2*time.Hour).UTC())
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("stale serve must not return a top-level error: %v", err)
	}
	if res.Latest != "v0.5.0" {
		t.Errorf("want stale v0.5.0 served, got %+v", res)
	}
	if !errors.Is(res.FetchErr, selfupdate.ErrRateLimited) {
		t.Errorf("FetchErr should carry ErrRateLimited even when stale served: %+v", res)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/selfupdate/ -run 'TestCheckRateLimit' -v`
Expected: FAIL to compile with `res.FetchErr undefined` and `undefined: selfupdate.ErrRateLimited`.

- [ ] **Step 3: Add the sentinel**

In `internal/selfupdate/checker.go`, after the `const (...)` block (around line 22), add:

```go
// ErrRateLimited signals that the GitHub releases/latest request was rejected for
// primary rate-limit exhaustion (HTTP 403/429 with X-RateLimit-Remaining: 0). The
// read endpoint surfaces it as the "add a GitHub token" hint; every other non-200
// stays a generic error.
var ErrRateLimited = errors.New("selfupdate: github rate limited")
```

(`errors` is already imported.)

- [ ] **Step 4: Add the `FetchErr` field**

In the `Result` struct, after the `CheckedAt` field, add:

```go
	// FetchErr is the soft signal for the read endpoint: it is set whenever a
	// refresh attempt failed, whether a stale cache was then served (top-level
	// error nil) or there was nothing to serve (top-level error non-nil). Never
	// serialized; handleSelfUpdate classifies it into an error_kind.
	FetchErr error `json:"-"`
```

- [ ] **Step 5: Classify the rate-limit in `fetchLatest`**

Replace the status guard in `fetchLatest` (currently `if resp.StatusCode != http.StatusOK { return "", "", fmt.Errorf(...) }`) with:

```go
	if resp.StatusCode != http.StatusOK {
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
			resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return "", "", ErrRateLimited
		}
		return "", "", fmt.Errorf("github releases/latest: status %d", resp.StatusCode)
	}
```

- [ ] **Step 6: Stamp `FetchErr` in `refresh`**

Replace the error branch of `refresh` (the `if err != nil { ... }` block) with:

```go
	if err != nil {
		if haveCache {
			// Best-effort: serve stale and leave checked_at untouched so the
			// next request retries GitHub rather than waiting out the TTL. Stamp
			// FetchErr so the read endpoint can still surface "couldn't refresh".
			logger.Debugf("selfupdate: github fetch failed, serving stale cache: %v", err)
			res := c.result(tag, url, checkedAt)
			res.FetchErr = err
			return res, nil
		}
		logger.Debugf("selfupdate: github fetch failed, no cache: %v", err)
		return Result{Current: c.current, FetchErr: err}, err
	}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/selfupdate/ -v`
Expected: PASS (new tests + all existing selfupdate tests).

- [ ] **Step 8: Commit**

```bash
git add internal/selfupdate/checker.go internal/selfupdate/checker_test.go
git commit -m "feat(selfupdate): classify github rate-limit and stamp FetchErr"
```

---

## Task 2: `handleSelfUpdate` emits `error_kind`

**Files:**
- Modify: `internal/httpapi/selfupdate.go:17-40` (`handleSelfUpdate`)
- Test: `internal/httpapi/selfupdate_test.go`

**Interfaces:**
- Consumes: `selfupdate.Result.FetchErr`, `selfupdate.ErrRateLimited` (Task 1).
- Produces: `/api/updates/self` JSON gains optional `error_kind` (`"rate_limited"` | `"unreachable"`) and `error` (raw string) fields. HTTP stays 200.

- [ ] **Step 1: Write the failing tests**

Append to `internal/httpapi/selfupdate_test.go`:

```go
func TestSelfUpdateEndpointRateLimited(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(gh.Close)

	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, selfUpdateDeps(t, db, gh.URL, "0.4.2"))

	rec := authedGet(t, srv, "/api/updates/self?force=true", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("best-effort contract: want 200, got %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["error_kind"] != "rate_limited" {
		t.Errorf("want error_kind rate_limited, got %v", out)
	}
	if out["update_available"] != false {
		t.Errorf("rate-limited with no cache must not claim an update: %v", out)
	}
}

func TestSelfUpdateEndpointUnreachable(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)

	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, selfUpdateDeps(t, db, gh.URL, "0.4.2"))

	rec := authedGet(t, srv, "/api/updates/self?force=true", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["error_kind"] != "unreachable" {
		t.Errorf("non-rate-limit github error should be unreachable, got %v", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/ -run 'TestSelfUpdateEndpoint(RateLimited|Unreachable)' -v`
Expected: FAIL (`error_kind` absent from body).

- [ ] **Step 3: Emit `error_kind` in the handler**

In `internal/httpapi/selfupdate.go`, `handleSelfUpdate`, after the `checked_at` block and before `writeJSON(...)`, add:

```go
	if res.FetchErr != nil {
		if errors.Is(res.FetchErr, selfupdate.ErrRateLimited) {
			out["error_kind"] = "rate_limited"
		} else {
			out["error_kind"] = "unreachable"
		}
		out["error"] = res.FetchErr.Error()
	}
```

(`errors` and `selfupdate` are already imported.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ -run 'TestSelfUpdate' -v`
Expected: PASS (new + existing self-update endpoint tests).

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/selfupdate.go internal/httpapi/selfupdate_test.go
git commit -m "feat(httpapi): surface github error_kind on self-update check"
```

---

## Task 3: scan sweep bubbles `rateLimited`

**Files:**
- Modify: `internal/scan/scan.go`
- Modify: `internal/httpapi/server.go:32-38` (`Checker` interface)
- Modify: `internal/httpapi/scanrun.go:148` (caller)
- Modify (compile fixes): `internal/httpapi/scanrun_test.go:76,178`, `internal/httpapi/actions_test.go:40`
- Test: `internal/scan/scan_test.go`

**Interfaces:**
- Consumes: `changelog.ErrRateLimited` (already used in scan.go).
- Produces:
  - `Scanner.CheckServicesFresh(ctx, ids, reopen, onDone) (bool, error)` — first return is `true` when any changelog resolve during the sweep hit the GitHub rate limit.
  - Exported `Scanner.CheckService(ctx, id) error` and `Scanner.CheckServiceFresh(ctx, id) error` keep their `error`-only signatures (thin wrappers over new internal `(bool, error)` helpers).
  - httpapi `Checker` interface method `CheckServicesFresh` now returns `(bool, error)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/scan/scan_test.go`:

```go
func TestCheckServicesFreshBubblesRateLimited(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3", CurrentDigest: "sha256:old",
	})
	updates := store.NewUpdates(db)
	uid, _ := updates.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})

	det := fakeDetector{upd: &store.Update{ID: uid, ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0"}}
	cl := &fakeChangelog{err: changelog.ErrRateLimited}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

	rateLimited, err := s.CheckServicesFresh(context.Background(), []int64{sid}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rateLimited {
		t.Fatal("sweep should report rateLimited when a changelog resolve is rate-limited")
	}
}

func TestCheckServicesFreshNoRateLimitWhenClean(t *testing.T) {
	s, ids := newScannerWithServices(t, 2) // up-to-date services, no drift
	rateLimited, err := s.CheckServicesFresh(context.Background(), ids, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rateLimited {
		t.Fatal("clean sweep must not report rateLimited")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scan/ -run 'TestCheckServicesFresh(BubblesRateLimited|NoRateLimitWhenClean)' -v`
Expected: FAIL to compile: `assignment mismatch: 2 variables but s.CheckServicesFresh returns 1 value`.

- [ ] **Step 3: Convert `ensureCurrentChangelog` to report rate-limit**

In `internal/scan/scan.go`, change the signature and both rate-limit-aware branches of `ensureCurrentChangelog`. New signature line:

```go
func (s *Scanner) ensureCurrentChangelog(ctx context.Context, svc store.Service) (rateLimited bool, err error) {
```

Update every `return` in this function to include the bool: the guard/error returns become `return false, nil` / `return false, derr` / `return false, serr` / `return false, herr` / `return false, err` exactly as the current single-value returns map (guards and store errors carry `false`). Then change its final changelog `switch` to:

```go
	text, url, err := s.changelog.Resolve(ctx, row, remote)
	switch {
	case errors.Is(err, changelog.ErrRateLimited):
		if serr := s.updates.SetChangelogStatus(id, "rate_limited"); serr != nil {
			logger.Errorf("scan: persist changelog status (current row %d): %v", id, serr)
		}
		return true, nil
	case err != nil:
		return false, err
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(id, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (current row %d): %v", id, serr)
		}
	}
	return false, nil
```

- [ ] **Step 4: Split `CheckService` into wrapper + internal `checkService`**

Replace the current `CheckService` function with a thin wrapper plus the internal worker (the worker body is the old `CheckService` body, adjusted to return the bool):

```go
// CheckService detects drift for one service and, on a fresh update, resolves +
// persists its changelog. A changelog miss/failure is non-fatal.
func (s *Scanner) CheckService(ctx context.Context, serviceID int64) error {
	_, err := s.checkService(ctx, serviceID)
	return err
}

// checkService is CheckService's internal form: it additionally reports whether a
// changelog resolve returned changelog.ErrRateLimited, so a sweep can surface an
// aggregate "add a token" hint.
func (s *Scanner) checkService(ctx context.Context, serviceID int64) (rateLimited bool, err error) {
	svc, err := s.services.Get(serviceID)
	if err != nil {
		return false, err
	}
	logger.Debugf("scan: checking service %d (%s) ref=%s", svc.ID, svc.Name, svc.ImageRef)
	upd, err := s.detector.Detect(ctx, svc)
	if err != nil {
		return false, err
	}
	if upd == nil {
		logger.Debugf("scan: service %d (%s) up to date", svc.ID, svc.Name)
		s.mu.Lock()
		delete(s.notifiedTo, serviceID)
		s.mu.Unlock()
		rl, cerr := s.ensureCurrentChangelog(ctx, svc)
		if cerr != nil {
			logger.Errorf("scan: ensure current changelog (service %d (%s)): %v", svc.ID, svc.Name, cerr)
		}
		return rl, nil // up-to-date / unmonitorable
	}
	logger.Infof("scan: update available service %d (%s): %s -> %s [%s]",
		svc.ID, svc.Name, refLabel(upd.FromVersion, upd.FromDigest), refLabel(upd.ToVersion, upd.ToDigest), upd.Severity)
	if s.notify != nil && s.markNotified(serviceID, upd.ToDigest) {
		s.notify(serviceID)
	}

	var labels map[string]string
	repo, _ := detect.SplitRef(svc.ImageRef)
	if img, gerr := s.images.GetByDigest(repo, upd.ToDigest); gerr == nil && img.Labels != "" {
		_ = json.Unmarshal([]byte(img.Labels), &labels)
	}
	remote := registry.RemoteImage{Ref: svc.ImageRef, Digest: upd.ToDigest, Labels: labels}

	text, url, err := s.changelog.Resolve(ctx, *upd, remote)
	switch {
	case errors.Is(err, changelog.ErrRateLimited):
		if serr := s.updates.SetChangelogStatus(upd.ID, "rate_limited"); serr != nil {
			logger.Errorf("scan: persist changelog status (update %d): %v", upd.ID, serr)
		}
		return true, nil
	case err != nil:
		logger.Errorf("scan: changelog resolve (service %d (%s)): %v", serviceID, svc.Name, err)
	case text != "" || url != "":
		if serr := s.updates.SetChangelog(upd.ID, url, text); serr != nil {
			logger.Errorf("scan: persist changelog (update %d): %v", upd.ID, serr)
		}
	}
	return false, nil
}
```

- [ ] **Step 5: Split `CheckServiceFresh` into wrapper + internal `checkServiceFresh`**

Replace the current `CheckServiceFresh` with:

```go
// CheckServiceFresh runs CheckService, first lifting the rolled_back suppression:
// a manual check is the explicit "look again" gesture.
func (s *Scanner) CheckServiceFresh(ctx context.Context, serviceID int64) error {
	_, err := s.checkServiceFresh(ctx, serviceID)
	return err
}

// checkServiceFresh is CheckServiceFresh's internal form, additionally reporting
// whether a changelog resolve hit the GitHub rate limit.
func (s *Scanner) checkServiceFresh(ctx context.Context, serviceID int64) (rateLimited bool, err error) {
	if s.updates != nil {
		if n, rerr := s.updates.ReopenRolledBack(serviceID); rerr != nil {
			logger.Errorf("scan: reopen rolled-back updates (service %d): %v", serviceID, rerr)
		} else if n > 0 {
			logger.Infof("scan: service %d manual check reopened %d rolled-back update(s)", serviceID, n)
		}
	}
	return s.checkService(ctx, serviceID)
}
```

- [ ] **Step 6: Make `CheckServicesFresh` accumulate + return the bool**

Replace `CheckServicesFresh`'s signature and loop body with:

```go
func (s *Scanner) CheckServicesFresh(ctx context.Context, ids []int64, reopen bool, onDone func(done, total int)) (bool, error) {
	total := len(ids)
	rateLimited := false
	for i, id := range ids {
		if ctx.Err() != nil {
			break // aborted or timed out: stop the sweep, keep partial results
		}
		var (
			rl   bool
			cerr error
		)
		if reopen {
			rl, cerr = s.checkServiceFresh(ctx, id)
		} else {
			rl, cerr = s.checkService(ctx, id)
		}
		if cerr != nil {
			logger.Errorf("scan: check service %d: %v", id, cerr)
		}
		if rl {
			rateLimited = true
		}
		if onDone != nil {
			onDone(i+1, total)
		}
	}
	return rateLimited, nil
}
```

Keep the existing doc comment above the function (update its first line to mention it also reports whether the sweep hit the GitHub rate limit).

- [ ] **Step 7: Update the httpapi `Checker` interface**

In `internal/httpapi/server.go`, change the interface method to:

```go
	CheckServicesFresh(ctx context.Context, ids []int64, reopen bool, onDone func(done, total int)) (bool, error)
```

- [ ] **Step 8: Update the caller in `scanrun.go`**

In `internal/httpapi/scanrun.go` `execute`, change the call (line ~148) from `_ = sr.checker.CheckServicesFresh(...)` to capture both returns. For now (Task 3 keeps behavior identical) discard the bool; Task 4 wires it:

```go
	_, _ = sr.checker.CheckServicesFresh(ctx, ids, reopen, func(done, total int) {
		sr.mu.Lock()
		sr.state.Done = done
		sr.mu.Unlock()
		sr.publish(Event{Type: "scan_progress", Done: done, Total: total})
	})
```

- [ ] **Step 9: Update the three test doubles to the new signature**

`internal/httpapi/scanrun_test.go` `blockingChecker.CheckServicesFresh` (line ~76): change signature to `(...) (bool, error)` and its `return nil` (if any) / trailing return to `return false, nil`.

`internal/httpapi/scanrun_test.go` `abortableChecker.CheckServicesFresh` (line ~178): same, return `false, nil` (or `false, ctx.Err()` if it currently returns the ctx error; preserve existing error value as the second return).

`internal/httpapi/actions_test.go` `fakeChecker.CheckServicesFresh` (line ~40): change signature to `(...) (bool, error)`, keep its recording side effects, end with `return false, nil`.

- [ ] **Step 10: Run tests to verify they pass**

Run: `go test ./internal/scan/ ./internal/httpapi/ -v`
Expected: PASS (new scan tests + all existing scan and httpapi tests, doubles compile).

- [ ] **Step 11: Commit**

```bash
git add internal/scan/scan.go internal/scan/scan_test.go internal/httpapi/server.go internal/httpapi/scanrun.go internal/httpapi/scanrun_test.go internal/httpapi/actions_test.go
git commit -m "feat(scan): bubble github rate-limit from the sweep"
```

---

## Task 4: `scan_finished.rate_limited` event, manual-only

**Files:**
- Modify: `internal/httpapi/events_stream.go:19-28` (`Event` struct)
- Modify: `internal/httpapi/scanrun.go` (`Start`, `RunScheduled`, `execute`)
- Test: `internal/httpapi/scanrun_test.go`

**Interfaces:**
- Consumes: `Checker.CheckServicesFresh` `(bool, error)` (Task 3).
- Produces: `Event` gains `RateLimited bool` (`json:"rate_limited,omitempty"`). A `scan_finished` event carries `rate_limited: true` only when a **manual** sweep hit the GitHub rate limit.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/scanrun_test.go`. This uses a checker double that reports `rateLimited=true`; if the existing doubles do not fit, add this local one:

```go
// rlChecker reports a GitHub rate-limit from its sweep, for asserting the
// scan_finished event carries rate_limited only on a manual run.
type rlChecker struct{}

func (rlChecker) CheckServicesFresh(_ context.Context, ids []int64, _ bool, onDone func(done, total int)) (bool, error) {
	if onDone != nil {
		onDone(len(ids), len(ids))
	}
	return true, nil
}

func TestManualScanPublishesRateLimited(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	svcs, settings := scanRunStores(t) // see note below
	sr := NewScanRunner(rlChecker{}, svcs, settings, bus)

	if _, err := sr.Start("all", 0, 0); err != nil {
		t.Fatalf("start: %v", err)
	}

	var got bool
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == "scan_finished" {
				got = e.RateLimited
				goto done
			}
		case <-deadline:
			t.Fatal("no scan_finished event")
		}
	}
done:
	if !got {
		t.Fatal("manual sweep that hit the rate limit must set scan_finished.rate_limited")
	}
}

func TestScheduledScanDoesNotPublishRateLimited(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	svcs, settings := scanRunStores(t)
	sr := NewScanRunner(rlChecker{}, svcs, settings, bus)

	if ok := sr.RunScheduled(context.Background()); !ok {
		t.Fatal("scheduled run should complete")
	}
	// Drain events; scan_finished must NOT carry rate_limited for a scheduled run.
	for {
		select {
		case e := <-ch:
			if e.Type == "scan_finished" && e.RateLimited {
				t.Fatal("scheduled sweep must not set rate_limited")
			}
			if e.Type == "scan_finished" {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no scan_finished event")
		}
	}
}
```

Note on `scanRunStores`: reuse whatever helper the existing scanrun tests use to build a `*store.Services` and `*store.Settings` over a temp DB with at least one service (grep `NewScanRunner(` in `scanrun_test.go` for the existing setup and mirror it; if a shared helper exists, call it, otherwise inline the two-line store setup used there). The double ignores the ids, so the store only needs `services.List()` to return at least one row so `resolve("all")` yields a non-empty id set.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run 'Test(Manual|Scheduled)Scan' -v`
Expected: FAIL to compile: `e.RateLimited undefined` and `rlChecker` does not implement `Checker` (signature) until built, and `RunScheduled`/`execute` not yet threading the flag.

- [ ] **Step 3: Add the `RateLimited` field to `Event`**

In `internal/httpapi/events_stream.go`, in the `Event` struct after `Total`:

```go
	// RateLimited rides on a scan_finished event from a MANUAL sweep whose
	// changelog resolution hit the GitHub rate limit, so the UI can show the
	// "add a token" hint once per run. Scheduled sweeps never set it.
	RateLimited bool `json:"rate_limited,omitempty"`
```

- [ ] **Step 4: Thread `manual` through `execute` and capture the flag**

In `internal/httpapi/scanrun.go`:

Change `execute`'s signature to add a trailing `manual bool`:

```go
func (sr *ScanRunner) execute(parent context.Context, scope string, ids []int64, manual bool) bool {
```

Capture the sweep's rate-limit return (replace the `_, _ =` from Task 3 Step 8):

```go
	rateLimited, _ := sr.checker.CheckServicesFresh(ctx, ids, reopen, func(done, total int) {
		sr.mu.Lock()
		sr.state.Done = done
		sr.mu.Unlock()
		sr.publish(Event{Type: "scan_progress", Done: done, Total: total})
	})
```

Change the final `scan_finished` publish (currently `sr.publish(Event{Type: "scan_finished"})`) to:

```go
	sr.publish(Event{Type: "scan_finished", RateLimited: manual && rateLimited})
```

- [ ] **Step 5: Pass `manual` from both callers**

In `Start`, the goroutine launch becomes:

```go
	go sr.execute(context.Background(), scope, ids, true)
```

In `RunScheduled`, the inline call becomes:

```go
	return sr.execute(ctx, "all", ids, false)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS (new event tests + all existing httpapi tests).

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/events_stream.go internal/httpapi/scanrun.go internal/httpapi/scanrun_test.go
git commit -m "feat(httpapi): publish rate_limited on manual scan_finished"
```

---

## Task 5: frontend self-update toast + inline line

**Files:**
- Modify: `web/src/api/types.ts:110-116` (`SelfUpdate`)
- Modify: `web/src/hooks/mutations.ts:213-223` (`useCheckForUpdates`)
- Modify: `web/src/components/settings/ApplicationSettings.tsx`
- Test: `web/src/hooks/mutations.test.tsx`, `web/src/components/settings/ApplicationSettings.test.tsx`

**Interfaces:**
- Consumes: `/api/updates/self` body fields `error`, `error_kind` (Task 2).
- Produces:
  - `SelfUpdate` type += `error?: string; error_kind?: "rate_limited" | "unreachable"`.
  - Exported const `SELF_UPDATE_ERROR_COPY` (a small helper mapping `error_kind` to a message string) so the toast and the inline line share one copy source. Place it in `web/src/lib/selfUpdate.ts`.

- [ ] **Step 1: Extend the `SelfUpdate` type**

In `web/src/api/types.ts`, add two fields to the `SelfUpdate` interface:

```ts
export interface SelfUpdate {
  current?: string;
  latest?: string;
  html_url?: string;
  update_available: boolean;
  checked_at?: string;
  /** Set when the last GitHub check failed; drives the token hint. */
  error?: string;
  error_kind?: "rate_limited" | "unreachable";
}
```

- [ ] **Step 2: Add shared copy in `lib/selfUpdate.ts`**

Append to `web/src/lib/selfUpdate.ts`:

```ts
// selfUpdateErrorMessage maps a check error_kind to user-facing copy. rate_limited
// gets the "add a token" hint; anything else is a generic unreachable message.
export function selfUpdateErrorMessage(kind: string | undefined): string | null {
  if (kind === "rate_limited") {
    return "GitHub rate limit reached. Add a GitHub token in Settings to raise the limit.";
  }
  if (kind === "unreachable") {
    return "Couldn't reach GitHub to check for updates. Try again shortly.";
  }
  return null;
}
```

- [ ] **Step 3: Write the failing mutation test**

Append to `web/src/hooks/mutations.test.tsx` (inside a new `describe("useCheckForUpdates", ...)` block; the file already mocks `sonner` and defines `wrapper`):

```ts
describe("useCheckForUpdates", () => {
  test("toasts the token hint when the check is rate-limited", async () => {
    server.use(
      http.get("/api/updates/self", () =>
        HttpResponse.json({ update_available: false, error_kind: "rate_limited", error: "rate limited" }),
      ),
    );
    const { W } = wrapper();
    const { result } = renderHook(() => useCheckForUpdates(), { wrapper: W });
    result.current.mutate();
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect((toast.error as any).mock.calls[0][0]).toMatch(/GitHub token/);
  });

  test("no toast on a clean check", async () => {
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json({ update_available: false })),
    );
    const { W } = wrapper();
    const { result } = renderHook(() => useCheckForUpdates(), { wrapper: W });
    result.current.mutate();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(toast.error).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 4: Run the mutation test to verify it fails**

Run: `cd web && npx vitest run src/hooks/mutations.test.tsx -t useCheckForUpdates`
Expected: FAIL (`toast.error` not called; no error handling yet).

- [ ] **Step 5: Wire the toast in `useCheckForUpdates`**

In `web/src/hooks/mutations.ts`, import the helper at the top with the other `@/lib` imports:

```ts
import { selfUpdateErrorMessage } from "@/lib/selfUpdate";
```

Change the `onSuccess` of `useCheckForUpdates` to:

```ts
    onSuccess: (data) => {
      qc.setQueryData(keys.selfUpdate, data);
      clearDismissedUpdate();
      const msg = selfUpdateErrorMessage(data.error_kind);
      if (msg) notify.error(msg);
    },
```

Confirm `notify` is imported in `mutations.ts`; if not, add `import { notify } from "@/lib/notify";`.

- [ ] **Step 6: Run the mutation test to verify it passes**

Run: `cd web && npx vitest run src/hooks/mutations.test.tsx -t useCheckForUpdates`
Expected: PASS.

- [ ] **Step 7: Write the failing settings test**

Append to `web/src/components/settings/ApplicationSettings.test.tsx` a test that renders with a rate-limited self-update verdict and asserts the inline hint + settings link. Mirror the existing render harness in that file (grep it for how it stubs `useSelfUpdate`/msw and renders `<ApplicationSettings />`). The assertion:

```ts
test("shows a token hint line when the self-update check is rate-limited", async () => {
  server.use(
    http.get("/api/updates/self", () =>
      HttpResponse.json({ update_available: false, error_kind: "rate_limited", error: "rate limited" }),
    ),
  );
  renderApplicationSettings(); // the file's existing render helper
  expect(await screen.findByText(/Add a GitHub token/i)).toBeInTheDocument();
  const link = screen.getByRole("link", { name: /Add a GitHub token/i });
  expect(link).toHaveAttribute("href", "/settings/registries");
});
```

If the file has no shared render helper, render inline with the same `QueryClientProvider` wrapper the other tests in the file use, and ensure `/api/system/info` is stubbed (the Build card needs it) so the component leaves its loading state.

- [ ] **Step 8: Run the settings test to verify it fails**

Run: `cd web && npx vitest run src/components/settings/ApplicationSettings.test.tsx -t "token hint"`
Expected: FAIL (no hint rendered).

- [ ] **Step 9: Render the inline hint in `ApplicationSettings`**

In `web/src/components/settings/ApplicationSettings.tsx`, import the helper near the other imports:

```ts
import { selfUpdateErrorMessage } from "@/lib/selfUpdate";
```

Compute the message alongside `versionSub` (after the `versionSub` declaration):

```ts
  const checkError = selfUpdateErrorMessage(su?.error_kind);
```

Then render it under the `Version` `InfoRow`. Replace the `<Rows>...</Rows>` block inside the Build `SettingsCard` with one that appends the hint line when present:

```tsx
        <Rows>
          <InfoRow label="Version" value={data.version} sub={versionSub} />
          <InfoRow label="Commit" value={commit} sub={data.commit_dirty ? "working tree was dirty at build time" : undefined} />
          <InfoRow label="Build date" value={formatDate(data.build_date)} />
        </Rows>
        {checkError && (
          <p className="mt-2 text-xs text-warning" role="status">
            {checkError}{" "}
            <a href="/settings/registries" className="text-primary hover:underline">
              Add a GitHub token
            </a>
          </p>
        )}
```

Note: `selfUpdateErrorMessage` already ends the rate-limit copy with "Add a GitHub token in Settings to raise the limit."; the trailing link repeats the "Add a GitHub token" phrase as the actionable link. To avoid duplication, the inline copy uses the message text as-is followed by the link; keep the link so the test's `getByRole("link")` passes. (The toast, which cannot host a link, relies on the message text alone.)

- [ ] **Step 10: Run the settings test to verify it passes**

Run: `cd web && npx vitest run src/components/settings/ApplicationSettings.test.tsx`
Expected: PASS (new + existing tests).

- [ ] **Step 11: Typecheck**

Run: `cd web && npm run typecheck`
Expected: no errors.

- [ ] **Step 12: Commit**

```bash
git add web/src/api/types.ts web/src/lib/selfUpdate.ts web/src/hooks/mutations.ts web/src/hooks/mutations.test.tsx web/src/components/settings/ApplicationSettings.tsx web/src/components/settings/ApplicationSettings.test.tsx
git commit -m "feat(web): surface self-update check github error with token hint"
```

---

## Task 6: frontend scan rate-limit toast

**Files:**
- Modify: `web/src/hooks/useEventStream.ts:71-124` (message handler)
- Test: `web/src/hooks/useEventStream.test.tsx`

**Interfaces:**
- Consumes: `scan_finished` SSE event with optional `rate_limited: boolean` (Task 4).
- Produces: a `notify.error` toast on a `scan_finished` event carrying `rate_limited: true`.

- [ ] **Step 1: Write the failing test**

Append to `web/src/hooks/useEventStream.test.tsx` (the file mocks `sonner` and defines `FakeES`, `wrapper`, `__setEventSourceFactory`). Import `toast` if not already imported at the top (`import { toast } from "sonner";`):

```ts
test("scan_finished with rate_limited toasts the token hint", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  const { W } = wrapper();
  renderHook(() => useEventStream(true), { wrapper: W });
  await waitFor(() => expect(FakeES.last).not.toBeNull());
  act(() => FakeES.last!.emit(JSON.stringify({ type: "scan_finished", rate_limited: true })));
  await waitFor(() => expect(toast.error).toHaveBeenCalled());
  expect((toast.error as any).mock.calls[0][0]).toMatch(/GitHub token/);
});

test("scan_finished without rate_limited does not toast", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  const { W } = wrapper();
  renderHook(() => useEventStream(true), { wrapper: W });
  await waitFor(() => expect(FakeES.last).not.toBeNull());
  act(() => FakeES.last!.emit(JSON.stringify({ type: "scan_finished" })));
  expect(toast.error).not.toHaveBeenCalled();
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/hooks/useEventStream.test.tsx -t "rate_limited"`
Expected: FAIL (no toast emitted).

- [ ] **Step 3: Add `rate_limited` to the parsed event shape and toast on it**

In `web/src/hooks/useEventStream.ts`, extend the inline parsed type in `handleMessage`:

```ts
        const ev = JSON.parse(e.data as string) as {
          type: string;
          service_id?: number;
          job_id?: number;
          done?: number;
          total?: number;
          rate_limited?: boolean;
        };
```

In the `case "scan_finished":` block, add the toast after the existing `setScanRun` + invalidations:

```ts
          case "scan_finished":
            setScanRun({ running: false, done: 0, total: 0 });
            void qc.invalidateQueries({ queryKey: keys.updates });
            void qc.invalidateQueries({ queryKey: keys.projects });
            void qc.invalidateQueries({ queryKey: keys.status });
            if (ev.rate_limited) {
              notify.error(
                "GitHub rate limit reached during the scan. Add a GitHub token in Settings to raise the limit.",
              );
            }
            break;
```

(`notify` is already imported in this file.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/hooks/useEventStream.test.tsx`
Expected: PASS (new + existing tests).

- [ ] **Step 5: Typecheck**

Run: `cd web && npm run typecheck`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/hooks/useEventStream.ts web/src/hooks/useEventStream.test.tsx
git commit -m "feat(web): toast github rate-limit hint after a manual scan"
```

---

## Final verification

- [ ] **Full check**

Run: `mise run check`
Expected: `go vet` clean, all Go tests pass, all vitest pass.

- [ ] **Build (static-binary invariant)**

Run: `mise run build`
Expected: builds `./dockbrr` with `CGO_ENABLED=0`, SPA embedded, no errors.

---

## Self-Review

**Spec coverage:**
- Rate-limit classification (spec §Design.1): Task 1 (selfupdate), Task 3 reuses existing `changelog.ErrRateLimited`.
- Self-update backend `FetchErr` + `error_kind` (spec §Design.2): Tasks 1, 2.
- Self-update frontend toast + inline line (spec §Design.2): Task 5.
- Aggregate scan toast: bubble (Task 3), event manual-only (Task 4), toast (Task 6). Matches spec §Design.3 including the scheduled-never-toast and manual-only gating.
- Hint trigger = rate-limit only; other errors generic (spec §Goals): enforced in Task 1 Step 5, Task 2 Step 3, and `selfUpdateErrorMessage`.
- HTTP 200 best-effort contract preserved (spec §Non-goals / Global Constraints): Task 2 keeps `writeJSON(w, http.StatusOK, ...)`.
- No token-set gating (spec §Non-goals): copy is unconditional on rate-limit; `github_token_set` untouched.
- Error-source matrix (spec): manual self-update = toast + inline (Task 5); background poll = inline only, no toast (Task 5 renders from the shared `useSelfUpdate` cache, `useCheckForUpdates` is the only toaster); manual scan = toast (Task 6); scheduled = no toast (Task 4 `manual` gate).

**Placeholder scan:** No TBD/TODO. The two spots that say "grep the existing file for its helper" (Task 4 `scanRunStores`, Task 5 settings render harness) are because those test files' local helpers were not read in full; each gives the exact shape/assertion to produce and the fallback inline setup, so no content is deferred.

**Type consistency:** `error_kind` values `"rate_limited"`/`"unreachable"` match across Task 2 (Go), Task 5 (`SelfUpdate` type + `selfUpdateErrorMessage`). `CheckServicesFresh` returns `(bool, error)` consistently across scan.go (Task 3), the `Checker` interface (Task 3 Step 7), the caller (Tasks 3/4), and all doubles (Task 3 Step 9, Task 4 `rlChecker`). `Event.RateLimited` / JSON `rate_limited` match between Task 4 (Go) and Task 6 (TS parse). `selfUpdateErrorMessage` is defined once (Task 5 Step 2) and consumed by mutations (Step 5) and ApplicationSettings (Step 9).
