# Changelog + version display for LinuxServer.io-style tags

Date: 2026-07-21
Status: Approved (design)

## Problem

Two related defects on LinuxServer.io (LSIO) `ghcr.io/linuxserver/*` images, both
rooted in how their release/version strings are shaped: `<upstream-name>-<semver>-lsNNN`
(release tags) and inconsistent OCI `org.opencontainers.image.version` labels.

Observed:

- **znc — no changelog.** `ghcr.io/linuxserver/znc:latest`, target version `1.10.2`.
  Repo resolves to `linuxserver/docker-znc` (via `org.opencontainers.image.source`).
  Its releases are tagged `znc-1.10.2-ls183`, `znc-1.10.2-ls182`, ... The `znc-`
  prefix defeats matching: `findRelease` compares raw tag names (no `1.10.2` match),
  the partial-prefix scan bails for full (2-dot) semver, and `detect.ParseCore("znc-1.10.2-ls183")`
  fails so no core-based path can see these releases. The rolling-tag latest-release
  fallback does not fire (version `1.10.2` parses as semver). Result: "No changelog available."

- **znc — inconsistent version display.** Dashboard shows `znc-1.10.2-ls181 -> 1.10.2`.
  The from-side keeps the (name-prefixed, suffixed) OCI label; the to-side is a bare
  `X.Y.Z` tag that `matchVersionByDigest` reverse-resolved from the digest, discarding
  the build suffix. Mixed formats, and the newer `lsNNN` build is hidden.

Contrast, working correctly today: `ghcr.io/linuxserver/thelounge:latest` shows
`v4.5.1-ls225 -> v4.5.2-ls228`. thelounge's upstream tags carry no name prefix
(`v4.5.2-ls228`), so the release matches and the label is kept on both sides. This is
the *correct* behavior and must be preserved: for LSIO the upstream version is often
static across rebuilds and only `lsNNN` bumps, so `1.10.2-ls181 -> 1.10.2-ls183` is a
real update that collapses to an invisible `1.10.2 -> 1.10.2` if the suffix is stripped.

## Goals

1. **Changelog (B):** LSIO images with `<name>-<semver>-lsNNN` release tags resolve to
   their release notes.
2. **Version display (C):** the digest-reverse-match must not *downgrade* a
   version-bearing OCI label to a barer form. Keep the more precise label when it names
   the same version; only override the label when it is genuinely wrong (different core,
   e.g. a base-OS-labeled image).

Non-goal: manufacturing a build suffix for a side whose label is genuinely bare (see
Out of scope).

## Design

Three changes. One shared helper in `detect`; the changelog and version-display fixes
each consume it.

### Shared helper: `detect.StripNamePrefix`

A lenient strip of a leading `<name>-` package prefix, so a name-prefixed LSIO tag can
be parsed as semver. Added to `internal/detect/semver.go` next to `ParseCore` (the
canonical version-parsing home).

```go
// namePrefixRe matches a leading alphabetic package-name segment followed by "-"
// and a version core (optionally v-prefixed), e.g. "znc-1.10.2-ls183" ->
// "1.10.2-ls183". It requires the remainder to begin with an optional "v" then a
// digit so it never eats a bare or v-prefixed version.
var namePrefixRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-(v?\d.*)$`)

// StripNamePrefix removes a leading "<name>-" package prefix from a tag/version
// string when one precedes the version core, else returns s unchanged. It is
// deliberately narrow: only an alphanumeric word directly followed by "-" and a
// version digit is stripped, so "release-1.2.3" / "znc-1.10.2-ls183" are stripped
// while "1.2.3", "v1.2.3", "master-omnibus" (no digit) are not.
func StripNamePrefix(s string) string {
    if m := namePrefixRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
        return m[1]
    }
    return s
}
```

Notes:
- The canonical `parseCore` is **not** changed (broad blast radius: `Severity`,
  `tagStream`, `semverTagsDesc`, `NewerSemverTag`). The strip is applied explicitly at
  the two call sites below.
- `master-omnibus` is not stripped (no digit after the dash), so the rolling-tag
  fallback path is unaffected.

### B1 — changelog `normalizeTag` strips the name prefix

`internal/changelog/github.go`, `normalizeTag`:

```go
func normalizeTag(tag string) string {
    tag = detect.StripNamePrefix(tag)
    return strings.TrimPrefix(strings.TrimPrefix(tag, "release-"), "v")
}
```

This lets `ParseCore(normalizeTag("znc-1.10.2-ls183"))` succeed, which every
core-based path (`latestStableRelease`, `releasesInSpan`, `isPrerelease`, and B2
below) depends on. `isPrerelease("1.10.2-ls183")` stays `false` (the `ls183` suffix is
not a pre-release marker), so LSIO builds remain stable releases.

### B2 — `findRelease` core-equality fallback for full semver

`findRelease` currently: (1) exact raw-tag match against `want`; (2) a partial-prefix
scan restricted to versions with fewer than two dots (`8.8` -> newest `8.8.x`). A full
semver like `1.10.2` that matches no raw tag falls through to a miss. Add a final pass:
match releases whose *normalized* core equals the version's core.

```go
// Full-semver core-equality fallback: LSIO-style tags ("znc-1.10.2-ls183") carry a
// name prefix and/or build suffix, so neither the raw exact match nor the partial
// (<2-dot) scan finds them. Match by parsed core instead. An exact normalized-tag
// match (a suffix-bearing version like "1.10.2-ls183") wins outright; otherwise the
// first-listed same-core release wins, which is the newest-published and thus the
// highest build for a given core (LSIO publishes ascending lsNNN over time).
if vCore, ok := detect.ParseCore(version); ok {
    normVer := normalizeTag(version)
    var best ghRelease
    found := false
    for _, rel := range rels {
        norm := normalizeTag(rel.TagName)
        c, cok := detect.ParseCore(norm)
        if !cok || c != vCore || isPrerelease(norm) {
            continue
        }
        if norm == normVer {
            return rel, true
        }
        if !found {
            best, found = rel, true
        }
    }
    if found {
        return best, true
    }
}
return ghRelease{}, false
```

Placement: after the existing partial-prefix scan, replacing the final
`return ghRelease{}, false`. It only runs when the earlier matches missed, so normal
repos (whose `1.10.2` matches `1.10.2` / `v1.10.2` / `release-1.10.2` via `defaultTags`
at the exact-match step) never reach it. For znc with C keeping the suffix, version
`1.10.2-ls183` normalized-exact-matches `znc-1.10.2-ls183`; for a bare `1.10.2` version
it core-matches and takes the newest build.

### C — version display keeps the more precise label

The reverse-digest match (`matchVersionByDigest`) returns a bare `X.Y.Z` tag (its scan
pool, `semverTagsDesc`, excludes any tag containing `-`/`+`). Today the result
unconditionally overwrites the version, discarding a suffix-bearing OCI label. Gate the
overwrite so it only fires when it is a genuine correction, not a downgrade.

New predicate in `internal/detect/detect.go`:

```go
// preferDigestTag reports whether a bare, digest-reverse-matched tag should replace
// the current OCI-label-derived version string. It replaces the label only when the
// label does not already name the same version: an unparseable label (e.g. a base-OS
// value, or empty) yields the tag; a label whose lenient core differs from the tag's
// is wrong for this image and yields the tag; a label whose core matches the tag is
// the same version but potentially more precise (carries "-lsNNN"), so it is kept.
func preferDigestTag(digestTag, label string) bool {
    lc, lok := parseCore(StripNamePrefix(label))
    if !lok {
        return true // label not a version (base-OS label, rolling word, empty)
    }
    dc, _ := parseCore(digestTag) // digestTag is always bare semver
    return lc != dc               // differ: correct the label; equal: keep it
}
```

Applied at both reverse-match sites:

- **Update path (`Detect`, section 5b).** `toVer` is seeded from the OCI label
  (line ~202) before the reverse match. Change the two overrides:

  ```go
  if v, _ := d.matchVersionByDigest(ctx, repo, svc.CurrentImageID, semverTagPref(svc.ImageVersion)); v != "" && preferDigestTag(v, fromVer) {
      fromVer = v
  }
  if v, _ := d.matchVersionByDigest(ctx, repo, targetRemote.ConfigDigest, semverTagPref(toLabel)); v != "" && preferDigestTag(v, toVer) {
      toVer = v
  }
  ```

- **Up-to-date current row (`resolveCurrentVersion`).** It prefers the matched tag over
  the label unconditionally; apply the same gate:

  ```go
  ver := tagName
  if ver == "" || !preferDigestTag(tagName, label) {
      ver = label
  }
  ```
  (When `tagName == ""` and `conclusive`, `ver` falls back to the label as today.)

Effect on znc: from-label `znc-1.10.2-ls181` -> `StripNamePrefix` -> core `1.10.2` ==
bare tag `1.10.2` core -> keep the label `znc-1.10.2-ls181`. The base-OS mislabel case
(technitium: label `24.04`, matched app tag `15.x`) still overrides, cores differ.

## Edge cases

- **thelounge (regression guard).** No bare `4.5.2` tag matches its digest, so the
  reverse match returns "" and the label `v4.5.2-ls228` is kept on both sides -
  unchanged today, unchanged after. If a bare tag *did* match, `preferDigestTag`
  (same core) keeps the richer label. Either way the `-lsNNN` survives.
- **radarr (regression guard).** Release tags `6.3.0.10514-ls311` have no name prefix;
  `StripNamePrefix` leaves them untouched and the existing exact/suffix match still
  wins before B2. Version `6.3.0.10514-ls311` -> core `[6,3,0]` (4th component ignored
  by `ParseCore`, as today).
- **Bare-label to-side.** If an image's label is genuinely bare (`1.10.2`, no build
  number), C shows `1.10.2`; the build number is not recoverable without extra registry
  calls (Out of scope). The changelog (B) still resolves via core-equality, so notes
  appear regardless.
- **Monorepo name collisions.** A repo with `client-1.0.0` and `server-1.0.0` release
  tags both strip to `1.0.0`; B2 core-match could pick either. Accepted: LSIO repos are
  single-app, and the match is best-effort enrichment, not a mutation.
- **Non-semver rolling tags.** `master-omnibus` has no digit after a dash, so
  `StripNamePrefix` is a no-op and the existing rolling-tag latest-release fallback is
  untouched.
- **Severity side effect (benign improvement).** C does not change `Severity`
  inputs beyond keeping a richer label; `Severity` still runs on `fromVer`/`toVer`.
  A kept `znc-1.10.2-ls181` is unparseable by the canonical `parseCore` (name prefix),
  so `Severity` yields `digest-only` as it does today - no regression. (Making
  `Severity` LSIO-aware is out of scope.)

## Testing

`internal/detect/semver_test.go`:
- `StripNamePrefix`: strips `znc-1.10.2-ls183` -> `1.10.2-ls183`, `release-1.2.3` ->
  `1.2.3`; leaves `1.2.3`, `v1.2.3`, `master-omnibus`, `` unchanged.

`internal/detect/detect_test.go`:
- `preferDigestTag`: `("1.10.2","znc-1.10.2-ls181")` -> false (keep label);
  `("15.1.2","24.04")` -> true (override); `("1.10.2","")` -> true;
  `("1.10.2","master-omnibus")` -> true.
- Reverse-match update path: same-core suffixed label is kept as `toVer`/`fromVer`;
  different-core (base-OS) label is overridden.

`internal/changelog/github_test.go`:
- `normalizeTag("znc-1.10.2-ls183")` -> `1.10.2-ls183`.
- `findRelease` core-equality: version `1.10.2-ls183`, releases
  `[znc-1.10.2-ls183, znc-1.10.2-ls182]` -> exact normalized match returns `ls183`.
- `findRelease` bare version `1.10.2`, releases `[znc-1.10.2-ls183, znc-1.10.2-ls182]`
  -> newest build `ls183`.
- `Resolve` end-to-end: `ghcr.io/linuxserver/znc` (source label -> `docker-znc`),
  version `1.10.2-ls183`, releases as above -> `Text` is the `ls183` body, `URL` its
  html_url.
- Regression: a plain semver `1.10.2` matching a plain `1.10.2` release still resolves
  via the exact-match step (B2 not reached).

## Out of scope

- Recovering a build suffix for a side whose OCI label is bare (would need extra
  registry HEADs against suffixed tags).
- Making `matchVersionByDigest` scan suffixed (`-lsNNN`) tags.
- LSIO-aware `Severity` (four-component versions, `lsNNN` as a change signal).
- Any change to the rolling-tag latest-release fallback, `RegistrySource`,
  `OCISource`, or resolver-chain ordering.
