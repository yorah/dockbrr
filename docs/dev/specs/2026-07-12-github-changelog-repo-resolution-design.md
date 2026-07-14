# GitHub Changelog Repo Resolution: Design Spec

**Date:** 2026-07-12
**Status:** Approved (brainstorm complete; ready for planning)

## Goal

Enrich changelogs with GitHub **release notes** for images that do not carry a
VCS source label: most official Docker Hub images (nginx, redis, …). Today the
`GitHubSource` only fires when `org.opencontainers.image.source` names a
`github.com` repo; label-less images (nginx, redis have no such label) never
reach GitHub and fall through to the Docker Hub `full_description`. Add an
ordered image→repo resolution chain (labels → ghcr host → curated map →
heuristics) plus tolerant tag-template matching, so popular images resolve to
their real GitHub releases, while bounding request fan-out against GitHub's rate
limits.

## Background: the two gaps

1. **No image→repo mapping without a label.** `internal/changelog/github.go`
   `GitHubSource.Resolve` defers unless `in.Source.Host == "github.com"`, and
   `in.Source` is parsed purely from OCI labels (`resolver.go buildInput` →
   `parseSource`). `library/nginx` carries only a `maintainer` label; `library/redis`
   carries none. So the resolver never learns `nginx/nginx` or `redis/redis` and
   makes zero GitHub calls.

2. **Tag-scheme mismatch.** `tagVariants(v)` yields only `{v, "v"+v}` (or the
   de-`v` form). nginx tags releases `release-1.31.2`; redis uses plain `7.4.9`;
   postgres uses `REL_16_1`. Even with a correct repo, the per-tag release fetch
   would 404 for nginx and postgres.

Empirically verified against the local images:
- `nginx:1.25.0` labels: `{"maintainer":"NGINX Docker Maintainers …"}`, no source label.
- `redis:7.2.0` labels: `null`.
- nginx GitHub release tags: `release-1.31.2`. redis GitHub release tags: `7.4.9` (plain).

## Core idea

A single ordered resolution function maps an image (ref + labels) to a GitHub
target `{owner, name, tag-template}`, trying authoritative sources first
(self-declared labels, ghcr host), then a curated override map for oddballs,
then broad heuristics. `GitHubSource` uses the target, matches the update's
version against tolerant tag templates via **one** releases-list request, and
caches the repo resolution (including negatives) to bound fan-out.

## A. Resolution chain: `internal/changelog/ghrepo.go` (new file)

Pure function, no network:

```go
// target is a resolved GitHub repository plus the candidate release tags to try
// for a given image version.
type target struct {
    Owner string
    Name  string
    tags  func(version string) []string
}

// githubTarget resolves an image reference + its OCI labels to a GitHub target.
// ok=false means "no GitHub repo could be determined" (caller defers). It never
// makes network calls; existence is confirmed later by the releases fetch.
func githubTarget(ref string, labels map[string]string) (target, bool)
```

**Ordered tiers, first match wins** (this ordering is the request short-circuit, only the winning tier's repo is ever queried):

| # | Tier | Rule | tag func |
|---|------|------|----------|
| 1 | OCI source label | `labels["org.opencontainers.image.source"]` parses to a `github.com` host → `{owner,name}` | `defaultTags` |
| 2 | Legacy VCS label | `labels["org.label-schema.vcs-url"]` parses to a `github.com` host → `{owner,name}` | `defaultTags` |
| 3 | ghcr.io host | `ref` host is `ghcr.io` → first two path segments = `{owner,name}` | `defaultTags` |
| 4 | Curated map | normalized Hub repo key present in `curatedRepos` | entry's explicit tag func |
| 5 | Namespaced Hub | Hub repo `ns/name` (ns has no `.`/`:`, ns != `library`) → `{ns,name}` | `defaultTags` |
| 6 | Official library | Hub repo `library/X` (or bare `X`) → `{X,X}` | `defaultTags` |

Tiers 1–3 are authoritative (image self-declares, or the registry *is* GitHub).
Tier 4 overrides the heuristics for names that differ or tag schemes that are
odd. Tiers 5–6 are guesses, made safe by exact-version tag matching (Section B)
plus caching (Section C).

**Repo normalization** for the Hub-repo key mirrors `registry.go hubPath`: strip
`docker.io/` / `index.docker.io/` prefixes; a first segment containing `.` or `:`
is a non-Hub registry → tiers 4–6 do not apply (only ghcr tier 3 handles a
known non-Hub host). A bare `X` normalizes to `library/X`.

**Label parsing** (`parseGitHubURL`): accept
`https://github.com/OWNER/REPO(.git)?`, `git+https://…`, `git@github.com:OWNER/REPO`,
and `github.com/OWNER/REPO`. Trailing `.git`, trailing slash, and extra path
segments are trimmed to the first two path components. A non-github host yields
no match (tier defers).

**Tag templates:**

```go
// defaultTags: the broad, tolerant set for authoritative + heuristic tiers.
func defaultTags(v string) []string {
    v = strings.TrimPrefix(v, "v")
    return []string{v, "v" + v, "release-" + v}
}
```

Curated tag funcs for the seeded oddballs:

```go
// postgresTags: 16.1 -> REL_16_1 ; also try plain as a fallback.
func postgresTags(v string) []string {
    return []string{"REL_" + strings.ReplaceAll(v, ".", "_"), v, "v" + v}
}
```

**Curated seed map** (`curatedRepos`, key = normalized Hub repo):

| key | owner/name | tag func | why |
|-----|-----------|----------|-----|
| `library/node` | `nodejs/node` | `defaultTags` | name remap |
| `library/python` | `python/cpython` | `defaultTags` | name remap |
| `library/golang` | `golang/go` | `defaultTags` | name remap |
| `library/postgres` | `postgres/postgres` | `postgresTags` | odd tag scheme |

nginx and redis are intentionally **not** curated: with `release-` in
`defaultTags`, tier 6 (`library/nginx → nginx/nginx`, `library/redis → redis/redis`)
resolves them correctly. The curated map is only for name remaps and exotic tag
schemes. Adding entries later is a one-line append.

## B. Fetch flow: `GitHubSource.Resolve` rewrite

```
1. target, ok := githubTarget(in.Image.Ref, in.Image.Labels)
   if !ok || in.Version == "" -> return empty (defer), no network
2. cache lookup by normalized image repo (Section C):
     negative & fresh -> return empty (defer), no network
     positive & fresh -> use cached {owner,name}, skip tier walk
     miss             -> proceed, persist result at end
3. GET {apiBase}/repos/{owner}/{name}/releases?per_page=100  (ONE request)
     for each release: if release.tag_name ∈ target.tags(version)
         -> Result{Text: release.body, URL: release.html_url}
4. list miss -> changelogLink fallback over target.tags(version) (bounded),
     returns a CHANGELOG.md blob link if present
5. persist cache: positive {owner,name} when the releases list returned 2xx
     (repo exists), negative (owner="") when the releases list 404'd (repo does
     not exist). The `!ok` path in step 1 returns before the cache is consulted,
     since it is deterministic and network-free, so it is neither read nor written.
```

- `apiBase`/`rawBase` stay injected (already are), so tests point at a fake server.
- The token (`tokenFn`) rides the releases-list and changelog-link requests
  exactly as today (`Authorization: Bearer` when non-empty).
- Replace the current per-tag `fetchRelease` loop with the single releases-list
  fetch + client-side tag match. `fetchRelease` may be removed if unused after
  the rewrite; `changelogLink` and `tagVariants` are superseded by
  `target.tags`: remove `tagVariants` and fold its logic into `defaultTags`.
- `in.Source` / `SourceInfo` / `parseSource` in `resolver.go`/`source.go` were
  consumed only by `GitHubSource`. Fold their label parsing into
  `githubTarget` (tiers 1–2) and remove `Input.Source` + `parseSource` +
  `SourceInfo`. `buildInput` keeps `Repo` and `Version`.

**Pagination note:** the releases list fetches the first 100 (one page). An
update targets a recent version, which is on page 1 for essentially all real
repos. Older-than-100-releases targets are a documented miss (fall through to
Docker Hub): no multi-page walk, to keep it one request.

## C. Repo-resolution cache: `internal/store/changelog_repos.go` (new) + migration `0006`

Bounds fan-out: resolve a repo (or confirm none) once per TTL window instead of
re-querying GitHub on every new-update detection.

```sql
-- 0006_changelog_repo_cache.sql
CREATE TABLE changelog_repo_cache (
  image_repo  TEXT PRIMARY KEY,   -- normalized Hub/ghcr repo, e.g. "library/nginx"
  owner       TEXT NOT NULL,      -- "" = negative (no GitHub repo resolved/exists)
  name        TEXT NOT NULL,
  resolved_at INTEGER NOT NULL    -- unix seconds
);
```

Store type:

```go
type ChangelogRepos struct { db *sql.DB }

// Get returns the cached resolution for a normalized image repo. found reports
// whether a row exists and is within ttl; positive reports owner!="".
func (r *ChangelogRepos) Get(repo string, ttl time.Duration) (owner, name string, positive, found bool, err error)

// Put upserts the resolution (owner="" for a negative result).
func (r *ChangelogRepos) Put(repo, owner, name string) error
```

- Injected into `GitHubSource` behind a small interface so the changelog package
  stays testable and decoupled; a **nil cache disables caching** (always live
  resolve), matching how a nil `tokenFn` disables the token.

```go
// repoCache is GitHubSource's optional resolution cache. nil = disabled.
type repoCache interface {
    Get(repo string, ttl time.Duration) (owner, name string, positive, found bool, err error)
    Put(repo, owner, name string) error
}
```

- TTL: reuse the existing registry `cache_ttl_seconds` setting is **not**
  appropriate (repo mappings change far less often than digests). Use a fixed
  default constant `changelogRepoTTL = 24 * time.Hour` in `main.go` wiring
  (no new setting in v1). Negatives expire on the same TTL, so a repo that gains
  releases later is eventually discovered.
- `Put` is best-effort in `Resolve`: a cache write error is logged, not fatal.
- Wiring: `cmd/dockbrr/main.go` constructs `store.NewChangelogRepos(db)` and
  passes it (with the TTL) into `changelog.NewGitHubSource`.

## D. Testing

**`internal/changelog/ghrepo_test.go`** (table-driven, no network):
- Each tier resolves the expected `{owner,name}`: OCI label, legacy label, ghcr
  host, curated (node→nodejs/node, postgres→postgres/postgres), namespaced
  (`grafana/grafana`), official (`library/nginx → nginx/nginx`,
  `library/redis → redis/redis`).
- Precedence: an image with BOTH a `github.com` label and a heuristic match
  resolves via the label; a curated key overrides the heuristic tier.
- Guards: `library/` first segment does not trigger the namespaced tier; a first
  segment with `.`/`:` (e.g. `quay.io/x/y`) yields `!ok` for tiers 4–6.
- Unmapped/undeterminable → `ok == false`.
- `defaultTags("1.31.2")` == `["1.31.2","v1.31.2","release-1.31.2"]`;
  `defaultTags("v2.0.0")` de-`v`s first; `postgresTags("16.1")` includes
  `REL_16_1`.

**`internal/changelog/github_test.go`** (fake GitHub via `apiBase`/`rawBase`):
- Releases-list contains `release-1.31.2` → nginx-style update resolves to that
  release's `body` + `html_url` in ONE list request.
- Plain-tag repo (`7.4.9`) resolves.
- List miss but `CHANGELOG.md` present at a candidate tag → link-only Result.
- List + link both miss → empty Result (defer to next source).
- Token present → `Authorization: Bearer` header sent on the list request.
- With a spy cache: a miss resolves and calls `Put`; a negative row makes a
  second Resolve skip the network entirely.

**`internal/store/changelog_repos_test.go`**:
- `Put` then `Get` returns the row (positive); `Get` of an unknown repo →
  `found=false`; a negative row (`owner=""`) → `found=true, positive=false`;
  a row older than TTL → `found=false`.

**Regression:** existing `resolver_test.go` precedence tests stay green, GitHub
source still ordered before Docker Hub, Docker Hub before OCI. Air-gap still
skips all network sources.

## Interplay / edge cases (must hold)

- **Digest-only update (no semver version):** `in.Version == ""` → defer,
  unchanged, no network.
- **ghcr deep path** `ghcr.io/o/r/sub`: take `o/r` (first two path segments);
  documented best-effort.
- **Wrong-repo coincidence:** a colliding `X/X` repo with a same-versioned
  release could produce a wrong changelog. Accepted risk, bounded by exact
  tag-name match against `target.tags(version)` and cached per repo; the result
  is user-visible and dismissible. No silent mutation.
- **postgres beta/rc tags** (`REL_17_BETA1`): not covered by `postgresTags`;
  documented miss → falls through to Docker Hub.
- **Air-gap mode:** `GitHubSource.NeedsNetwork()` stays `true`; the resolver
  skips the whole source, cache is never consulted.
- **Security (invariant 7):** release bodies are markdown, rendered through the
  existing `react-markdown` + `rehype-sanitize` path (`Changelog.tsx`). No new
  HTML sink, no `dangerouslySetInnerHTML`.

## Safety invariants (unchanged, must still hold)

- Single static binary, CGO-free; SPA via embed.FS. No new dependency required
  (`encoding/json`, `net/http`, `net/url`, `strings` only).
- Changelog enrichment stays read-only: HTTP GETs against GitHub + a store cache
  write (`changelog_repo_cache`). No Docker mutation, no image pull.
- Frontend unchanged; no CSRF/markdown-rendering change.

## Out of scope

- Multi-page release-list walking (targets older than 100 releases miss).
- A configurable/user-editable curated map or per-repo tag template UI (the
  curated map is a code constant in v1).
- Non-GitHub forges (GitLab, Gitea) release notes.
- Backfilling changelogs for already-detected updates, enrichment runs on new
  detection as today; existing rows are re-enriched only when re-detected.
- A dedicated `changelog_repo_ttl` setting (fixed 24h constant in v1).
