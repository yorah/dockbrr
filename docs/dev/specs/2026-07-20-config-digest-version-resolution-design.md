# Config-digest version resolution

Date: 2026-07-20
Status: design approved

## Problem

Floating-tag services (e.g. `technitium/dns-server:latest`) can display a wrong
version, suppress the correct one, and record nonsensical severities. Two
independent bugs combine.

### Observed case

`technitium/dns-server:latest`:

- Running container digest `sha256:99c250f0…` (old latest), remote latest now
  `sha256:3580381de00b…`. Real drift, real rebuild.
- dockbrr shows version `24.04`. The running app is Technitium DNS **15.2**;
  latest is **15.4.0**. The repo publishes tags `latest, 15.4.0, 15.3.0,
  15.2.0, …` and has **no `24.04` tag**.
- Changelog only links to Docker Hub (no version diff), even though a real
  `15.x` changelog exists.

### Root cause

The image ships a wrong OCI label:

```
org.opencontainers.image.version = "24.04"
```

That is the Ubuntu base-image version, not the app version. dockbrr trusts it
faithfully. Two failures follow.

**Bug 1 — mislabel suppresses the reverse-lookup safety net.**
For a fully-floating tag, `detect.go` derives the version from the OCI label
(`svc.ImageVersion`) and only falls back to a digest reverse-lookup when a
version end is blank. The 5b gate is:

```go
if semverOrEmpty(tag) == "" && ClassifyTag == TagFloating && (fromVer == "" || toVer == "")
```

The bogus label fills both `fromVer` and `toVer` with `"24.04"`, so neither is
blank and the reverse-lookup never runs. The real `15.x` tags are never
consulted.

**Bug 2 — reverse-lookup matches at the wrong digest level, so it fails for
multi-arch even when it runs.**
`reverseVersions` names a tag by `Head`-ing candidate tags and comparing the
returned digest against the target. But `Head` returns the **manifest-list
(index) digest**, and multi-arch tags get a *distinct* index digest per tag even
when the underlying platform image is byte-identical:

| tag | index (served) digest | amd64 platform digest |
|-----|----------------------|----------------------|
| `latest` | `sha256:3580381d…` | `sha256:b2b6eee…` |
| `15.4.0` | `sha256:df7d90ef…` | `sha256:b2b6eee…` |
| `15.3.0` | `sha256:c5ea91d9…` | `sha256:d6e37201…` |
| `15.2.0` | `sha256:23d3b63d…` | `sha256:85c2cfd4…` |

`latest` **is** `15.4.0` (same amd64 platform digest `b2b6eee`), but their index
digests differ (`3580381` vs `df7d90ef`). The comparison
`dg == target.Digest || dg == target.PlatformDigest` crosses levels
(candidate index digest vs target index/platform digest) and never matches for
multi-arch. Reverse-lookup is silently broken for the common multi-arch case,
not just this image. The same broken `reverseVersions` backs
`resolveCurrentVersion` (the up-to-date display path).

The running container's stored `CurrentDigest` is `docker` `RepoDigest`, which
for a multi-arch pull is the **index digest** too, so the "from" side is
unnameable across tags by the same reasoning.

## Decisions

1. **Digest-matched tag wins.** For a floating tag, reverse-lookup the digest to
   a real repo semver tag first. The OCI `image.version` label is used only as a
   fallback when no tag matches. The displayed version always corresponds to
   something real in the repository.
2. **Match on the config digest (image ID).** The config digest is the true
   per-platform image identity: identical built image ⇒ identical config digest,
   independent of manifest-list packaging. It is available for free on the
   running side (`docker` `ImageID`) and the target side (`Resolve` already
   fetches the config), which sidesteps the entire index-vs-platform digest
   mismatch of Bug 2.

## Design

### Identity: config digest everywhere

All version-naming matches compare **config digests**, never index or platform
manifest digests. Three sources:

1. **Running side (free).** `docker.Container.ImageID` is already collected in
   `internal/docker/collect.go` but dropped at the discovery boundary. Thread it
   through: `discovery.Service.ImageID` → new `service.image_id` column → visible
   to detect. For the technitium container this is the amd64 config digest
   (`sha256:2a8e6b85…`).
2. **Target side (free).** `registry.Resolve` already calls `img.ConfigFile()`.
   Add `RemoteImage.ConfigDigest`, populated from `img.ConfigName()` (reads the
   already-fetched manifest, no extra blob fetch).
3. **Candidate side (cached).** To name a tag, resolve it to its config digest.
   Extend the permanent `tag_digests` cache with a `config_digest` column.
   Exact-semver tags are immutable, so each is fetched once and cached forever.

### Reverse-lookup algorithm

Replace the internals of `reverseVersions` / `tagDigest` with a config-digest
match:

```
nameVersion(repo, configDigest) -> tag or "":
  1. tags := semverTagsDesc(listTags(repo))      # stable exact-semver, newest-first (reused)
  2. for each candidate tag (cap reverseScanCap = 50):
       cd := cachedConfigDigest(repo, tag)         # tag_digests.config_digest, else resolve + cache
       if cd == configDigest: return tag           # matched a real release
       if rateLimited: break                        # best-effort abort, leave blank
  3. return ""                                     # no tag matches (HEAD build / unreleased)
```

Called with the running config digest to name `from`, and the target config
digest to name `to`. Both ends now resolve reliably; the "from" side no longer
depends on an unavailable platform/index digest.

### Label fast-path (cost bound)

Before the full scan, cheaply trust a *correct* label:

```
if label parses as exact semver
   and that tag exists in the repo
   and cachedConfigDigest(repo, labelTag) == configDigest:
     use the label, skip the full scan   # 1 lookup
```

Correctly-labeled images (e.g. linuxserver `X.Y.Z-lsNNN` streams whose label
matches a release tag) cost one lookup. Technitium fails this test (`24.04` is
not a tag) and falls through to the full scan, which finds `15.4.0`.

### Gate fix (Bug 1)

Drop the `(fromVer == "" || toVer == "")` precondition on the 5b block in
`detect.go`. For any fully-floating tag, run the digest reverse-lookup and let a
matched real tag **override** the label. The label is consulted only when
reverse-lookup returns `""`.

Precedence for each version end of a floating tag:

1. digest-matched repo tag (via fast-path or full scan)
2. OCI `image.version` label
3. blank

Partial-semver floating tags (`1`, `1.31`) remain excluded (their
`semverOrEmpty` is non-empty): the stream name is not a release name, unchanged
from today.

### Severity impact

With both ends named from real tags (`from` ≈ `15.2.0`, `to` = `15.4.0`),
`Severity` returns `minor` instead of the nonsense `major`/`digest-only` derived
from `24.04`. The changelog resolver continues to key off the target tag.

### Caching and negative-cache

Q1 makes reverse-lookup run on every floating-tag detect, so cost control
matters.

- **Positive cache (candidates).** `tag_digests.config_digest` per `(repo, tag)`.
  Immutable exact-semver tags ⇒ fetched once, then pure cache hits. Zero
  candidate network from the second cycle on.
- **Negative cache (per image).** `resolveCurrentVersion` today persists a
  version only when non-empty (`SetResolvedVersion` skips `""`), so an image
  whose digest matches no tag rescans all 50 candidates **every cycle forever**.
  Fix: record the resolution outcome even when empty — add an
  `image.version_resolved` marker so `resolved_version = ""` + `version_resolved
  = 1` means "scanned, nothing matched; do not rescan until the digest changes".
  Distinguishes a negative result from "never scanned".
- **Bounds retained.** `reverseScanCap = 50`, newest-first ordering, rate-limit
  abort (`IsRateLimited` → stop, leave blank, best-effort). Detection and
  digest-compare never hard-fail on a reverse-lookup error.

Steady-state cost per floating service:

- correctly labeled: 1 lookup (fast-path), then cached.
- mislabeled/unlabeled (technitium): up to 50 lookups on first encounter of a
  new digest, then positive- + negative-cached ⇒ ~0 until the next drift.

## Data model changes (migration 0012)

Highest existing migration is `0011` (three duplicate-`0010` files pre-exist; do
not touch them). Next is `0012`.

- `service.image_id TEXT` — running config digest.
- `tag_digests.config_digest TEXT` (nullable) — extend the cache. Keep the
  existing `digest` column intact; it still serves the exact-semver
  `NewerSemverTag` path. `Get`/`Put` gain a config-digest variant.
- `image.version_resolved` (boolean/int, default 0) — negative-cache marker for
  `resolveCurrentVersion`.

All columns nullable/defaulted; no data migration. Existing rows keep working.

## Back-compat

- New columns default cleanly; existing installs upgrade in place.
- `service.image_id` is empty for services discovered before the upgrade, so the
  `from` reverse-lookup degrades to label/blank until the next discovery
  repopulates it. Strictly better than current behavior, not a regression.
- The served-digest `tag_digests` consumer is untouched (separate column).

## Testing

- **`detect` unit:**
  - config-digest match names the tag.
  - label overridden when it mismatches every real tag.
  - label fast-path trusted when it matches a tag's config digest.
  - no-match returns `""` and sets the negative-cache marker.
  - rate-limit aborts cleanly, leaves version blank, no hard failure.
- **`semver` unit:** unchanged (`semverTagsDesc` reused as-is).
- **`store`:** `tag_digests` config-digest round-trip; `version_resolved`
  negative-cache round-trip; migration `0012` applies clean.
- **`registry`:** existing multi-arch resolver tests extended to assert
  `RemoteImage.ConfigDigest` is populated from `ConfigName()`.
- **Regression fixture (the bug):** mislabeled `image.version = 24.04`, repo tags
  `15.4.0/15.3.0/15.2.0`, running config digest equal to `15.4.0`'s amd64 config
  digest. Assert: version resolves to `15.4.0`, severity `minor`, label ignored.
  This is a named test guarding the reported case.

## Out of scope

- No change to the exact-semver pin auto-suggest path (`NewerSemverTag`); it
  keeps using the served-digest cache column.
- No change to apply behavior: a floating tag still floats the same tag. Version
  naming is cosmetic + informational (display, severity, changelog keying).
- No cross-registry config-digest normalization beyond what `go-containerregistry`
  already returns.
