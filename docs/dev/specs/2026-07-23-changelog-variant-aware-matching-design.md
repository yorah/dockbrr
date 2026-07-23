# Variant-aware changelog matching for dual-version release tags

Date: 2026-07-23
Status: Approved, ready for planning

## Problem

Detected updates for LinuxServer.io images with a variant flavor show "no
changelog", even though the vendor publishes GitHub Releases with real notes.

Concrete case (`ghcr.io/linuxserver/qbittorrent`):

- Docker image tag dockbrr detects: `5.2.3-libtorrentv1` (app version `5.2.3`,
  flavor `libtorrentv1`).
- Matching GitHub release tag: `libtorrentv1-5.2.3_v1.2.20-ls126` (flavor as a
  prefix, app version `5.2.3`, an embedded libtorrent version `_v1.2.20`, and a
  build suffix `-ls126`).

The image's `org.opencontainers.image.source` label points at
`github.com/linuxserver/docker-qbittorrent`, so the repo resolves correctly
(changelog tier 1). The failure is in release *matching*, not repo resolution.

### Root cause (verified)

In `internal/changelog/github.go`, `findRelease` tries, in order: exact tag
match against `defaultTags`, a partial-prefix scan, then a full-semver
core-equality fallback. For this image:

1. Exact match misses: the docker tag and the release tag do not line up.
2. Partial scan is skipped: `5.2.3` has two dots, not fewer than two.
3. Core-equality fallback rejects every candidate, because
   `detect.parseCore("5.2.3_v1.2.20-ls126")` returns `ok=false`. `parseCore`
   cuts only at `-` and `+`, not `_`, so it splits `5.2.3_v1.2.20` on `.` and
   the component `3_v1` fails `Atoi`.

Every candidate fails to parse, so no release is found. The CHANGELOG.md link
probe then hits tags that do not exist and 404s, leaving an empty result, which
the caller records as "no changelog".

Empirically confirmed with a scratch test: real release tags parse `ok=false`,
the detected version `5.2.3-libtorrentv1` parses `[5 2 3] ok=true`.

### Second wrinkle: variant ambiguity

qbittorrent 5.2.3 ships two libtorrent variants, both with app-core `[5,2,3]`:

- `libtorrentv1-5.2.3_v1.2.20-ls126`
- `5.2.3_v2.0.13-ls469`

Their release bodies differ (distinct CI reports, compare links, "Updating to"
lines). So making the app-core parse is not enough: plain core-equality can not
tell the variants apart and would pick the newest-published, which is the wrong
variant for a `libtorrentv1` image.

## Goals

- A `libtorrentv1` image resolves to the `libtorrentv1` release notes, both for
  a single target release and for a from -> to span.
- No regression for existing images (znc, radarr, postgres, plain semver,
  rolling tags).
- Changes stay inside `internal/changelog`. `detect.parseCore` is a
  cross-package single source of truth and is not touched.

## Non-goals

- Reconciling images whose flavor never appears as a token in any release tag.
- Any new network calls or new sources. This is selection logic over the
  releases already fetched.

## Design

Two package-local mechanisms in `internal/changelog/github.go`.

### 1. `_`-aware `normalizeTag`

`normalizeTag` already strips a leading `<name>-` package prefix and the
`release-` / `v` decorations. Extend it to also truncate at the first `_`.

LinuxServer.io embeds a downstream second version after `_`
(`<appver>_<downstreamver>`); that second version is what makes `parseCore`
fail. Truncating keeps the app-core:

- `libtorrentv1-5.2.3_v1.2.20-ls126` -> strip name prefix -> `5.2.3_v1.2.20-ls126`
  -> cut at `_` -> `5.2.3` -> parses `[5,2,3]`.

Safety: znc (`1.10.2-ls183`), radarr (`6.3.0.10514-ls311`), and postgres
(`REL_16_1`, matched via the exact want-list tier, never via `normalizeTag`)
carry no `_` in the app-core region, so they are unaffected. No app version has
its core *after* a `_`; `_` is always a separator before a downstream/build
component, so truncation only ever drops non-core text.

### 2. Flavor pre-filter

Two new functions, applied once in `Resolve` immediately after the
repo-exists check and before `findRelease` / span selection, so the entire
downstream selection operates on a flavor-consistent subset.

`extractFlavor(version) string`

- Take the suffix after the numeric core.
- Split it on `-`; the first segment is the flavor, unless that segment is a
  recognized build or pre-release marker (`ls\d+`, or anything matched by the
  existing pre-release detection such as `rc`, `beta`, `alpha`).
- Examples: `5.2.3-libtorrentv1` -> `libtorrentv1`; `5.2.3-alpine` -> `alpine`;
  `1.10.2-ls183` -> `""` (build suffix, no flavor); `5.2.3` -> `""`.

`filterByFlavor(rels, flavor) []ghRelease`

- If `flavor == ""`, return `rels` unchanged.
- Otherwise, if at least one release's raw tag contains the flavor token, keep
  only those releases; if none contains it, return `rels` unchanged.

Applying the filter once, before both selection paths, means:

- Single-release path (`findRelease` core-equality fallback) picks the
  `libtorrentv1` release, because the `5.2.3_v2.0.13` variant was already
  removed.
- Span path (`releasesInSpan`, for a 5.2.2 -> 5.2.3 jump) aggregates only
  `libtorrentv1` releases, never mixing in v2 notes.

### Monotonic safety

The filter only narrows the candidate set when the flavor is non-empty and
actually matches a candidate. Images with no flavor, or a flavor that matches
no release, get exactly today's behavior. The znc range test
(`1.10.2-ls181` -> `1.10.2-ls183`) is unaffected because `ls183` is a build
marker, so `extractFlavor` returns `""`.

## Worked example (qbittorrent 5.2.2-libtorrentv1 -> 5.2.3-libtorrentv1)

1. Repo resolves to `linuxserver/docker-qbittorrent` via the source label.
2. Releases fetched (both variants interleaved).
3. `extractFlavor("5.2.3-libtorrentv1")` = `libtorrentv1`.
4. `filterByFlavor` keeps only `libtorrentv1-*` releases.
5. `normalizeTag` now parses their app-core as `[5,2,3]`.
6. `findRelease` target = newest `libtorrentv1-5.2.3_...` (ls126).
7. `fromCore=[5,2,2]`, `toCore=[5,2,3]`: span path aggregates the
   `libtorrentv1` 5.2.3 builds under headings; the v2 releases are gone.

## Files

- `internal/changelog/github.go`
  - `normalizeTag`: add `_` truncation.
  - new `extractFlavor`, `filterByFlavor`.
  - one call site in `Resolve`, after the exists check.
- `internal/changelog/github_test.go`: qbittorrent single-target case and
  from -> to range case (variant isolation asserted: v1 notes present, v2 notes
  absent).
- `internal/changelog/repo_internal_test.go`: `extractFlavor` unit cases
  (`libtorrentv1`, `alpine`, `ls183` -> empty, plain semver -> empty).

No changes to the `detect` package.

## Testing

- New table cases as above.
- Full `go test ./internal/changelog/...` stays green (regression guard for
  znc / radarr / postgres / rolling-tag paths).
- `mise run check` before completion.
