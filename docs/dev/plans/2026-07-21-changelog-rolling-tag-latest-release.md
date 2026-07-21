# Changelog Rolling-Tag Latest-Release Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a repo's latest stable GitHub release notes for images pinned to a non-semver rolling tag (`latest`, `master`, `master-omnibus`, ...) instead of returning only an OCI-label link.

**Architecture:** One contained change in the GitHub changelog source. A new `latestStableRelease` helper picks the highest-semver stable release from the releases page already fetched. `GitHubSource.Resolve`, on a `findRelease` miss, fires this fallback only when the target version is not parseable as semver, prepending an honesty note before the release body. Semver misses are untouched.

**Tech Stack:** Go 1.26, standard library only. Tests via `go test`.

## Global Constraints

- `CGO_ENABLED=0`, standard library only: no new dependencies (static binary invariant).
- No schema/migration change, no new Source, no change to resolver chain ordering.
- Reuse existing helpers: `normalizeTag`, `isPrerelease`, `detect.ParseCore`, `detect.CoreLess`.
- Semver images that fail to match a release MUST NOT fall back to the latest release (only non-semver rolling tags do).

---

### Task 1: `latestStableRelease` helper

**Files:**
- Modify: `internal/changelog/github.go` (add helper near `findRelease`, ~line 226)
- Test: `internal/changelog/github_test.go`

**Interfaces:**
- Consumes: `ghRelease` struct (`TagName`, `HTMLURL`, `Body`); `normalizeTag(string) string`; `isPrerelease(string) bool`; `detect.ParseCore(string) ([3]int, bool)`; `detect.CoreLess(a, b [3]int) bool` — all already in the package.
- Produces: `func latestStableRelease(rels []ghRelease) (ghRelease, bool)` — highest-semver stable release, `ok=false` when none.

- [ ] **Step 1: Write the failing test**

Add to `internal/changelog/github_test.go`:

```go
func TestLatestStableRelease(t *testing.T) {
	t.Run("picks highest stable, skips prerelease", func(t *testing.T) {
		rels := []ghRelease{
			{TagName: "v0.9.0-rc1", Body: "rc"},
			{TagName: "v0.9.2", HTMLURL: "https://github.com/o/r/releases/tag/v0.9.2", Body: "notes 0.9.2"},
			{TagName: "v0.9.1", Body: "notes 0.9.1"},
		}
		got, ok := latestStableRelease(rels)
		if !ok {
			t.Fatal("want ok=true, got false")
		}
		if got.TagName != "v0.9.2" {
			t.Fatalf("want v0.9.2, got %q", got.TagName)
		}
	})

	t.Run("highest by semver, not list order", func(t *testing.T) {
		// GitHub lists by publish date; a backport (0.8.6) can precede the newest.
		rels := []ghRelease{
			{TagName: "v0.8.6"},
			{TagName: "v0.9.2"},
			{TagName: "v0.9.0"},
		}
		got, ok := latestStableRelease(rels)
		if !ok || got.TagName != "v0.9.2" {
			t.Fatalf("want v0.9.2 ok, got %q ok=%v", got.TagName, ok)
		}
	})

	t.Run("empty when prerelease-only", func(t *testing.T) {
		rels := []ghRelease{{TagName: "v1.0.0-beta.1"}, {TagName: "v1.0.0-rc2"}}
		if _, ok := latestStableRelease(rels); ok {
			t.Fatal("want ok=false for prerelease-only list")
		}
	})

	t.Run("empty when no releases", func(t *testing.T) {
		if _, ok := latestStableRelease(nil); ok {
			t.Fatal("want ok=false for empty list")
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/changelog/ -run TestLatestStableRelease -v`
Expected: FAIL, `undefined: latestStableRelease`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/changelog/github.go` after `findRelease` (before `releasesInSpan`):

```go
// latestStableRelease returns the highest-semver stable release in rels
// (pre-releases skipped), for rolling-tag images whose version matches no release
// tag. Mirrors findRelease's prefix-scan / CoreLess ranking: highest version wins,
// not first-listed, since GitHub orders releases by publish date and a backport can
// precede the newest release.
func latestStableRelease(rels []ghRelease) (ghRelease, bool) {
	var best ghRelease
	var bestCore [3]int
	found := false
	for _, rel := range rels {
		norm := normalizeTag(rel.TagName)
		if isPrerelease(norm) {
			continue
		}
		c, ok := detect.ParseCore(norm)
		if !ok {
			continue
		}
		if !found || detect.CoreLess(bestCore, c) {
			best, bestCore, found = rel, c, true
		}
	}
	return best, found
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/changelog/ -run TestLatestStableRelease -v`
Expected: PASS (all four subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/changelog/github.go internal/changelog/github_test.go
git commit -m "feat(changelog): add latestStableRelease helper"
```

---

### Task 2: Wire rolling-tag fallback into `Resolve`

**Files:**
- Modify: `internal/changelog/github.go:153-164` (the `findRelease` miss branch in `Resolve`)
- Test: `internal/changelog/github_test.go`

**Interfaces:**
- Consumes: `latestStableRelease(rels []ghRelease) (ghRelease, bool)` from Task 1; `detect.ParseCore`; `in.Version` (target version, `firstNonEmpty(Update.ToVersion, Update.Tag)`).
- Produces: no new exported surface; changes `Resolve`'s behavior for non-semver `in.Version` on a release miss.

**Context for the test:** `GitHubSource` is constructed with `NewGitHubSource(client, apiBase, rawBase, tokenFn, cache, ttl)`. Existing tests in `github_test.go` stand up an `httptest.Server` and pass its URL as `apiBase`/`rawBase`. Copy that existing harness pattern (search the test file for `httptest.NewServer` and `NewGitHubSource`) rather than inventing a new one. The releases endpoint is `GET /repos/{owner}/{repo}/releases`. Build `Input` with `Image: registry.RemoteImage{Ref: "ghcr.io/analogj/scrutiny:master-omnibus", Labels: map[string]string{"org.opencontainers.image.source": "https://github.com/AnalogJ/scrutiny"}}` and `Version: "master-omnibus"` (and `FromVersion: ""`).

- [ ] **Step 1: Write the failing test**

Add to `internal/changelog/github_test.go`. Adapt the server/handler setup to match the existing harness in the file (same `httptest` + `NewGitHubSource` wiring the other `Resolve` tests use):

```go
func TestResolveRollingTagLatestRelease(t *testing.T) {
	releases := `[
		{"tag_name":"v0.9.2","html_url":"https://github.com/AnalogJ/scrutiny/releases/tag/v0.9.2","body":"scrutiny 0.9.2 notes"},
		{"tag_name":"v0.9.1","html_url":"https://github.com/AnalogJ/scrutiny/releases/tag/v0.9.1","body":"0.9.1 notes"},
		{"tag_name":"v0.9.0-rc1","html_url":"x","body":"rc"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases") {
			_, _ = w.Write([]byte(releases))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	src := NewGitHubSource(srv.Client(), srv.URL, srv.URL, nil, nil, 0)
	in := Input{
		Image: registry.RemoteImage{
			Ref:    "ghcr.io/analogj/scrutiny:master-omnibus",
			Labels: map[string]string{"org.opencontainers.image.source": "https://github.com/AnalogJ/scrutiny"},
		},
		Version: "master-omnibus",
	}

	res, err := src.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(res.Text, "Latest release for rolling tag `master-omnibus`") {
		t.Fatalf("missing rolling-tag note, got: %q", res.Text)
	}
	if !strings.Contains(res.Text, "scrutiny 0.9.2 notes") {
		t.Fatalf("want v0.9.2 body, got: %q", res.Text)
	}
	if res.URL != "https://github.com/AnalogJ/scrutiny/releases/tag/v0.9.2" {
		t.Fatalf("want v0.9.2 html_url, got: %q", res.URL)
	}
}

func TestResolveRollingTagPrereleaseOnlyFallsThrough(t *testing.T) {
	releases := `[{"tag_name":"v1.0.0-rc1","html_url":"x","body":"rc"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases") {
			_, _ = w.Write([]byte(releases))
			return
		}
		w.WriteHeader(http.StatusNotFound) // no CHANGELOG.md either
	}))
	defer srv.Close()

	src := NewGitHubSource(srv.Client(), srv.URL, srv.URL, nil, nil, 0)
	in := Input{
		Image:   registry.RemoteImage{Ref: "ghcr.io/o/r:latest"},
		Version: "latest",
	}
	res, err := src.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Text != "" {
		t.Fatalf("want no text (fall through), got: %q", res.Text)
	}
}

func TestResolveSemverMissNoLatestFallback(t *testing.T) {
	releases := `[{"tag_name":"v3.0.0","html_url":"x","body":"v3 notes"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases") {
			_, _ = w.Write([]byte(releases))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	src := NewGitHubSource(srv.Client(), srv.URL, srv.URL, nil, nil, 0)
	in := Input{
		Image:   registry.RemoteImage{Ref: "ghcr.io/o/r:1.2.3"},
		Version: "1.2.3", // semver, matches no release
	}
	res, err := src.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if strings.Contains(res.Text, "v3 notes") {
		t.Fatalf("semver miss must NOT fall back to latest release, got: %q", res.Text)
	}
}
```

Ensure the test file imports `context`, `net/http`, `net/http/httptest`, `strings`, and `dockbrr/internal/registry` (add any missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/changelog/ -run TestResolveRollingTag -v`
Expected: `TestResolveRollingTagLatestRelease` FAILs (no note / empty text — current code returns the OCI-less `Result{}` then chain gives only a link at resolver level, but this source returns empty text).

- [ ] **Step 3: Write minimal implementation**

In `internal/changelog/github.go`, edit the `findRelease` miss branch inside `Resolve` (currently lines 153-164). Replace:

```go
	want := tgt.tags(in.Version)
	target, ok := findRelease(rels, want, in.Version)
	if !ok {
		link, ok, err := s.changelogLink(ctx, owner, name, want)
		if err != nil {
			return Result{}, err
		}
		if ok {
			return Result{URL: link}, nil
		}
		return Result{}, nil
	}
```

with:

```go
	want := tgt.tags(in.Version)
	target, ok := findRelease(rels, want, in.Version)
	if !ok {
		// Rolling tag (non-semver version, e.g. "master-omnibus"): no release is
		// tagged with it, but the running digest is built from the repo tip, so the
		// latest stable release is the right proxy. Only for non-semver versions: a
		// semver miss must not resolve to an unrelated latest release.
		if _, parseable := detect.ParseCore(in.Version); !parseable {
			if latest, ok := latestStableRelease(rels); ok {
				note := fmt.Sprintf("_Latest release for rolling tag `%s`._\n\n", in.Version)
				return Result{Text: note + latest.Body, URL: latest.HTMLURL}, nil
			}
		}
		link, ok, err := s.changelogLink(ctx, owner, name, want)
		if err != nil {
			return Result{}, err
		}
		if ok {
			return Result{URL: link}, nil
		}
		return Result{}, nil
	}
```

(`fmt` and `detect` are already imported in `github.go`.)

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/changelog/ -run 'TestResolveRollingTag|TestResolveSemverMiss' -v`
Expected: PASS (all three).

- [ ] **Step 5: Run the full changelog package suite (no regressions)**

Run: `go test ./internal/changelog/`
Expected: `ok  dockbrr/internal/changelog`.

- [ ] **Step 6: Commit**

```bash
git add internal/changelog/github.go internal/changelog/github_test.go
git commit -m "feat(changelog): show latest release notes for rolling-tag images"
```

---

### Task 3: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Vet + full test suite**

Run: `go vet ./... && go test ./...`
Expected: no vet output; all packages `ok`.

- [ ] **Step 2: Static-binary invariant**

Run: `CGO_ENABLED=0 go build ./...`
Expected: builds clean, no errors.

---

## Self-Review

**Spec coverage:**
- Trigger (non-semver version, findRelease miss, repo exists) → Task 2, Step 3.
- `latestStableRelease` helper (highest stable, skip prereleases, CoreLess ranking) → Task 1.
- Note text `_Latest release for rolling tag `<tag>`._` → Task 2, Step 3 + assertion in Step 1.
- Semver miss unchanged → Task 2 `TestResolveSemverMissNoLatestFallback`.
- Prerelease-only / no stable falls through → Task 2 `TestResolveRollingTagPrereleaseOnlyFallsThrough`.
- Digest-only (`in.Version == ""`) unaffected → guaranteed by the existing early return in `Resolve` (`in.Version == ""`), not re-tested.
- No schema/dep change → confirmed by Task 3 build/vet.

**Placeholder scan:** none — every code and test block is complete.

**Type consistency:** `latestStableRelease(rels []ghRelease) (ghRelease, bool)` defined in Task 1, consumed identically in Task 2. `detect.ParseCore` / `detect.CoreLess` signatures match `internal/detect/semver.go`.
