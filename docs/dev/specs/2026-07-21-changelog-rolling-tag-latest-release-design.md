# Changelog: latest-release fallback for rolling tags

Date: 2026-07-21
Status: Approved (design)

## Problem

Images pinned to a rolling tag (`latest`, `master`, `stable`, `master-omnibus`, ...)
carry no semver in their tag or `org.opencontainers.image.version` label. The
GitHub changelog source resolves the repo correctly but looks up release notes by
version tag, so nothing matches: no release is tagged `master-omnibus`, and there
is no `CHANGELOG.md` at that ref. Only the OCI-label fallback fires, yielding a
bare repo link with no notes text.

Concrete case: `ghcr.io/analogj/scrutiny:master-omnibus`.
- Repo resolves to `AnalogJ/scrutiny` (ghcr host + `org.opencontainers.image.source`).
- `org.opencontainers.image.version` = `master-omnibus` (not semver).
- Releases are tagged `v0.9.2`, `v0.9.1`, ... so `findRelease` misses.
- User sees a link to github.com/AnalogJ/scrutiny, no release notes.

For a rolling tag the running digest is built from the repo tip, so the repo's
latest stable release is the right proxy for "what shipped."

## Goal

When the target version is a non-semver rolling tag and the GitHub repo resolved,
show the repo's latest stable release notes instead of missing. Semver images are
unchanged: a semver version that fails to match a release must NOT fall back to the
latest release (running v1.2 vs latest v3.0 would be wrong).

## Design

Single-source change, contained in `internal/changelog/github.go`. No new source,
no schema change, no extra API call.

### Trigger

Inside `GitHubSource.Resolve`, in the existing `findRelease` miss branch (before the
`changelogLink` probe). Fire the latest-release fallback only when the target
version is not parseable as semver:

```go
if _, parseable := detect.ParseCore(in.Version); !parseable {
    if latest, ok := latestStableRelease(rels); ok {
        note := fmt.Sprintf("_Latest release for rolling tag `%s`._\n\n", in.Version)
        return Result{Text: note + latest.Body, URL: latest.HTMLURL}, nil
    }
}
```

`in.Version` is `firstNonEmpty(Update.ToVersion, Update.Tag)`; for a rolling tag it
is the rolling identifier itself (e.g. `master-omnibus`). This branch is only
reachable when:
- `in.Version != ""` (digest-only updates return earlier in `Resolve`), and
- the repo `exists` (confirmed by `fetchReleases`), and
- `findRelease` already missed.

A parseable (semver) version skips the fallback entirely, preserving current
behavior for semver images with an unmatched tag scheme.

### New helper

```go
// latestStableRelease returns the highest-semver stable release in rels
// (pre-releases skipped), for rolling-tag images whose version matches no
// release tag. Mirrors findRelease's prefix-scan / CoreLess ranking.
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

Reuses existing helpers (`normalizeTag`, `isPrerelease`, `detect.ParseCore`,
`detect.CoreLess`) and operates on the page-1 releases already fetched. Highest
semver, not first-listed, because GitHub orders releases by publish date and a
backport can precede the newest release.

### Note text

Prepend one italic line so the shown version is honestly labeled as the latest
release, not a guaranteed match for the exact running digest:

```
_Latest release for rolling tag `master-omnibus`._

<release body>
```

The combined text is capped by the resolver's existing `sanitizeText`
(`maxChangelogBytes`); no span-cap logic is involved (single release, not a span).

## Edge cases

- **Prerelease-only repo / no stable release:** `latestStableRelease` returns
  `found=false`; flow falls through to the existing `changelogLink` probe, then the
  OCI-label link. Same as today.
- **Semver image, unmatched tag scheme:** version parses as semver, so the fallback
  is skipped. Unchanged behavior.
- **Digest-only update (`in.Version == ""`):** `Resolve` returns before this branch.
  Not affected.
- **Releases API error / rate limit:** handled earlier in `Resolve` (the
  `changelogLink`-on-error path and `ErrRateLimited`). Not affected.
- **Caching:** repo-mapping cache behavior unchanged (positive resolution cached).
  The changelog text itself is not cached in the source; for a `current` row the
  scanner's `HasResolvedCurrentAtDigest` guard prevents re-resolve until the digest
  moves, at which point `DeleteStaleCurrent` drops the stale baseline and a fresh
  latest-release resolve runs.

## Testing

Unit tests in `internal/changelog/github_test.go`:

1. `latestStableRelease`:
   - picks the highest-semver stable release from a mixed list;
   - skips pre-releases (`v0.9.0-rc1`);
   - returns `found=false` on a prerelease-only or empty list.
2. `Resolve`, rolling version (`master-omnibus`), releases
   `[v0.9.2, v0.9.1, v0.9.0-rc1]` → `Text` begins with the note line and contains
   the `v0.9.2` body; `URL` is the `v0.9.2` release html_url.
3. `Resolve`, rolling version, prerelease-only releases → falls through to
   `changelogLink` / empty (no latest-release text).
4. `Resolve`, semver version (`1.2.3`) that matches no release → no latest-release
   fallback (existing behavior preserved).
5. Note text contains the rolling-tag string verbatim.

## Out of scope

- `/releases/latest` endpoint (extra API call; can disagree with highest-semver on
  backport-heavy repos).
- Falling back to the latest release for semver images that miss.
- Any change to `RegistrySource`, `OCISource`, or the resolver chain ordering.
