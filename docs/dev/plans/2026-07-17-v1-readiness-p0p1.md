# v1 Readiness (P0+P1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the P0/P1 gaps from the 2026-07-17 v1 audit: login brute-force protection, HTTP security headers, server timeouts, session GC, CI linting (golangci-lint, govulncheck, eslint), Docker HEALTHCHECK, rolling image tags, and the operational docs (SECURITY.md, reverse proxy, backup).

**Architecture:** All hardening lands in `internal/httpapi` (middleware + a new rate-limit file) and `cmd/dockbrr/main.go` (server config + one new background loop), mirroring existing patterns (`requireAuth` middleware, `pruneLoop`). CI additions are new independent jobs in `.github/workflows/ci.yml`. Packaging changes touch only `Dockerfile` and `.goreleaser.yaml` and are validated by the existing `release-dryrun` snapshot job.

**Tech Stack:** Go 1.26 (chi, modernc sqlite), Vite/React 19/TS web app, GitHub Actions, GoReleaser v2, golangci-lint v2, eslint v9 flat config.

## Global Constraints

- CGO_ENABLED=0 static-binary invariant must hold (`CGO_ENABLED=0 go build ./...` must pass).
- UI/API never mutates Docker; nothing in this plan may touch the Job Engine's mutation path.
- No new runtime dependencies in `go.mod` (everything here uses stdlib); web gets dev-dependencies only.
- Frontend stays self-contained: no CDN, no external origins (the CSP in Task 2 encodes exactly this).
- TS verification: use `./node_modules/.bin/tsc -b --noEmit` or `npm run build`, never `npx tsc` (rtk hook masks errors).
- Commit messages: Conventional Commits, no AI attribution lines.
- All 7 safety invariants in CLAUDE.md apply to every task.

## Task summary (dispatch table)

| # | Task | Deps | Model | Reviewer | Plan section |
|---|------|------|-------|----------|--------------|
| 1 | Security response headers middleware | - | opus | opus | Task 1 |
| 2 | Login rate limiting | - | opus | opus | Task 2 |
| 3 | http.Server timeouts | - | haiku | sonnet | Task 3 |
| 4 | Session GC loop | - | haiku | sonnet | Task 4 |
| 5 | golangci-lint (config + CI + fix findings) | - | sonnet | sonnet | Task 5 |
| 6 | govulncheck CI job | - | haiku | sonnet | Task 6 |
| 7 | eslint (flat config + CI + fix findings) | - | sonnet | sonnet | Task 7 |
| 8 | Dockerfile HEALTHCHECK | - | haiku | sonnet | Task 8 |
| 9 | GoReleaser rolling image tags | - | haiku | sonnet | Task 9 |
| 10 | SECURITY.md | - | sonnet | sonnet | Task 10 |
| 11 | README ops docs + CLAUDE.md fix | 2 | sonnet | sonnet | Task 11 |
| 12 | Manual release checklist (human) | 1-11 | human | - | Task 12 |

Tasks 1-10 are independent and may run in any order. Task 11 documents behavior added in Task 2 (login lockout), so run it after. Task 12 is a human-in-the-loop checklist and closes the plan.

---

### Task 1: Security response headers middleware

Every response (API, SPA, assets) currently ships zero security headers. Add one middleware setting a strict CSP plus the standard hardening headers. The SPA is fully self-contained (system-fonts/bundled fonts, no CDN), so `'self'` covers everything except: React/Radix set inline `style` attributes (needs `'unsafe-inline'` in `style-src`) and Vite may inline small assets as `data:` URIs (img/font).

**Files:**
- Modify: `internal/httpapi/middleware.go` (append function)
- Modify: `internal/httpapi/server.go:129` (`routes()`: register middleware)
- Test: `internal/httpapi/middleware_test.go` (append)

**Interfaces:**
- Produces: `secureHeaders(next http.Handler) http.Handler` — package-private chi middleware.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/middleware_test.go`:

```go
// TestSecureHeaders asserts the hardening headers are present on an API route
// and on the SPA fallback (chi routes NotFound through the middleware stack too).
func TestSecureHeaders(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	for _, path := range []string{"/healthz", "/", "/api/setup/status"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		h := rec.Header()
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		if got := h.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "same-origin" {
			t.Errorf("%s: Referrer-Policy = %q, want same-origin", path, got)
		}
		csp := h.Get("Content-Security-Policy")
		for _, directive := range []string{
			"default-src 'self'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data:",
			"font-src 'self' data:",
			"object-src 'none'",
			"frame-ancestors 'none'",
			"base-uri 'self'",
		} {
			if !strings.Contains(csp, directive) {
				t.Errorf("%s: CSP missing %q (got %q)", path, directive, csp)
			}
		}
	}
}
```

Add `"strings"` and `"net/http/httptest"` to the test file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestSecureHeaders -v`
Expected: FAIL (`secureHeaders` undefined won't compile yet if wired in the same commit; write the test against the handler, so failure mode is empty-header assertions once Step 3's function exists but isn't wired, or a compile error — either is an acceptable red).

- [ ] **Step 3: Implement the middleware**

Append to `internal/httpapi/middleware.go`:

```go
// cspPolicy is the app's Content-Security-Policy. The SPA is fully
// self-contained (invariant #7: no CDN), so 'self' covers scripts, styles,
// fonts, XHR and SSE. The two relaxations are deliberate: React/Radix set
// inline style ATTRIBUTES ('unsafe-inline' in style-src governs those; inline
// <style> injection is still same-origin only), and Vite inlines small
// images/fonts as data: URIs.
const cspPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self' data:; connect-src 'self'; object-src 'none'; " +
	"base-uri 'self'; frame-ancestors 'none'; form-action 'self'"

// secureHeaders sets baseline hardening headers on every response, API and
// SPA alike. Registered first on the mux so chi applies it to NotFound (the
// SPA fallback) too.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", cspPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
```

Wire it in `internal/httpapi/server.go` — first line of `routes()` (chi requires `Use` before any route registration):

```go
func (s *Server) routes() {
	s.mux.Use(secureHeaders)

	s.mux.Get("/healthz", s.handleHealth)
	// ... rest unchanged
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/httpapi/ -v -run TestSecureHeaders` then the full package `go test ./internal/httpapi/`
Expected: PASS, no other test broken.

- [ ] **Step 5: Manual CSP smoke against the real SPA**

Run: `mise run build && ./dockbrr --data-dir /tmp/dockbrr-csp-check`
Open http://localhost:8080, complete/skip setup, open browser devtools Console. Toggle dark/light theme, open a dialog and a select (Radix), open a service's changelog.
Expected: zero CSP violation reports. **Known risk:** `next-themes` may inject an inline `<script>` for theme bootstrapping; if the console shows a blocked inline-script violation and the theme still works (client-side effect path), keep the policy. If theme switching actually breaks, add the reported script hash (`'sha256-...'` from the console message) to `script-src` in `cspPolicy` with a comment naming next-themes as the source. Do NOT add `'unsafe-inline'` to `script-src`.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/middleware.go internal/httpapi/middleware_test.go internal/httpapi/server.go
git commit -m "feat(httpapi): add CSP and security headers on all responses"
```

---

### Task 2: Login rate limiting

`POST /api/auth/login` is brute-forceable: no throttle, no lockout. Add an in-memory per-IP fixed-window failure counter: 5 failed logins within 15 minutes locks that IP out until the window expires (429 + `Retry-After`). Success clears the counter. Keyed on `r.RemoteAddr` host only — deliberately NOT `X-Forwarded-For`, which an attacker controls; behind a reverse proxy this degrades to a global limiter, acceptable for a single-user app (documented in Task 11).

**Files:**
- Create: `internal/httpapi/ratelimit.go`
- Create: `internal/httpapi/ratelimit_test.go`
- Modify: `internal/httpapi/auth.go:65-87` (`handleLogin`)
- Modify: `internal/httpapi/middleware.go:22-28` (add sentinel error)
- Modify: `internal/httpapi/server.go:98-111` (Server struct + `New`)
- Test: `internal/httpapi/auth_test.go` (append handler-level test)

**Interfaces:**
- Produces: `newLoginLimiter(now func() time.Time) *loginLimiter` with methods `blocked(ip string) (retryAfterSeconds int, blocked bool)`, `fail(ip string)`, `success(ip string)`; helper `clientIP(r *http.Request) string`. `Server` gains field `limiter *loginLimiter` (constructed in `New` with `time.Now`; tests may overwrite the field, same package).

- [ ] **Step 1: Write the failing limiter unit tests**

Create `internal/httpapi/ratelimit_test.go`:

```go
package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	for i := 0; i < failLimit; i++ {
		if _, blocked := l.blocked("10.0.0.1"); blocked {
			t.Fatalf("blocked after %d failures, want unblocked below limit", i)
		}
		l.fail("10.0.0.1")
	}
	retry, blocked := l.blocked("10.0.0.1")
	if !blocked {
		t.Fatal("not blocked after failLimit failures")
	}
	if retry <= 0 || retry > int(failWindow/time.Second) {
		t.Fatalf("retry-after = %d, want within (0, %d]", retry, int(failWindow/time.Second))
	}
}

func TestLoginLimiterWindowExpiryUnblocks(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < failLimit; i++ {
		l.fail("10.0.0.1")
	}
	now = now.Add(failWindow + time.Second)
	if _, blocked := l.blocked("10.0.0.1"); blocked {
		t.Fatal("still blocked after window expired")
	}
	// Expired entry must not linger: next failure starts a fresh count of 1.
	l.fail("10.0.0.1")
	if _, blocked := l.blocked("10.0.0.1"); blocked {
		t.Fatal("blocked after a single post-expiry failure")
	}
}

func TestLoginLimiterSuccessResets(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < failLimit-1; i++ {
		l.fail("10.0.0.1")
	}
	l.success("10.0.0.1")
	l.fail("10.0.0.1")
	if _, blocked := l.blocked("10.0.0.1"); blocked {
		t.Fatal("success did not reset the failure count")
	}
}

func TestLoginLimiterPerIPIsolation(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < failLimit; i++ {
		l.fail("10.0.0.1")
	}
	if _, blocked := l.blocked("10.0.0.2"); blocked {
		t.Fatal("unrelated IP blocked")
	}
}

func TestLoginLimiterEvictsAtCap(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < maxTrackedIPs+10; i++ {
		l.fail(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := len(l.m); n > maxTrackedIPs {
		t.Fatalf("map grew to %d entries, cap is %d", n, maxTrackedIPs)
	}
}

func TestClientIPStripsPort(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "192.0.2.7:51234"
	if got := clientIP(r); got != "192.0.2.7" {
		t.Fatalf("clientIP = %q, want 192.0.2.7", got)
	}
	// Malformed RemoteAddr (no port) degrades to the raw string, never "".
	r.RemoteAddr = "192.0.2.7"
	if got := clientIP(r); got != "192.0.2.7" {
		t.Fatalf("clientIP(no port) = %q, want 192.0.2.7", got)
	}
}
```

Add `"fmt"` to imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/ -run TestLoginLimiter -v`
Expected: compile error (`newLoginLimiter` undefined).

- [ ] **Step 3: Implement the limiter**

Create `internal/httpapi/ratelimit.go`:

```go
package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Login brute-force lockout: an IP accumulating failLimit failed logins inside
// failWindow is rejected with 429 until the window (measured from the FIRST
// failure) expires. A successful login clears the counter. In-memory only:
// a restart forgets history, which is fine — the goal is slowing online
// guessing, not durable audit.
const (
	failLimit  = 5
	failWindow = 15 * time.Minute
	// maxTrackedIPs bounds the map so a spray of spoofed source addresses
	// cannot grow memory without limit.
	maxTrackedIPs = 4096
)

type failEntry struct {
	count int
	first time.Time // start of this entry's window
}

// loginLimiter is a per-IP fixed-window failure counter. now is injected for
// tests. All methods are safe for concurrent use.
type loginLimiter struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]*failEntry
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, m: make(map[string]*failEntry)}
}

// blocked reports whether ip is locked out, and if so for how many more
// seconds (for Retry-After). Expired entries are pruned on sight.
func (l *loginLimiter) blocked(ip string) (retryAfterSeconds int, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip]
	if !ok {
		return 0, false
	}
	remaining := failWindow - l.now().Sub(e.first)
	if remaining <= 0 {
		delete(l.m, ip)
		return 0, false
	}
	if e.count < failLimit {
		return 0, false
	}
	secs := int(remaining / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs, true
}

// fail records a failed login for ip.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if e, ok := l.m[ip]; ok && now.Sub(e.first) < failWindow {
		e.count++
		return
	}
	if len(l.m) >= maxTrackedIPs {
		l.evictLocked(now)
	}
	l.m[ip] = &failEntry{count: 1, first: now}
}

// success clears ip's failure history.
func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}

// evictLocked drops expired entries; if none were expired it drops the oldest
// entry so the map never exceeds maxTrackedIPs. Caller holds mu.
func (l *loginLimiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, e := range l.m {
		if now.Sub(e.first) >= failWindow {
			delete(l.m, k)
			continue
		}
		if oldestKey == "" || e.first.Before(oldest) {
			oldestKey, oldest = k, e.first
		}
	}
	if len(l.m) >= maxTrackedIPs && oldestKey != "" {
		delete(l.m, oldestKey)
	}
}

// clientIP extracts the peer address from RemoteAddr. Deliberately NOT
// X-Forwarded-For: that header is attacker-controlled and would let a client
// dodge the lockout by rotating it. Behind a reverse proxy every request
// shares the proxy's address, so the lockout degrades to a global one —
// acceptable for a single-user app, and documented in the README.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
```

- [ ] **Step 4: Run limiter tests to verify pass**

Run: `go test ./internal/httpapi/ -run 'TestLoginLimiter|TestClientIP' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing handler-level test**

Append to `internal/httpapi/auth_test.go`:

```go
// TestLoginRateLimited drives failLimit bad logins through the real handler
// and asserts the next attempt is rejected 429 with Retry-After — even with
// CORRECT credentials (lockout is unconditional once tripped).
func TestLoginRateLimited(t *testing.T) {
	srv, db, _, _ := authedServer(t, Deps{})
	_ = db

	doLogin := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.RemoteAddr = "192.0.2.9:40000"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < failLimit; i++ {
		if rec := doLogin(`{"username":"admin","password":"wrong"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: code = %d, want 401", i, rec.Code)
		}
	}
	rec := doLogin(`{"username":"admin","password":"wrong"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("post-limit code = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response missing Retry-After header")
	}

	// A different IP is unaffected.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.RemoteAddr = "192.0.2.10:40000"
	other := httptest.NewRecorder()
	srv.Handler().ServeHTTP(other, req)
	if other.Code != http.StatusUnauthorized {
		t.Fatalf("other-IP code = %d, want 401", other.Code)
	}
}
```

(`authedServer` seeds user "admin" with a junk hash, so every login fails verification — exactly what this test needs. Add `"strings"`/`"net/http/httptest"` imports if absent.)

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./internal/httpapi/ -run TestLoginRateLimited -v`
Expected: FAIL — 6th attempt returns 401, want 429.

- [ ] **Step 7: Wire the limiter into the server and login handler**

In `internal/httpapi/server.go`, extend the struct and constructor:

```go
type Server struct {
	cfg     config.Config
	db      *store.DB
	deps    Deps
	mux     *chi.Mux
	limiter *loginLimiter
}

func New(cfg config.Config, db *store.DB, deps Deps) *Server {
	s := &Server{cfg: cfg, db: db, deps: deps, mux: chi.NewRouter(), limiter: newLoginLimiter(time.Now)}
	s.routes()
	return s
}
```

In `internal/httpapi/middleware.go`, add to the `var (...)` error block:

```go
	errTooManyAttempts = errors.New("too many failed login attempts; try again later")
```

In `internal/httpapi/auth.go`, rework `handleLogin`:

```go
// handleLogin verifies credentials and issues a session. Failed attempts feed
// the per-IP lockout; a locked-out IP gets 429 before any credential work.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry, blocked := s.limiter.blocked(ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeJSONError(w, http.StatusTooManyRequests, errTooManyAttempts)
		return
	}
	var body credsBody
	if err := decodeJSON(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, errBadCredentials)
		return
	}
	u, err := s.deps.Users.GetByUsername(body.Username)
	if err != nil {
		_, _ = auth.VerifyPassword(dummyHash, body.Password) // constant-ish time
		s.limiter.fail(ip)
		writeJSONError(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, body.Password)
	if err != nil || !ok {
		s.limiter.fail(ip)
		writeJSONError(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	s.limiter.success(ip)
	if err := s.issueSession(w, r, u.ID); err != nil {
		writeInternalError(w, "login: issue session", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username})
}
```

Add `"strconv"` to `auth.go` imports.

- [ ] **Step 8: Run the full httpapi suite**

Run: `go test ./internal/httpapi/`
Expected: PASS, including `TestLoginRateLimited` and all pre-existing auth tests.

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/ratelimit.go internal/httpapi/ratelimit_test.go internal/httpapi/auth.go internal/httpapi/auth_test.go internal/httpapi/middleware.go internal/httpapi/server.go
git commit -m "feat(httpapi): lock out repeated failed logins per source IP"
```

---

### Task 3: http.Server timeouts

The listener has no `ReadHeaderTimeout` (slowloris: a client trickling header bytes holds a connection open forever) and no `IdleTimeout`. Set both. Do NOT set `ReadTimeout`/`WriteTimeout`: `/api/events/stream` and job-log SSE are long-lived responses and a blanket write deadline would kill them.

**Files:**
- Modify: `cmd/dockbrr/main.go:358-362` (http.Server literal)

**Interfaces:** none (config-only change).

- [ ] **Step 1: Apply the change**

Replace the `httpServer` literal:

```go
	httpServer := &http.Server{
		Addr:        cfg.BindAddr,
		Handler:     srv.Handler(),
		BaseContext: func(net.Listener) context.Context { return ctx },
		// Slowloris guard. No ReadTimeout/WriteTimeout: SSE streams
		// (/api/events/stream, job logs) are long-lived responses and a
		// blanket deadline would sever them; per-request cancellation comes
		// from BaseContext instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
```

- [ ] **Step 2: Verify build + package tests**

Run: `CGO_ENABLED=0 go build ./... && go test ./cmd/...`
Expected: build OK, `cmd/dockbrr` tests (boot/shutdown via `testCancel`) PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/dockbrr/main.go
git commit -m "fix(server): set ReadHeaderTimeout and IdleTimeout on the listener"
```

---

### Task 4: Session GC loop

`store.Sessions.DeleteExpired` exists and is store-tested (`internal/store/sessions_test.go:101`) but nothing calls it: expired rows accumulate forever. Not a security hole (`Get` enforces expiry) — pure growth. Add a small background loop mirroring `pruneLoop` (`cmd/dockbrr/main.go:580`).

**Files:**
- Modify: `cmd/dockbrr/main.go` (new func + one `go` block next to the pruner at `main.go:312-317`)

**Interfaces:**
- Consumes: `(*store.Sessions).DeleteExpired(now time.Time) (int64, error)` (exists).

- [ ] **Step 1: Implement the loop**

Add below `pruneLoop` in `cmd/dockbrr/main.go`:

```go
// sessionGCInterval: how often expired session rows are swept. Expiry is
// enforced authoritatively on read (Sessions.Get); this loop only stops dead
// rows accumulating in the DB, so precision doesn't matter.
const sessionGCInterval = time.Hour

// sessionGCLoop ages out expired sessions. Store-only, no Docker.
func sessionGCLoop(ctx context.Context, sessions *store.Sessions) {
	run := func() {
		n, err := sessions.DeleteExpired(time.Now().UTC())
		if err != nil {
			logger.Errorf("session gc: delete expired: %v", err)
			return
		}
		if n > 0 {
			logger.Infof("session gc: removed %d expired session(s)", n)
		}
	}
	run()
	ticker := time.NewTicker(sessionGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
```

Wire it next to the pruner block (`// Pruner: ages out finished job history.`):

```go
	// Session GC: ages out expired session rows. Store-only, no Docker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		sessionGCLoop(ctx, sessions)
	}()
```

(`sessions` is the `*store.Sessions` already constructed earlier in `run()` and passed to `Deps`.)

- [ ] **Step 2: Verify build + tests**

Run: `CGO_ENABLED=0 go build ./... && go test ./cmd/... ./internal/store/`
Expected: PASS. The boot/shutdown test in `cmd/dockbrr` exercises that the new goroutine exits on ctx cancel (wg.Wait would hang otherwise).

- [ ] **Step 3: Commit**

```bash
git add cmd/dockbrr/main.go
git commit -m "feat(server): garbage-collect expired sessions hourly"
```

---

### Task 5: golangci-lint (config + CI job + fix findings)

Only `go vet` runs today. Add golangci-lint v2 with the `standard` preset (govet, errcheck, staticcheck, ineffassign, unused), a mise task for local runs, and a CI job.

**Files:**
- Create: `.golangci.yml`
- Modify: `.github/workflows/ci.yml` (new job)
- Modify: `mise.toml` (new task)
- Modify: whatever Go files the first lint run flags (fix findings)

**Interfaces:** none.

- [ ] **Step 1: Add the config**

Create `.golangci.yml`:

```yaml
# golangci-lint v2. Standard preset = govet, errcheck, staticcheck,
# ineffassign, unused. Add linters deliberately, one at a time, when they
# prove signal — not wholesale.
version: "2"

linters:
  default: standard
```

- [ ] **Step 2: Run locally and fix findings**

Run: `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...`

Fix every finding. Rules for fixes:
- `errcheck` on genuinely fire-and-forget calls: keep the existing codebase idiom (`_ = f()`), don't add noise comments.
- `staticcheck` simplifications: apply as suggested.
- If a finding is a false positive or intentionally accepted, suppress with an inline `//nolint:<linter> // <reason>` (reason mandatory), NOT a config-level exclusion.
Re-run until clean. Then run `go test ./...` to prove fixes broke nothing.
Expected final state: lint exits 0, all tests PASS.

- [ ] **Step 3: Add the mise task**

Append to `mise.toml`:

```toml
[tasks.lint-go]
description = "golangci-lint over the Go tree (same config as CI)"
run = "go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./..."
```

Run: `mise run lint-go` — expected: exits 0.

- [ ] **Step 4: Add the CI job**

Append to `.github/workflows/ci.yml` `jobs:`:

```yaml
  lint-go:
    name: Go lint (golangci-lint)
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: true
      - uses: golangci/golangci-lint-action@v9
        with:
          version: latest
```

(If `golangci/golangci-lint-action@v9` does not exist yet, use the newest major listed on github.com/golangci/golangci-lint-action — check with `gh api repos/golangci/golangci-lint-action/releases/latest --jq .tag_name`.)

- [ ] **Step 5: Commit**

```bash
git add .golangci.yml .github/workflows/ci.yml mise.toml <fixed-files>
git commit -m "ci: add golangci-lint and fix findings"
```

---

### Task 6: govulncheck CI job

No vulnerability scanning for Go deps today (CodeQL analyzes our code, not the dependency CVE database; dependabot bumps versions but doesn't alert on the current build). Add a govulncheck job.

**Files:**
- Modify: `.github/workflows/ci.yml` (new job)

**Interfaces:** none.

- [ ] **Step 1: Run locally first**

Run: `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`
Expected: "No vulnerabilities found" (exit 0). If it reports a real reachable vulnerability, STOP and surface it to the user before proceeding — that's a finding to fix, not to bury in a CI commit.

- [ ] **Step 2: Add the CI job**

Append to `.github/workflows/ci.yml` `jobs:`:

```yaml
  govulncheck:
    name: Go vulnerabilities (govulncheck)
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: true
      # Scans the dependency graph against the Go vulnerability DB, call-graph
      # aware (only REACHABLE vulns fail). May go red on a PR that didn't
      # cause it when a new CVE lands; that's the point — fix or bump then.
      - run: go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: scan Go dependencies with govulncheck"
```

---

### Task 7: eslint (flat config + CI + fix findings)

The web `lint` script is just `tsc` — types only, no lint rules (hooks correctness, dead code, etc.). Add eslint v9 flat config with typescript-eslint + react-hooks, wire into CI.

**Files:**
- Create: `web/eslint.config.js`
- Modify: `web/package.json` (devDeps + scripts)
- Modify: `.github/workflows/ci.yml` (step in the `web` job)
- Modify: whatever `web/src` files the first run flags

**Interfaces:** none.

- [ ] **Step 1: Install dev dependencies**

Run (in `web/`):

```bash
npm install -D eslint @eslint/js typescript-eslint eslint-plugin-react-hooks globals
```

Note: `package.json` pins `typescript@^7`; if typescript-eslint emits a peer-range WARNING for TS 7, that's acceptable (warning, not error). If it hard-fails, install the latest typescript-eslint prerelease that supports TS 7 and record that in the commit body.

- [ ] **Step 2: Add the flat config**

Create `web/eslint.config.js`:

```js
import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";

export default tseslint.config(
  { ignores: ["dist", "coverage", "node_modules"] },
  {
    files: ["src/**/*.{ts,tsx}"],
    extends: [
      js.configs.recommended,
      ...tseslint.configs.recommended,
      reactHooks.configs["recommended-latest"],
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // Vitest/test files legitimately use empty mocks and any-casts; keep
      // signal high in src, don't fight the test idiom.
      "@typescript-eslint/no-explicit-any": "warn",
    },
  },
);
```

(If `reactHooks.configs["recommended-latest"]` is not present in the installed plugin version, use the plugin's documented flat-config export — check `node_modules/eslint-plugin-react-hooks/README.md` — e.g. `reactHooks.configs.flat.recommended`.)

- [ ] **Step 3: Wire the script**

In `web/package.json` `scripts`, replace the current `lint` entry:

```json
    "lint": "eslint src",
    "typecheck": "tsc -b --noEmit"
```

- [ ] **Step 4: Run and fix findings**

Run (in `web/`): `npm run lint`
Fix every error. Downgrading a rule in the config is allowed only with a comment saying why. After fixes: `npm test` and `./node_modules/.bin/tsc -b --noEmit` must both pass (remember: never `npx tsc`, the rtk hook masks its errors).
Expected final state: eslint exits 0 (warnings allowed, errors not), tests PASS, typecheck clean.

- [ ] **Step 5: Add the CI step**

In `.github/workflows/ci.yml`, `web` job, after the `Typecheck` step:

```yaml
      - name: Lint
        run: npm run lint
```

- [ ] **Step 6: Commit**

```bash
git add web/eslint.config.js web/package.json web/package-lock.json .github/workflows/ci.yml <fixed-files>
git commit -m "ci(web): add eslint flat config and fix findings"
```

---

### Task 8: Dockerfile HEALTHCHECK

The image has `/healthz` (returns 503 when the DB is unreachable) but no `HEALTHCHECK`, so orchestrators see the container as merely "running". `docker:29-cli` is Alpine-based — busybox `wget` is available.

**Files:**
- Modify: `Dockerfile:18` (before `ENTRYPOINT`)

**Interfaces:** none.

- [ ] **Step 1: Add the healthcheck**

Insert between `EXPOSE 8080` and `ENTRYPOINT`:

```dockerfile
# busybox wget (Alpine base). Probes the in-process health endpoint; /healthz
# returns 503 when the DB is unreachable, flipping the container to unhealthy.
# Assumes the default in-image bind (:8080) — if you override DOCKBRR_BIND,
# override the healthcheck to match.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1
```

- [ ] **Step 2: Verify the image still builds and reports healthy**

Run:

```bash
mise run build
mkdir -p /tmp/dockbrr-hc/linux/amd64 && cp dockbrr /tmp/dockbrr-hc/linux/amd64/
docker build --build-arg TARGETPLATFORM=linux/amd64 -t dockbrr-hc-test /tmp/dockbrr-hc -f Dockerfile
docker run -d --name dockbrr-hc-test -v dockbrr-hc-data:/data -e DOCKBRR_DATA_DIR=/data dockbrr-hc-test
sleep 40 && docker inspect --format '{{.State.Health.Status}}' dockbrr-hc-test
docker rm -f dockbrr-hc-test && docker volume rm dockbrr-hc-data
```

Expected: `healthy`. (No socket mount needed: dockbrr boots fine with Docker unreachable, and `/healthz` only checks the DB.)

- [ ] **Step 3: Commit**

```bash
git add Dockerfile
git commit -m "feat(docker): add HEALTHCHECK probing /healthz"
```

---

### Task 9: GoReleaser rolling image tags

ghcr images are tagged only `{version}` + `latest`. Users can't pin a major line (`ghcr.io/yorah/dockbrr:1`) — ironic for an update manager. Add rolling major and major.minor tags.

**Files:**
- Modify: `.goreleaser.yaml:62-64` (`dockers_v2` `tags`)

**Interfaces:** none.

- [ ] **Step 1: Extend the tag list**

```yaml
    tags:
      - "{{ .Version }}"
      # Rolling lines so users can pin a major (dockbrr:1) or minor (dockbrr:1.2)
      # and still receive patch updates. Pre-1.0 these render as 0 / 0.x.
      - "{{ .Major }}"
      - "{{ .Major }}.{{ .Minor }}"
      - latest
```

- [ ] **Step 2: Verify via snapshot**

Run: `goreleaser release --snapshot --clean` (or rely on the `release-dryrun` CI job if goreleaser isn't installed locally; it runs on every PR).
Expected: build succeeds; `docker images ghcr.io/yorah/dockbrr` (local snapshot) shows the four tags.

- [ ] **Step 3: Commit**

```bash
git add .goreleaser.yaml
git commit -m "feat(release): publish rolling major and major.minor image tags"
```

---

### Task 10: SECURITY.md

Socket-privileged app, no disclosure policy. Add one.

**Files:**
- Create: `SECURITY.md`

**Interfaces:** none.

- [ ] **Step 1: Enable GitHub private vulnerability reporting**

Run: `gh api -X PUT repos/yorah/dockbrr/private-vulnerability-reporting`
Expected: HTTP 204. (If this fails for permission reasons, note it in the task report — the user flips it in repo Settings → Security → "Private vulnerability reporting".)

- [ ] **Step 2: Write the policy**

Create `SECURITY.md`:

```markdown
# Security Policy

## Supported versions

Only the latest release receives security fixes. dockbrr versions below 1.0
have no LTS branches.

## Reporting a vulnerability

Please report vulnerabilities privately via GitHub's private vulnerability
reporting: go to the [Security tab](https://github.com/yorah/dockbrr/security)
and click "Report a vulnerability". Do not open a public issue for anything
you believe is exploitable.

You can expect an acknowledgement within a week. Please include reproduction
steps and the dockbrr version.

## Scope notes

- dockbrr holds the Docker socket, which is root-equivalent on the host.
  Anything that lets an unauthenticated or lower-privileged party reach
  dockbrr's mutating API is in scope and treated as critical.
- dockbrr is designed to run on a trusted network or behind an
  authenticating reverse proxy. Reports assuming the attacker is already an
  authenticated dockbrr admin are generally out of scope (single-user model).
- The registry credentials and GitHub token stored by dockbrr are encrypted
  at rest (AES-256-GCM, key in `secret.key`). Reports involving an attacker
  with full read access to the data directory are out of scope: protecting
  the data directory is the deployment's job.
```

- [ ] **Step 3: Commit**

```bash
git add SECURITY.md
git commit -m "docs: add security policy"
```

---

### Task 11: README ops docs + CLAUDE.md fix

Three operational gaps in the README (reverse proxy/HTTPS, backup/restore, the new login lockout), plus a stale CLAUDE.md line ("respects air-gap" — no air-gap mode exists).

**Files:**
- Modify: `README.md` (two new sections after "Configuration", i.e. after line 147)
- Modify: `CLAUDE.md:31` (the `changelog` bullet)

**Interfaces:**
- Consumes: lockout behavior from Task 2 (5 failures / 15 min / per source IP).

- [ ] **Step 1: Add the README sections**

Insert after the Configuration table (before "## GitHub token and changelogs"):

```markdown
## Running behind a reverse proxy

dockbrr speaks plain HTTP and expects a reverse proxy to terminate TLS. Two
things matter:

- **Forward `X-Forwarded-Proto`.** dockbrr marks its cookies `Secure` only
  when the request arrived over HTTPS, which it detects from direct TLS or
  the `X-Forwarded-Proto: https` header. Every mainstream proxy sets this by
  default or with one line.
- **Login lockout is per source IP.** After 5 failed logins from one address,
  dockbrr rejects further attempts from it for 15 minutes. dockbrr reads the
  TCP peer address, not `X-Forwarded-For` (that header is spoofable), so
  behind a proxy all clients share the proxy's address and the lockout is
  effectively global. For a single-user tool that's the safe trade.

Caddy example:

    dockbrr.example.com {
        reverse_proxy 127.0.0.1:8080
    }

nginx example:

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header X-Forwarded-Proto $scheme;
        # SSE (live updates): disable buffering so events stream through.
        proxy_buffering off;
        proxy_read_timeout 1h;
    }

## Backup and restore

Everything dockbrr owns lives in the data directory (`--data-dir`, default
`./data`):

- `dockbrr.db` (+ `-wal`/`-shm` sidecars): SQLite database — settings,
  projects, update history, jobs.
- `secret.key`: the encryption key for registry credentials and the GitHub
  token. **Without this file those secrets are unrecoverable**; the rest of
  the database remains readable.
- `logs/`: rotated log files (safe to skip).

To back up: stop dockbrr (or accept a crash-consistent copy — SQLite in WAL
mode tolerates it) and copy the directory. To restore: place the directory
back and start dockbrr; there is no import step. Snapshots taken before
updates live in the database, so restoring the directory restores rollback
history too.
```

- [ ] **Step 2: Fix the stale CLAUDE.md line**

In `CLAUDE.md`, change:

```
- `changelog`: enriches updates with changelog text/URL (respects air-gap).
```

to:

```
- `changelog`: enriches updates with changelog text/URL (GitHub releases, registry description, OCI labels).
```

- [ ] **Step 3: Verify claims against code**

Check each factual claim written above against source before committing:
- Cookie `Secure` logic: `internal/httpapi/middleware.go` `isSecure` (TLS or `X-Forwarded-Proto: https`).
- Lockout numbers: `internal/httpapi/ratelimit.go` (`failLimit = 5`, `failWindow = 15 * time.Minute`).
- Data-dir contents: `internal/config` + `internal/store` (db filename), `internal/secret` (key filename), log path default in README config table.
Fix any mismatch in the DOC (the code is the source of truth).

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: reverse proxy, backup/restore, login lockout; fix stale air-gap note"
```

---

### Task 12: Manual release checklist (human-in-the-loop)

These need a real display and a real Docker host; they cannot be done by a sandboxed agent. Present this checklist to the user as the final gate before tagging v1.

**Files:** possibly `README.md` (screenshot embeds), `docs/screenshots/` (new).

- [ ] **Live-Docker smoke** (closes backlog `[batch4 Task 10]` + `[batch4 T8-live]`): on a host with real Compose projects: semver delta + severity colors correct; stop a container → dashboard badge flips within seconds (docker events → SSE); apply an update end-to-end (pull → up → health-gate → history entry); rollback it; Jobs screen shows both; settings export → wipe data dir → import round-trip.
- [ ] **Settings visual pass** (closes backlog `[p8 VISUAL]`): open Settings at desktop and ~`md` widths; check the horizontal scroller and active-row highlight look right in light + dark.
- [ ] **CSP console check** on the same session: devtools console shows no CSP violations anywhere in the app (belt-and-braces repeat of Task 1 Step 5 on a real deployment).
- [ ] **Screenshots**: capture (1) dashboard with a pending update, light theme; (2) same in dark; (3) update detail with changelog; (4) apply panel mid-job. Save as `docs/screenshots/{dashboard-light,dashboard-dark,changelog,apply}.png`, then add to README under the intro:

```markdown
![dockbrr dashboard](docs/screenshots/dashboard-light.png)
```

- [ ] **Healthcheck on the real image**: `docker inspect --format '{{.State.Health.Status}}' dockbrr` → `healthy` on the deployed container.
- [ ] After everything above is green and merged: cut v1 with a `release-as` commit (release-please's `bump-minor-pre-major` never reaches 1.0 on its own):

```bash
git commit --allow-empty -m "chore: release 1.0.0" -m "Release-As: 1.0.0"
```

---

## Self-review notes

- Spec coverage: audit P0 items → Tasks 1, 2, 12 (smoke + screenshots) + Task 10 (SECURITY.md); P1 items → Tasks 3-9, 11. Deliberately excluded (P2 in audit): systemd unit in nfpm, CONTRIBUTING/templates, coverage/attestation, settings-import transactionality, lac-M4 payload trim.
- Type consistency: `loginLimiter` method set used in Task 2 Step 7 matches Step 3 definitions (`blocked` returns `(int, bool)` — retry seconds first — consistent in handler and tests).
- Known uncertainty flagged in-plan: next-themes inline script vs CSP (Task 1 Step 5 has the decision rule), golangci-lint-action major version (Task 5 Step 4 has the check command), react-hooks flat-config export name and typescript-eslint TS 7 peer range (Task 7 Steps 1-2).
