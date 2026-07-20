# Changelog raw-CHANGELOG fallback on releases-API error Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the GitHub releases API errors (commonly an unauthenticated rate-limit), still try the raw `CHANGELOG.md` probe before giving up, so a repo with a `CHANGELOG.md` but no GitHub Releases (e.g. `technitium/dns-server` → `TechnitiumSoftware/DnsServer`) links to the GitHub changelog instead of falling through to Docker Hub.

**Architecture:** One added branch in `GitHubSource.Resolve` (`internal/changelog/github.go`): on releases-fetch error, call the existing `changelogLink` raw probe; a hit returns the link and drops the error, a miss propagates the original error.

**Tech Stack:** Go 1.26 (CGO-free), `net/http/httptest` tests.

**Spec:** `docs/dev/specs/2026-07-20-changelog-raw-fallback-on-releases-error-design.md`

## Global Constraints

- Build stays CGO-free: `CGO_ENABLED=0 go build ./...` must pass.
- `internal/changelog` is read-only enrichment: HTTP GETs only, no Docker mutation, no pulls.
- Preserve the rate-limit signal: when the raw fallback also misses, `Resolve` must still return `ErrRateLimited` for a rate-limited releases fetch.
- Source ordering and the resolver chain are unchanged; no new setting, no API/schema/frontend change.
- Conventional Commits. Do NOT add any Claude / Co-Authored-By / Generated-with attribution.

---

### Task 1: Reach the raw CHANGELOG fallback on a releases-fetch error

**Files:**
- Modify: `internal/changelog/github.go` — `GitHubSource.Resolve`, the `if err != nil` branch right after the `s.fetchReleases(...)` call (currently `return Result{}, err`).
- Test: `internal/changelog/github_test.go` — add two tests.

**Interfaces:**
- Consumes (unchanged, already in scope at the edit point): `owner, name string`; `tgt target` with `tgt.tags(version string) []string`; `in.Version string` (guaranteed non-empty — `Resolve` returns early at the top when it is empty); `s.changelogLink(ctx, owner, name, tags) (string, bool, error)`.
- Produces: no signature change. Behavior: releases-error + raw hit → `Result{URL: link}, nil`; releases-error + raw miss → `Result{}, err` (original error).

- [ ] **Step 1: Write the failing tests**

Add to `internal/changelog/github_test.go`. These stand up their own mux because the shared `ghServer` helper cannot emit a 403. They rely on `ghInput` (no labels) with a `ghcr.io/...` ref so `githubTarget` resolves via the ghcr tier to `owner/name` with `defaultTags`.

```go
func TestGitHubReleasesErrorFallsBackToRawChangelog(t *testing.T) {
	// Releases API is rate-limited (403 + X-RateLimit-Remaining: 0), but a raw
	// CHANGELOG.md exists at the v-prefixed tag. The source must recover the
	// GitHub blob link and drop the rate-limit error.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// raw CHANGELOG.md only at foo/bar/v1.2.3
		if r.URL.Path == "/foo/bar/v1.2.3/CHANGELOG.md" {
			_, _ = w.Write([]byte("# Changelog"))
			return
		}
		http.NotFound(w, r)
	})

	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("ghcr.io/foo/bar:latest", "1.2.3"))
	if err != nil {
		t.Fatalf("err = %v, want nil (raw fallback should drop the rate-limit error)", err)
	}
	if res.URL != "https://github.com/foo/bar/blob/v1.2.3/CHANGELOG.md" {
		t.Fatalf("URL = %q, want the GitHub blob CHANGELOG link", res.URL)
	}
	if res.Text != "" {
		t.Fatalf("Text = %q, want empty (CHANGELOG fallback is link-only)", res.Text)
	}
}

func TestGitHubReleasesErrorNoRawChangelogPreservesError(t *testing.T) {
	// Releases API rate-limited AND no raw CHANGELOG.md anywhere: the original
	// ErrRateLimited must still surface (not be swallowed by the fallback).
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("ghcr.io/foo/bar:latest", "1.2.3"))
	if !errors.Is(err, changelog.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited preserved", err)
	}
	if res.URL != "" || res.Text != "" {
		t.Fatalf("res = %+v, want empty when nothing resolved", res)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/changelog/ -run 'TestGitHubReleasesError' -v`
Expected: `TestGitHubReleasesErrorFallsBackToRawChangelog` FAILS (current code returns the error, so `err != nil` and `res.URL` is empty). `TestGitHubReleasesErrorNoRawChangelogPreservesError` may already pass (that is the current behavior) — that is fine; it is a regression guard for Step 3.

- [ ] **Step 3: Implement the fallback branch**

In `internal/changelog/github.go`, in `GitHubSource.Resolve`, replace the releases-fetch error branch:

```go
	if err != nil {
		return Result{}, err
	}
```

with:

```go
	if err != nil {
		// Releases API failed (commonly an unauthenticated rate-limit). The raw
		// CHANGELOG.md probe hits raw.githubusercontent.com, which is not subject
		// to that limit and does not need the releases list, so try it before
		// giving up. A hit returns a link (dropping the error); a miss propagates
		// the original error so a genuine rate-limit still surfaces.
		if link, ok, lerr := s.changelogLink(ctx, owner, name, tgt.tags(in.Version)); lerr == nil && ok {
			return Result{URL: link}, nil
		}
		return Result{}, err
	}
```

Do not change anything else. The repo-resolution cache is intentionally left untouched on this path (the errored fetch is inconclusive about repo existence).

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/changelog/ -run 'TestGitHubReleasesError' -v`
Expected: both PASS.

- [ ] **Step 5: Run the full changelog package + build**

Run: `CGO_ENABLED=0 go build ./... && go test ./internal/changelog/`
Expected: build OK; all changelog tests PASS (existing `TestGitHubChangelogFallback`, `TestGitHubRateLimitedYieldsErrRateLimited`, success paths unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/changelog/github.go internal/changelog/github_test.go
git commit -m "fix(changelog): try raw CHANGELOG.md fallback when releases API errors"
```

---

## Self-Review

**Spec coverage:**
- Releases-error → raw fallback branch — Task 1 Step 3. ✔
- Raw hit returns link, drops error — Test 1. ✔
- Raw miss preserves `ErrRateLimited` — Test 2. ✔
- Cache untouched on error path — no cache call added in Step 3. ✔
- No ordering/setting/API/schema/frontend change — diff limited to one branch + tests. ✔

**Placeholder scan:** No TBD/TODO; full code and exact commands in every step.

**Type consistency:** `changelogLink(ctx, owner, name, tags) (string, bool, error)` used with the exact `(link, ok, lerr)` shape it returns; `tgt.tags(in.Version)` matches the `target.tags func(version string) []string` field; `Result{URL: ...}` matches the existing struct.
