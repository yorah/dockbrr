# GitHub Changelog Repo Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve label-less official images (nginx, redis, …) to their real GitHub release notes via an ordered image→repo chain + tolerant tag templates + a resolution cache.

**Architecture:** A new pure `githubTarget(ref, labels)` maps an image to `{owner, name, tagFn}` through ordered tiers (github-source label → ghcr host → curated map → namespaced-Hub → `library/X→X/X`). `GitHubSource.Resolve` uses the target, fetches the repo's releases list once and matches the update version against tolerant tag candidates, and caches the resolution (including negatives) in a new `changelog_repo_cache` store to bound GitHub request fan-out.

**Tech Stack:** Go 1.26 (stdlib only: `encoding/json`, `net/http`, `net/url`, `strings`, `time`), SQLite via existing `store` package, embedded migrations.

## Global Constraints

- CGO-free (`CGO_ENABLED=0 go build ./...`); **no new Go dependency**, stdlib only.
- Changelog enrichment stays read-only: HTTP GETs + one store cache write. No Docker mutation, no image pull.
- Source chain order unchanged: GitHub → Docker Hub → OCI (`cmd/dockbrr/main.go:143-147`).
- `defaultTags(v)` = `[v, "v"+v, "release-"+v]` (v-prefix stripped first). `postgresTags(v)` = `["REL_"+dotsToUnderscores(v), v, "v"+v]`.
- Curated map entries: `library/node→nodejs/node`, `library/python→python/cpython`, `library/golang→golang/go` (all `defaultTags`), `library/postgres→postgres/postgres` (`postgresTags`). nginx and redis are NOT curated (heuristic tier 6 + `release-` template covers them).
- Migration file: `internal/store/migrations/0006_changelog_repo_cache.sql` (auto-applied in sorted order by `migrate.go`).
- Repo-resolution cache: fixed TTL constant `24 * time.Hour` wired in `main.go` (no new setting). Negative caching via `owner=""`. A nil cache disables caching (always live-resolve), mirroring the nil-`tokenFn` pattern.
- Releases list: ONE request `GET {apiBase}/repos/{o}/{n}/releases?per_page=100`, page 1 only. Older-than-100-releases targets are a documented miss.
- Air-gap: `GitHubSource.NeedsNetwork()` stays `true`; the whole source (and its cache) is skipped by the resolver in air-gap mode.
- Security (invariant 7): release bodies are markdown, cached through the existing `sanitizeText` path and rendered via `react-markdown` + `rehype-sanitize`. No new HTML sink.

---

### Task 1: `githubTarget` resolution chain (pure, no network)

**Files:**
- Create: `internal/changelog/ghrepo.go`
- Test: `internal/changelog/ghrepo_test.go`

**Interfaces:**
- Consumes: `parseSource(labels map[string]string) SourceInfo` from `internal/changelog/oci.go:38` (returns `SourceInfo{URL, Host, Owner, Name}`; already reads both `org.opencontainers.image.source` and legacy `org.label-schema.vcs-url` with OCI precedence).
- Produces:
  - `type target struct { Owner, Name string; tags func(version string) []string }`
  - `func githubTarget(ref string, labels map[string]string) (target, bool)`
  - `func defaultTags(v string) []string`
  - `func postgresTags(v string) []string`
  - `var curatedRepos map[string]target`

- [ ] **Step 1: Write the failing test**

Create `internal/changelog/ghrepo_test.go`. It is a white-box test (`package changelog`) so it can call the unexported `githubTarget`.

```go
package changelog

import (
	"reflect"
	"testing"
)

func gh(owner, name string) (string, string) { return owner, name }

func TestGithubTarget(t *testing.T) {
	cases := []struct {
		name       string
		ref        string
		labels     map[string]string
		wantOK     bool
		wantOwner  string
		wantName   string
	}{
		{
			name:      "oci source label wins over heuristic",
			ref:       "library/nginx",
			labels:    map[string]string{"org.opencontainers.image.source": "https://github.com/acme/custom"},
			wantOK:    true, wantOwner: "acme", wantName: "custom",
		},
		{
			name:      "legacy label-schema vcs-url",
			ref:       "someimage",
			labels:    map[string]string{"org.label-schema.vcs-url": "https://github.com/acme/legacy.git"},
			wantOK:    true, wantOwner: "acme", wantName: "legacy",
		},
		{
			name:      "ghcr host",
			ref:       "ghcr.io/immich-app/immich-server:v1.100.0",
			wantOK:    true, wantOwner: "immich-app", wantName: "immich-server",
		},
		{
			name:      "curated remap node",
			ref:       "node:22",
			wantOK:    true, wantOwner: "nodejs", wantName: "node",
		},
		{
			name:      "curated remap postgres",
			ref:       "library/postgres:16.1",
			wantOK:    true, wantOwner: "postgres", wantName: "postgres",
		},
		{
			name:      "namespaced vendor image",
			ref:       "grafana/grafana:11.0.0",
			wantOK:    true, wantOwner: "grafana", wantName: "grafana",
		},
		{
			name:      "official library nginx",
			ref:       "nginx:1.25.0",
			wantOK:    true, wantOwner: "nginx", wantName: "nginx",
		},
		{
			name:      "official library redis",
			ref:       "redis:7.2.0",
			wantOK:    true, wantOwner: "redis", wantName: "redis",
		},
		{
			name:      "non-hub non-ghcr registry defers",
			ref:       "quay.io/prometheus/prometheus:v2.50.0",
			wantOK:    false,
		},
		{
			name:      "non-github label defers to heuristic",
			ref:       "gitlab.com/x/y", // treated as a Hub ns "gitlab.com"? no: host has a dot
			labels:    map[string]string{"org.opencontainers.image.source": "https://gitlab.com/g/p"},
			wantOK:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := githubTarget(tc.ref, tc.labels)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Owner != tc.wantOwner || got.Name != tc.wantName {
				t.Fatalf("owner/name = %q/%q, want %q/%q", got.Owner, got.Name, tc.wantOwner, tc.wantName)
			}
			if got.tags == nil {
				t.Fatal("tags func is nil")
			}
		})
	}
}

func TestDefaultTags(t *testing.T) {
	if got := defaultTags("1.31.2"); !reflect.DeepEqual(got, []string{"1.31.2", "v1.31.2", "release-1.31.2"}) {
		t.Fatalf("defaultTags = %v", got)
	}
	if got := defaultTags("v2.0.0"); !reflect.DeepEqual(got, []string{"2.0.0", "v2.0.0", "release-2.0.0"}) {
		t.Fatalf("defaultTags(v-prefixed) = %v", got)
	}
}

func TestPostgresTags(t *testing.T) {
	got := postgresTags("16.1")
	if len(got) == 0 || got[0] != "REL_16_1" {
		t.Fatalf("postgresTags = %v, want first REL_16_1", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/changelog/ -run 'TestGithubTarget|TestDefaultTags|TestPostgresTags' -v`
Expected: FAIL: `undefined: githubTarget` / `undefined: defaultTags` / `undefined: postgresTags`.

- [ ] **Step 3: Write the implementation**

Create `internal/changelog/ghrepo.go`:

```go
package changelog

import "strings"

// target is a resolved GitHub repository plus the candidate release tags to try
// for a given image version.
type target struct {
	Owner string
	Name  string
	tags  func(version string) []string
}

// defaultTags is the broad, tolerant tag-candidate set for the authoritative and
// heuristic tiers: plain, v-prefixed, and release-prefixed (nginx-style). The
// leading "v" is stripped first so the three forms are canonical.
func defaultTags(v string) []string {
	v = strings.TrimPrefix(v, "v")
	return []string{v, "v" + v, "release-" + v}
}

// postgresTags matches PostgreSQL's REL_16_1 tag scheme (dots -> underscores),
// with plain / v-prefixed fallbacks. Beta/rc tags (REL_17_BETA1) are not
// covered: those fall through to the Docker Hub source.
func postgresTags(v string) []string {
	v = strings.TrimPrefix(v, "v")
	return []string{"REL_" + strings.ReplaceAll(v, ".", "_"), v, "v" + v}
}

// curatedRepos overrides the heuristics for official images whose GitHub repo
// name differs from the image name, or whose release tags use an exotic scheme.
// Keyed by normalized Hub repo (see normalizeHubRepo). nginx and redis are
// intentionally absent: the library/X->X/X heuristic plus defaultTags' release-
// form resolves them.
var curatedRepos = map[string]target{
	"library/node":     {Owner: "nodejs", Name: "node", tags: defaultTags},
	"library/python":   {Owner: "python", Name: "cpython", tags: defaultTags},
	"library/golang":   {Owner: "golang", Name: "go", tags: defaultTags},
	"library/postgres": {Owner: "postgres", Name: "postgres", tags: postgresTags},
}

// githubTarget resolves an image reference plus its OCI labels to a GitHub
// target. ok=false means no repo could be determined (the caller defers). It
// makes no network calls; repo existence is confirmed later by the releases
// fetch. Tiers, first match wins:
//   1-2 a self-declared github.com VCS source label (OCI or legacy)
//   3   ghcr.io host (the registry is GitHub)
//   4   curated override map (name remaps, odd tag schemes)
//   5   namespaced Hub vendor image (ns/name -> ns/name)
//   6   official library image (library/X -> X/X)
func githubTarget(ref string, labels map[string]string) (target, bool) {
	if si := parseSource(labels); si.Host == "github.com" && si.Owner != "" && si.Name != "" {
		return target{Owner: si.Owner, Name: si.Name, tags: defaultTags}, true
	}
	host, path := splitHostPath(ref)
	if host == "ghcr.io" {
		if o, n, ok := firstTwo(path); ok {
			return target{Owner: o, Name: n, tags: defaultTags}, true
		}
		return target{}, false
	}
	hubRepo, ok := normalizeHubRepo(host, path)
	if !ok {
		return target{}, false
	}
	if t, ok := curatedRepos[hubRepo]; ok {
		return t, true
	}
	ns, name, ok := firstTwo(hubRepo)
	if !ok {
		return target{}, false
	}
	if ns != "library" {
		return target{Owner: ns, Name: name, tags: defaultTags}, true
	}
	return target{Owner: name, Name: name, tags: defaultTags}, true
}

// splitHostPath strips any :tag and @digest from ref, then separates a registry
// host (a first path segment containing "." or ":", or "localhost") from the
// remaining repository path.
func splitHostPath(ref string) (host, path string) {
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		if colon := strings.LastIndex(ref[slash:], ":"); colon >= 0 {
			ref = ref[:slash+colon]
		}
	} else if colon := strings.LastIndex(ref, ":"); colon >= 0 {
		ref = ref[:colon]
	}
	if i := strings.Index(ref, "/"); i >= 0 {
		first := ref[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			return first, ref[i+1:]
		}
	}
	return "", ref
}

// normalizeHubRepo maps a (host, path) to a normalized Docker Hub repo key
// ("<ns>/<name>"). A non-Hub host yields ok=false (tiers 4-6 do not apply). A
// bare name normalizes to library/<name>.
func normalizeHubRepo(host, path string) (string, bool) {
	switch host {
	case "", "docker.io", "index.docker.io":
	default:
		return "", false
	}
	if path == "" {
		return "", false
	}
	if !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return path, true
}

// firstTwo returns the first two "/"-separated segments of path, trimming a
// trailing ".git" from the second. ok=false if either is missing/empty.
func firstTwo(path string) (a, b string, ok bool) {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/changelog/ -run 'TestGithubTarget|TestDefaultTags|TestPostgresTags' -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Vet + full package build**

Run: `CGO_ENABLED=0 go vet ./internal/changelog/ && CGO_ENABLED=0 go build ./...`
Expected: no output (success). (The existing `github.go` still references `parseSource` via `buildInput`; unchanged in this task.)

- [ ] **Step 6: Commit**

```bash
git add internal/changelog/ghrepo.go internal/changelog/ghrepo_test.go
git commit -m "feat(changelog): image->github repo resolution chain"
```

---

### Task 2: `changelog_repo_cache` store + migration

**Files:**
- Create: `internal/store/migrations/0006_changelog_repo_cache.sql`
- Create: `internal/store/changelog_repos.go`
- Test: `internal/store/changelog_repos_test.go`

**Interfaces:**
- Consumes: `store.DB` (opened via `store.Open`), `NewX(db *DB)` constructor pattern (see `internal/store/images.go:107` `NewRemoteStates`).
- Produces:
  - `type ChangelogRepos struct{ db *sql.DB }`
  - `func NewChangelogRepos(db *DB) *ChangelogRepos`
  - `func (r *ChangelogRepos) Get(repo string, ttl time.Duration) (owner, name string, positive, found bool, err error)`
  - `func (r *ChangelogRepos) Put(repo, owner, name string) error`

- [ ] **Step 1: Write the migration**

Create `internal/store/migrations/0006_changelog_repo_cache.sql`:

```sql
CREATE TABLE changelog_repo_cache (
  image_repo  TEXT PRIMARY KEY,
  owner       TEXT NOT NULL,
  name        TEXT NOT NULL,
  resolved_at INTEGER NOT NULL
);
```

- [ ] **Step 2: Write the failing test**

Create `internal/store/changelog_repos_test.go`. Use the existing test-DB helper. Check how other store tests open a DB:

Run first: `grep -rn "func newTestDB\|store.Open\|testDB(" internal/store/*_test.go | head -3`
Use the same helper the neighboring tests use (e.g. `newTestDB(t)`), matching its signature. The test body:

```go
package store_test

import (
	"testing"
	"time"

	"dockbrr/internal/store"
)

func TestChangelogReposPutGet(t *testing.T) {
	db := newTestDB(t) // use the same helper as the other store tests
	repos := store.NewChangelogRepos(db)

	// Unknown repo -> not found.
	if _, _, _, found, err := repos.Get("library/nginx", time.Hour); err != nil || found {
		t.Fatalf("unknown repo: found=%v err=%v, want found=false", found, err)
	}

	// Positive resolution.
	if err := repos.Put("library/nginx", "nginx", "nginx"); err != nil {
		t.Fatal(err)
	}
	owner, name, positive, found, err := repos.Get("library/nginx", time.Hour)
	if err != nil || !found || !positive || owner != "nginx" || name != "nginx" {
		t.Fatalf("positive get = %q/%q positive=%v found=%v err=%v", owner, name, positive, found, err)
	}

	// Negative resolution (owner="").
	if err := repos.Put("library/void", "", ""); err != nil {
		t.Fatal(err)
	}
	_, _, positive, found, err = repos.Get("library/void", time.Hour)
	if err != nil || !found || positive {
		t.Fatalf("negative get = positive=%v found=%v err=%v, want found=true positive=false", positive, found, err)
	}

	// Expired row -> not found (ttl of 0 makes any row stale).
	if _, _, _, found, err := repos.Get("library/nginx", 0); err != nil || found {
		t.Fatalf("expired get: found=%v err=%v, want found=false", found, err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestChangelogReposPutGet -v`
Expected: FAIL: `undefined: store.NewChangelogRepos`.

- [ ] **Step 4: Write the implementation**

Create `internal/store/changelog_repos.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"time"
)

// ChangelogRepos caches the image->GitHub-repo resolution (including negative
// results) so the changelog GitHubSource does not re-query GitHub on every new
// update detection. Keyed by the image repo string as written in the service's
// image ref.
type ChangelogRepos struct{ db *sql.DB }

func NewChangelogRepos(db *DB) *ChangelogRepos { return &ChangelogRepos{db: db.DB} }

// Get returns the cached resolution for an image repo. found reports whether a
// row exists AND is within ttl; positive reports owner != "" (a real repo, vs a
// cached negative). A stale row (older than ttl) reports found=false so the
// caller re-resolves.
func (r *ChangelogRepos) Get(repo string, ttl time.Duration) (owner, name string, positive, found bool, err error) {
	var resolvedAt int64
	err = r.db.QueryRow(
		`SELECT owner, name, resolved_at FROM changelog_repo_cache WHERE image_repo=?`,
		repo,
	).Scan(&owner, &name, &resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, false, nil
	}
	if err != nil {
		return "", "", false, false, err
	}
	if time.Since(time.Unix(resolvedAt, 0)) > ttl {
		return "", "", false, false, nil
	}
	return owner, name, owner != "", true, nil
}

// Put upserts a resolution. An empty owner records a negative result (no GitHub
// repo). resolved_at is stamped to now.
func (r *ChangelogRepos) Put(repo, owner, name string) error {
	_, err := r.db.Exec(
		`INSERT INTO changelog_repo_cache (image_repo, owner, name, resolved_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(image_repo) DO UPDATE SET
		   owner       = excluded.owner,
		   name        = excluded.name,
		   resolved_at = excluded.resolved_at`,
		repo, owner, name, time.Now().Unix(),
	)
	return err
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/ -run TestChangelogReposPutGet -v`
Expected: PASS. (The migration is picked up automatically by `runMigrations` when the test DB opens.)

- [ ] **Step 6: Full store suite + build**

Run: `go test ./internal/store/ && CGO_ENABLED=0 go build ./...`
Expected: PASS, no build errors.

- [ ] **Step 7: Commit**

```bash
git add internal/store/migrations/0006_changelog_repo_cache.sql internal/store/changelog_repos.go internal/store/changelog_repos_test.go
git commit -m "feat(store): changelog_repo_cache resolution cache"
```

---

### Task 3: `GitHubSource` rewrite: target-driven releases-list fetch

**Files:**
- Modify: `internal/changelog/github.go` (rewrite `Resolve`, add `fetchReleasesList`, change `changelogLink` signature, remove `fetchRelease` and `tagVariants`)
- Modify: `internal/changelog/source.go:29-35` (remove `Source SourceInfo` field from `Input`)
- Modify: `internal/changelog/resolver.go:72-80` (drop `Source:` from `buildInput`)
- Modify: `internal/changelog/github_test.go` (list endpoint mock + `Image`-based input)

**Interfaces:**
- Consumes: `githubTarget(ref, labels) (target, bool)`, `defaultTags`, `target.tags` (Task 1); `repoFromRef(ref) string` from `resolver.go:85`; `registry.RemoteImage{Ref, Labels}`.
- Produces: `GitHubSource.Resolve` now reads `in.Image.Ref` + `in.Image.Labels` (no longer `in.Source`). `NewGitHubSource` signature UNCHANGED in this task: `NewGitHubSource(client *http.Client, apiBase, rawBase string, tokenFn func() string) *GitHubSource`.

- [ ] **Step 1: Rewrite the tests first**

Replace `internal/changelog/github_test.go` entirely with the version below. Key changes: the fake server now serves the **releases list** endpoint (`/repos/<o>/<r>/releases`), and inputs carry an `Image` (ref+labels) instead of a `Source`.

```go
package changelog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dockbrr/internal/changelog"
	"dockbrr/internal/registry"
)

// failTransport fails the test if any HTTP request is attempted.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("unexpected network request (defer / air-gap path must make none)")
	return nil, nil
}

func failClient(t *testing.T) *http.Client { return &http.Client{Transport: failTransport{t}} }

type ghRel struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

// ghServer stands up a fake GitHub API + raw host. releases maps "<owner>/<repo>"
// to its release list; changelogTags is the set of "<owner>/<repo>/<tag>" for
// which a raw CHANGELOG.md exists. wantAuth, when non-empty, asserts the
// Authorization header on the releases request.
func ghServer(t *testing.T, releases map[string][]ghRel, changelogTags map[string]bool, wantAuth string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), wantAuth)
		}
		// /repos/<owner>/<repo>/releases
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
		if len(parts) != 3 || parts[2] != "releases" {
			http.NotFound(w, r)
			return
		}
		rels, ok := releases[parts[0]+"/"+parts[1]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rels)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 4 && parts[3] == "CHANGELOG.md" &&
			changelogTags[parts[0]+"/"+parts[1]+"/"+parts[2]] {
			_, _ = w.Write([]byte("# Changelog"))
			return
		}
		http.NotFound(w, r)
	})
	return srv
}

// ghInput builds an Input for an image ref + version (no labels).
func ghInput(ref, version string) changelog.Input {
	return changelog.Input{Image: registry.RemoteImage{Ref: ref}, Version: version}
}

func TestGitHubNginxStyleReleaseHit(t *testing.T) {
	// nginx tags releases "release-1.31.2"; the image ref is label-less.
	srv := ghServer(t, map[string][]ghRel{
		"nginx/nginx": {{TagName: "release-1.31.2", HTMLURL: "https://github.com/nginx/nginx/releases/tag/release-1.31.2", Body: "## 1.31.2\n- fixed a bug"}},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" })
	res, err := s.Resolve(context.Background(), ghInput("nginx:1.31.2", "1.31.2"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/nginx/nginx/releases/tag/release-1.31.2" {
		t.Fatalf("URL = %q", res.URL)
	}
	if !strings.Contains(res.Text, "fixed a bug") {
		t.Fatalf("Text = %q", res.Text)
	}
}

func TestGitHubPlainTagReleaseHit(t *testing.T) {
	// redis uses plain "7.4.0".
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "7.4.0", HTMLURL: "https://github.com/redis/redis/releases/tag/7.4.0", Body: "redis 7.4.0"}},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" })
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "redis 7.4.0") {
		t.Fatalf("Text = %q", res.Text)
	}
}

func TestGitHubTokenSent(t *testing.T) {
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "7.4.0", HTMLURL: "u", Body: "notes"}},
	}, nil, "Bearer ghp_secret")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "ghp_secret" })
	if _, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0")); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubChangelogFallback(t *testing.T) {
	// Repo exists (empty release list) but a raw CHANGELOG.md exists at a tag.
	srv := ghServer(t, map[string][]ghRel{"redis/redis": {}}, map[string]bool{"redis/redis/7.4.0": true}, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" })
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/redis/redis/blob/7.4.0/CHANGELOG.md" {
		t.Fatalf("URL = %q, want blob CHANGELOG link", res.URL)
	}
	if res.Text != "" {
		t.Fatalf("CHANGELOG fallback must be link-only, got text %q", res.Text)
	}
}

func TestGitHubRepo404ReturnsEmpty(t *testing.T) {
	// Repo does not exist on the fake server -> releases 404 -> defer.
	srv := ghServer(t, nil, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" })
	res, err := s.Resolve(context.Background(), ghInput("redis:9.9.9", "9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

func TestGitHubUnresolvableDefersNoNetwork(t *testing.T) {
	// A non-Hub, non-ghcr registry cannot be resolved to a repo -> defer, no HTTP.
	s := changelog.NewGitHubSource(failClient(t), "http://127.0.0.1:0", "http://127.0.0.1:0", func() string { return "" })
	res, err := s.Resolve(context.Background(), ghInput("quay.io/prometheus/prometheus:v2.50.0", "2.50.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("unresolvable ref should defer, got %+v", res)
	}
}

func TestGitHubNoVersionDefersNoNetwork(t *testing.T) {
	s := changelog.NewGitHubSource(failClient(t), "http://127.0.0.1:0", "http://127.0.0.1:0", func() string { return "" })
	res, err := s.Resolve(context.Background(), ghInput("nginx:1.25.0", ""))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("no-version input should defer, got %+v", res)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/changelog/ -run TestGitHub -v`
Expected: FAIL / build error: `in.Image` compiles, but the server serves a list endpoint the current `Resolve` doesn't call (it calls `/releases/tags/<tag>`); several assertions fail. This confirms the rewrite is needed.

- [ ] **Step 3: Remove the `Source` field from `Input`**

In `internal/changelog/source.go`, delete the `Source  SourceInfo` line from the `Input` struct (currently line 33). The struct becomes:

```go
// Input is the resolved context handed to each Source. It is built once by the
// resolver from the update and its remote image.
type Input struct {
	Update  store.Update
	Image   registry.RemoteImage
	Repo    string // bare repository from Image.Ref (no tag, no @digest)
	Version string // target version: Update.ToVersion, else Update.Tag
}
```

Keep `SourceInfo` (still returned by `parseSource`, used by `githubTarget`).

- [ ] **Step 4: Drop `Source:` from `buildInput`**

In `internal/changelog/resolver.go`, `buildInput` (line 72) loses its `Source:` line:

```go
func buildInput(u store.Update, img registry.RemoteImage) Input {
	return Input{
		Update:  u,
		Image:   img,
		Repo:    repoFromRef(img.Ref),
		Version: firstNonEmpty(u.ToVersion, u.Tag),
	}
}
```

- [ ] **Step 5: Rewrite `GitHubSource.Resolve` + fetch helpers**

In `internal/changelog/github.go`:

(a) Replace the `ghRelease` struct (line 84) with a list-friendly shape carrying the tag:

```go
type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}
```

(b) Replace the entire `Resolve` method (lines 57-82) with:

```go
// Resolve resolves the image to a GitHub repo (via githubTarget), fetches that
// repo's releases once, and returns the release whose tag matches the update
// version under the target's tolerant tag templates. It falls back to a
// CHANGELOG.md blob link. It defers (empty Result) when no repo can be resolved
// or no version is known.
func (s *GitHubSource) Resolve(ctx context.Context, in Input) (Result, error) {
	tgt, ok := githubTarget(in.Image.Ref, in.Image.Labels)
	if !ok || in.Version == "" {
		return Result{}, nil
	}
	rels, exists, err := s.fetchReleasesList(ctx, tgt.Owner, tgt.Name)
	if err != nil {
		return Result{}, err
	}
	if !exists {
		return Result{}, nil // repo does not exist: defer
	}
	want := tgt.tags(in.Version)
	for _, rel := range rels {
		for _, w := range want {
			if rel.TagName == w {
				return Result{Text: rel.Body, URL: rel.HTMLURL}, nil
			}
		}
	}
	link, ok, err := s.changelogLink(ctx, tgt.Owner, tgt.Name, want)
	if err != nil {
		return Result{}, err
	}
	if ok {
		return Result{URL: link}, nil
	}
	return Result{}, nil
}

// fetchReleasesList GETs the first page (100) of a repo's releases. exists=false
// on 404 (no such repo). A non-2xx/404 status is an error.
func (s *GitHubSource) fetchReleasesList(ctx context.Context, owner, repo string) ([]ghRelease, bool, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100",
		s.apiBase, url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := s.tokenFn(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var rels []ghRelease
		if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
			return nil, false, err
		}
		return rels, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("github releases: status %d", resp.StatusCode)
	}
}
```

(c) Delete the old `fetchRelease` method entirely (old lines 89-123).

(d) Change `changelogLink` to accept the candidate tags instead of computing `tagVariants`. Its signature and loop head become:

```go
// changelogLink probes raw CHANGELOG.md at each candidate tag and returns the
// human blob link when the file exists. Best-effort, link-only.
func (s *GitHubSource) changelogLink(ctx context.Context, owner, repo string, tags []string) (string, bool, error) {
	for _, tag := range tags {
		// ... existing body unchanged ...
	}
	return "", false, nil
}
```

Keep the loop body (raw URL build, request, token header, status check) exactly as it is today. Only the function signature and the `for` range change (was `for _, tag := range tagVariants(version)`).

(e) Delete the `tagVariants` function entirely (old lines 156-163): it is superseded by `target.tags`.

- [ ] **Step 6: Run the changelog suite**

Run: `go test ./internal/changelog/ -v`
Expected: PASS: the new `TestGitHub*` tests and the existing `TestGithubTarget`/`resolver`/`oci`/`registry` tests all green.

- [ ] **Step 7: Vet + full build**

Run: `CGO_ENABLED=0 go vet ./internal/changelog/ && CGO_ENABLED=0 go build ./...`
Expected: success. `cmd/dockbrr/main.go` still compiles: `NewGitHubSource`'s signature is unchanged this task.

- [ ] **Step 8: Commit**

```bash
git add internal/changelog/github.go internal/changelog/github_test.go internal/changelog/source.go internal/changelog/resolver.go
git commit -m "feat(changelog): target-driven releases-list fetch, tag-tolerant"
```

---

### Task 4: Wire the resolution cache into `GitHubSource`

**Files:**
- Modify: `internal/changelog/github.go` (add `repoCache` interface, cache field + params, cache logic in `Resolve`)
- Modify: `cmd/dockbrr/main.go:135-144` (construct the store, pass it + TTL)
- Modify: `internal/changelog/github_test.go` (spy-cache tests; update existing `NewGitHubSource` calls to the new signature)

**Interfaces:**
- Consumes: `store.NewChangelogRepos(db)` returning `*store.ChangelogRepos` with `Get(repo, ttl) (owner,name string, positive,found bool, err error)` and `Put(repo,owner,name string) error` (Task 2); `repoFromRef` (`resolver.go:85`).
- Produces: `NewGitHubSource(client *http.Client, apiBase, rawBase string, tokenFn func() string, cache repoCache, ttl time.Duration) *GitHubSource`. `repoCache` interface (nil = disabled).

- [ ] **Step 1: Write the failing spy-cache test**

Append to `internal/changelog/github_test.go`:

```go
// spyCache records Get/Put calls and serves canned rows.
type spyCache struct {
	rows map[string][2]string // repo -> {owner,name}; present key = found
	gets int
	puts int
}

func (c *spyCache) Get(repo string, ttl time.Duration) (string, string, bool, bool, error) {
	c.gets++
	v, ok := c.rows[repo]
	if !ok {
		return "", "", false, false, nil
	}
	return v[0], v[1], v[0] != "", true, nil
}

func (c *spyCache) Put(repo, owner, name string) error {
	c.puts++
	if c.rows == nil {
		c.rows = map[string][2]string{}
	}
	c.rows[repo] = [2]string{owner, name}
	return nil
}

func TestGitHubCacheStoresResolution(t *testing.T) {
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "7.4.0", HTMLURL: "u", Body: "notes"}},
	}, nil, "")
	cache := &spyCache{}
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, cache, time.Hour)
	if _, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0")); err != nil {
		t.Fatal(err)
	}
	if cache.puts != 1 {
		t.Fatalf("puts = %d, want 1", cache.puts)
	}
}

func TestGitHubNegativeCacheSkipsNetwork(t *testing.T) {
	// Cache already holds a negative row for the image repo -> no HTTP at all.
	cache := &spyCache{rows: map[string][2]string{"redis": {"", ""}}}
	s := changelog.NewGitHubSource(failClient(t), "http://127.0.0.1:0", "http://127.0.0.1:0", func() string { return "" }, cache, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("negative cache should defer, got %+v", res)
	}
}
```

Also update the earlier `NewGitHubSource(...)` calls in this file (Tasks 3 tests) to pass `nil, time.Hour` for the two new params: e.g. `changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)`. Add `"time"` to the test imports.

Note the cache key: `repoFromRef("redis:7.4.0")` == `"redis"`, which is why the negative-cache test keys on `"redis"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/changelog/ -run TestGitHub -v`
Expected: FAIL / build error: `NewGitHubSource` takes 4 args, tests pass 6.

- [ ] **Step 3: Add the cache to `GitHubSource`**

In `internal/changelog/github.go`:

(a) Add the `time` import if not present (it is, line 11).

(b) Add the interface + fields, and extend the constructor:

```go
// repoCache is GitHubSource's optional image->repo resolution cache. nil
// disables caching (every Resolve re-resolves live).
type repoCache interface {
	Get(repo string, ttl time.Duration) (owner, name string, positive, found bool, err error)
	Put(repo, owner, name string) error
}

type GitHubSource struct {
	client  *http.Client
	apiBase string
	rawBase string
	tokenFn func() string
	cache   repoCache
	ttl     time.Duration
}
```

Extend `NewGitHubSource`:

```go
func NewGitHubSource(client *http.Client, apiBase, rawBase string, tokenFn func() string, cache repoCache, ttl time.Duration) *GitHubSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	if rawBase == "" {
		rawBase = defaultGitHubRawBase
	}
	if tokenFn == nil {
		tokenFn = func() string { return "" }
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &GitHubSource{client: client, apiBase: apiBase, rawBase: rawBase, tokenFn: tokenFn, cache: cache, ttl: ttl}
}
```

(c) Insert the cache lookup + write into `Resolve`. The method becomes:

```go
func (s *GitHubSource) Resolve(ctx context.Context, in Input) (Result, error) {
	tgt, ok := githubTarget(in.Image.Ref, in.Image.Labels)
	if !ok || in.Version == "" {
		return Result{}, nil
	}
	owner, name := tgt.Owner, tgt.Name
	repoKey := repoFromRef(in.Image.Ref)
	if s.cache != nil {
		if o, n, positive, found, err := s.cache.Get(repoKey, s.ttl); err == nil && found {
			if !positive {
				return Result{}, nil // cached negative: no repo
			}
			owner, name = o, n
		}
	}
	rels, exists, err := s.fetchReleasesList(ctx, owner, name)
	if err != nil {
		return Result{}, err
	}
	if s.cache != nil {
		if exists {
			if perr := s.cache.Put(repoKey, owner, name); perr != nil {
				log.Printf("changelog: cache put %s: %v", repoKey, perr)
			}
		} else if perr := s.cache.Put(repoKey, "", ""); perr != nil {
			log.Printf("changelog: cache put %s: %v", repoKey, perr)
		}
	}
	if !exists {
		return Result{}, nil
	}
	want := tgt.tags(in.Version)
	for _, rel := range rels {
		for _, w := range want {
			if rel.TagName == w {
				return Result{Text: rel.Body, URL: rel.HTMLURL}, nil
			}
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

(d) Add `"log"` to the imports in `github.go` (block at lines 3-12): insert `"log"` after `"io"`.

- [ ] **Step 4: Wire the store in `main.go`**

In `cmd/dockbrr/main.go`:

(a) After the other store constructors (near line 100, e.g. after `snapshots := store.NewSnapshots(db)`), add:

```go
	changelogRepos := store.NewChangelogRepos(db)
```

(b) Update the `NewGitHubSource` call (line 144) to pass the cache + TTL:

```go
		changelog.NewGitHubSource(httpClient, "https://api.github.com", "https://raw.githubusercontent.com", tokenFn, changelogRepos, 24*time.Hour),
```

(c) Ensure `time` is imported in `main.go` (it uses `httpClientTTL` already, so `time` is present, verify with the vet step).

- [ ] **Step 5: Run the changelog suite**

Run: `go test ./internal/changelog/ -v`
Expected: PASS: including `TestGitHubCacheStoresResolution` and `TestGitHubNegativeCacheSkipsNetwork`.

- [ ] **Step 6: Vet + full build + full test suite**

Run: `CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go build ./... && go test ./...`
Expected: all packages build and pass.

- [ ] **Step 7: Commit**

```bash
git add internal/changelog/github.go internal/changelog/github_test.go cmd/dockbrr/main.go
git commit -m "feat(changelog): bound github fan-out with resolution cache"
```

---

## Notes for the implementer

- **rtk / tsc caveat** does not apply. This is a Go-only change; there is no web build.
- **No frontend change**: the dashboard already renders `changelog_text`/`changelog_url` (`Changelog.tsx`, `ReviewDrawer.tsx`). Release-note markdown flows through the existing `sanitizeText` (cache-time) + `react-markdown`+`rehype-sanitize` (render-time) path unchanged.
- **Migration is auto-applied**: `internal/store/migrate.go` embeds `migrations/*.sql` and applies any file not yet in `schema_migrations`, in sorted order. Just adding `0006_*.sql` is enough, no registration code.
- **Cache key** is `repoFromRef(in.Image.Ref)` (the bare repo as written in the service's image ref): stable per service. Two differently-written refs for the same image (e.g. `nginx` vs `library/nginx`) would key separately; harmless (one extra resolve), not incorrect.
- **Manual smoke** (needs a live Docker host + the running binary, deferred to the usual smoke pass): with a real nginx/redis stack, a Check should now surface GitHub release notes (nginx `release-1.31.2` body, redis `7.4.0` body) in the update's changelog instead of the Docker Hub description; air-gap mode must still show nothing fetched.
```
