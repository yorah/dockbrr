# Changelog raw-CHANGELOG fallback on releases-API error

Date: 2026-07-20
Status: design approved

## Problem

A floating-tag service whose image declares a GitHub source but whose repo
publishes no GitHub Releases (only a `CHANGELOG.md` file) shows the Docker Hub
page as its changelog instead of the GitHub `CHANGELOG.md`, whenever the GitHub
releases API call fails.

### Observed case

`technitium/dns-server:latest` (now correctly resolved to `15.4.0`):

- The image label `org.opencontainers.image.source =
  https://github.com/TechnitiumSoftware/DnsServer` is present and dockbrr resolves
  the GitHub target from it.
- `TechnitiumSoftware/DnsServer` publishes **no GitHub Releases**, but does ship
  `CHANGELOG.md` at each `v`-prefixed tag. Verified:
  `https://raw.githubusercontent.com/TechnitiumSoftware/DnsServer/v15.4.0/CHANGELOG.md`
  → 200.
- The dashboard's changelog links to
  `https://hub.docker.com/r/technitium/dns-server`.

### Root cause

`GitHubSource.Resolve` (`internal/changelog/github.go`) fetches the releases API
first, and returns immediately on any fetch error:

```go
rels, reachedFrom, exists, err := s.fetchReleases(...)
if err != nil {
    return Result{}, err   // aborts before changelogLink
}
```

The raw `CHANGELOG.md` fallback (`changelogLink`) is only reached later, on a
successful-but-no-match fetch. So when the releases API errors, the source aborts
without ever trying the raw probe.

The common trigger is the **unauthenticated GitHub API rate limit** (60/h). With
no GitHub token configured, the releases call returns HTTP 403 with
`X-RateLimit-Remaining: 0`, which maps to `ErrRateLimited`. `Resolve` returns
that error; the resolver chain logs it and falls through to the Docker Hub
source, which hits and is cached.

The raw probe uses `raw.githubusercontent.com`, which is **not** subject to the
API rate limit and does **not** need the releases list, so it would succeed for
Technitium if it were reached. The releases-error early-return is the only thing
blocking it.

## Decision

On a releases-fetch error, attempt the raw `CHANGELOG.md` probe before giving up.
A hit returns the GitHub link and drops the error; a miss propagates the original
error so a genuine rate-limit still surfaces. When the raw probe succeeds during
a rate-limit, prefer the recovered link and drop the rate-limit signal (the user
gets a real changelog rather than a warning).

## Design

Single change in `GitHubSource.Resolve`, at the releases-fetch error branch:

```go
rels, reachedFrom, exists, err := s.fetchReleases(ctx, owner, name, pages, func(oldest ghRelease) bool {
    c, ok := detect.ParseCore(normalizeTag(oldest.TagName))
    return ok && !detect.CoreLess(fromCore, c)
})
if err != nil {
    // Releases API failed (commonly an unauthenticated rate-limit). The raw
    // CHANGELOG.md probe hits raw.githubusercontent.com, which is not subject to
    // that limit and does not need the releases list, so try it before giving
    // up. A hit returns a link (dropping the error); a miss propagates the
    // original error so a genuine rate-limit still surfaces.
    if link, ok, lerr := s.changelogLink(ctx, owner, name, tgt.tags(in.Version)); lerr == nil && ok {
        return Result{URL: link}, nil
    }
    return Result{}, err
}
```

`owner`, `name`, and `tgt` are already in scope at this point; `in.Version` is
guaranteed non-empty (the method returns early at the top when it is empty).
`tgt.tags(in.Version)` is the same candidate-tag set the existing no-match
fallback uses (`defaultTags` yields `["<v>", "v<v>", "release-<v>"]`), so the
`v`-prefixed tag Technitium uses is probed.

### Behavior after the change

- Errored/rate-limited releases API + raw `CHANGELOG.md` exists (Technitium
  `v15.4.0`): returns the GitHub blob link
  (`https://github.com/<owner>/<repo>/blob/v15.4.0/CHANGELOG.md`), `err = nil`.
  No Docker Hub, no rate-limit hint.
- Errored/rate-limited releases API + no raw `CHANGELOG.md` at any candidate tag:
  propagates the original error (rate-limit signal preserved as today).
- All success paths unchanged: an empty-but-successful releases list already
  reaches `changelogLink`; a matched release still returns its notes.

### Caching

The repo-resolution cache is left untouched on this path. The errored releases
fetch is inconclusive about repo existence, so no positive/negative entry is
written; the next scan cycle retries, and once the API is reachable again normal
caching resumes. This keeps the change to one branch with no partial-state
reasoning.

## Out of scope

- No change to the source ordering or the resolver chain: GitHub releases stay
  first, Docker Hub stays the last network fallback.
- No new setting. Configuring a GitHub token remains the way to get full release
  notes (and higher rate limits); this change only makes the link-level fallback
  reachable without one.
- No frontend/API/schema change. `changelogLink` and `Result` are unchanged.

## Testing

In `internal/changelog/github_test.go`, add error-path coverage using a bespoke
`httptest` server (the shared `ghServer` helper cannot emit a 403, so these tests
stand up their own mux):

1. **Rate-limited releases + raw CHANGELOG hit → link, no error.** `/repos/.../releases`
   returns 403 with `X-RateLimit-Remaining: 0`; the raw `CHANGELOG.md` at the
   `v`-prefixed tag returns 200. Assert `res.URL` is the GitHub blob link,
   `res.Text` is empty (link-only), and `err` is nil (rate-limit dropped).
2. **Rate-limited releases + raw CHANGELOG miss → error preserved.** Same 403
   releases endpoint; every `CHANGELOG.md` path returns 404. Assert `err` is
   `ErrRateLimited` and the result is empty.

Existing tests (`TestGitHubChangelogFallback`, `TestGitHubRateLimitedYieldsErrRateLimited`,
success paths) must stay green — the change only adds a branch on the error path.
