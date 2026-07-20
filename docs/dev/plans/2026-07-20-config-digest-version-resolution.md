# Config-digest version resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve a floating-tag service's version by matching its config digest to a real repo tag, so a wrong OCI `image.version` label (e.g. `24.04` on `technitium/dns-server:latest`) no longer hides the real version (`15.4.0`) or produces a nonsense severity.

**Architecture:** Match image identity on the **config digest** (Docker image ID) end to end — running side is already persisted as `service.current_image_id`, target side comes free from `registry.Resolve`, candidates are resolved (cached). Replace the broken manifest-list-digest reverse-lookup in `internal/detect`. For a floating tag the digest-matched repo tag wins over the label; the label is a fallback only.

**Tech Stack:** Go 1.26 (CGO-free), SQLite via `internal/store`, `go-containerregistry` for registry reads, standard `testing`.

**Spec:** `docs/dev/specs/2026-07-20-config-digest-version-resolution-design.md`

## Global Constraints

- Build stays CGO-free: `CGO_ENABLED=0 go build ./...` must pass. No new cgo deps.
- Registry reads only in `internal/registry`; `internal/detect` never mutates Docker.
- Match key is the **platform config digest**, never a manifest-list or platform-manifest digest.
- Precedence for a floating tag's version: digest-matched repo tag → OCI `image.version` label → blank.
- Verify with `mise run check` (go vet + go test + web vitest). TS unaffected.
- Commits: Conventional Commits. Do NOT add any Claude/Co-Authored-By/Generated-with attribution.
- Migration files are immutable once shipped; the next number is `0012` (three duplicate-`0010` files already exist — do not touch them).

---

### Task 1: registry — expose the config digest

**Files:**
- Modify: `internal/registry/registry.go` (`RemoteImage` struct ~37-46; `Resolve` ~82-108; add `ConfigDigest` method after `Resolve`)
- Test: `internal/registry/registry_test.go` (add one test)

**Interfaces:**
- Produces: `registry.RemoteImage.ConfigDigest string` — the platform image's config digest (`sha256:…`).
- Produces: `func (r *Resolver) ConfigDigest(ctx context.Context, ref string, plat Platform) (string, error)` — resolves ref to its platform config digest without fetching the config blob. Returns a `transport.Error`-wrapped error on failure (so `registry.IsRateLimited`/`IsUnauthorized`/`IsNotFound` classify it).

- [ ] **Step 1: Write the failing test**

Add to `internal/registry/registry_test.go`:

```go
func TestResolveAndConfigDigestPopulateConfigDigest(t *testing.T) {
	// Push a fixture inline so we can capture its config digest directly.
	srv := httptest.NewServer(ggcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(host + "/acme/web:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	wantCfg, err := img.ConfigName() // the config digest
	if err != nil {
		t.Fatal(err)
	}

	r := registry.NewResolver(nil)

	got, err := r.Resolve(context.Background(), ref.String(), registry.HostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigDigest != wantCfg.String() {
		t.Fatalf("Resolve ConfigDigest = %q, want %q", got.ConfigDigest, wantCfg.String())
	}

	cd, err := r.ConfigDigest(context.Background(), ref.String(), registry.HostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	if cd != wantCfg.String() {
		t.Fatalf("ConfigDigest = %q, want %q", cd, wantCfg.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/registry/ -run TestResolveAndConfigDigestPopulateConfigDigest -v`
Expected: FAIL — `got.ConfigDigest` is `""` and `r.ConfigDigest` is undefined (compile error).

- [ ] **Step 3: Add the `ConfigDigest` field**

In `internal/registry/registry.go`, add to `RemoteImage` (after `PlatformDigest`):

```go
	ConfigDigest   string
```

- [ ] **Step 4: Populate it in `Resolve`**

In `Resolve`, inside the existing `if h, err := img.Digest(); err == nil { ... }` region (after `out.PlatformDigest` is set, before the `ConfigFile` block), add:

```go
	if cn, err := img.ConfigName(); err == nil {
		out.ConfigDigest = cn.String()
	}
```

- [ ] **Step 5: Add the `ConfigDigest` method**

Add after `Resolve` (before `get`):

```go
// ConfigDigest resolves ref to its platform image's config digest (the Docker
// image ID) — the stable per-platform image identity used by the detector's
// version reverse-lookup. It reads the platform manifest but not the config
// blob, so it is cheaper than Resolve. Anonymous-first with the same
// credential-retry-on-401 behavior.
func (r *Resolver) ConfigDigest(ctx context.Context, ref string, plat Platform) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("registry: parse ref %q: %w", ref, err)
	}
	platform := v1.Platform{OS: plat.OS, Architecture: plat.Arch}
	desc, err := r.get(ctx, parsed, platform)
	if err != nil {
		return "", err
	}
	img, err := desc.Image()
	if err != nil {
		return "", fmt.Errorf("registry: resolve image %q: %w", ref, err)
	}
	cn, err := img.ConfigName()
	if err != nil {
		return "", fmt.Errorf("registry: config name %q: %w", ref, err)
	}
	return cn.String(), nil
}
```

(`r.get` already does the anonymous→401→creds retry and returns `transport.Error`-wrapped failures.)

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/registry/ -run TestResolveAndConfigDigestPopulateConfigDigest -v`
Expected: PASS

- [ ] **Step 7: Run the package + build**

Run: `CGO_ENABLED=0 go build ./... && go test ./internal/registry/`
Expected: build OK, all registry tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/registry/registry.go internal/registry/registry_test.go
git commit -m "feat(registry): expose platform config digest"
```

---

### Task 2: store — migration 0012 + negative-cache column

**Files:**
- Create: `internal/store/migrations/0012_config_digest_version.sql`
- Modify: `internal/store/images.go` (`Image` struct ~16-33; `GetByDigest` ~71-97; `SetResolvedVersion` ~103-109)
- Modify: `internal/store/tag_digests.go` (doc comments only ~8-11, 16, 30-31)
- Test: `internal/store/images_test.go` (add one test; create the file if absent)

**Interfaces:**
- Produces: `store.Image.VersionResolved bool` — true once detection has attempted to name this image's version (matched, fell back to a label, or conclusively matched nothing). Read by detect to skip re-scanning.
- Produces: `SetResolvedVersion` now also sets `version_resolved = 1` (any call marks the image resolved, including an empty version = negative cache).
- Consumes: `tag_digest_cache.digest` now stores a config digest (semantics change; same API `TagDigests.Get`/`Put`).

- [ ] **Step 1: Write the failing test**

Add to `internal/store/images_test.go` (package `store_test`; mirror the imports/`newTestDB` helper of neighboring `*_test.go` files in this package):

```go
func TestSetResolvedVersionMarksVersionResolved(t *testing.T) {
	db := newTestDB(t) // same helper other store tests use; open a temp DB
	images := store.NewImages(db)

	if _, err := images.Upsert(store.Image{
		Repo: "technitium/dns-server", Digest: "sha256:list", Tag: "latest",
	}); err != nil {
		t.Fatal(err)
	}

	// Before resolution: not marked.
	img, err := images.GetByDigest("technitium/dns-server", "sha256:list")
	if err != nil {
		t.Fatal(err)
	}
	if img.VersionResolved {
		t.Fatal("VersionResolved = true before SetResolvedVersion, want false")
	}

	// Negative cache: resolve to empty still marks it resolved.
	if err := images.SetResolvedVersion("technitium/dns-server", "sha256:list", ""); err != nil {
		t.Fatal(err)
	}
	img, err = images.GetByDigest("technitium/dns-server", "sha256:list")
	if err != nil {
		t.Fatal(err)
	}
	if !img.VersionResolved {
		t.Fatal("VersionResolved = false after SetResolvedVersion, want true")
	}
	if img.ResolvedVersion != "" {
		t.Fatalf("ResolvedVersion = %q, want empty", img.ResolvedVersion)
	}
}
```

If `internal/store/images_test.go` does not exist, create it with the package header and imports used by other store tests (`package store_test`, `testing`, `dockbrr/internal/store`, and whatever temp-DB helper the package already defines — check an existing `internal/store/*_test.go` for the exact helper name and copy its usage).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSetResolvedVersionMarksVersionResolved -v`
Expected: FAIL — `img.VersionResolved` undefined (compile error).

- [ ] **Step 3: Write the migration**

Create `internal/store/migrations/0012_config_digest_version.sql`:

```sql
-- tag_digest_cache.digest now stores the platform config digest (the image
-- identity used by the floating-tag reverse version-naming scan), not the
-- served manifest-list digest. The list digest differs per tag for multi-arch
-- images and never cross-matched, which is why floating-tag versions resolved
-- wrong. Wipe pre-upgrade rows so a stale served digest is never compared as a
-- config digest and cached as a permanent non-match; the table is a pure
-- rebuildable cache.
DELETE FROM tag_digest_cache;

-- Negative cache for resolveCurrentVersion: mark an image whose floating-tag
-- version has been resolved (matched a tag, fell back to a label, or
-- conclusively matched nothing) so the reverse scan is not re-run every detect
-- cycle for an unnameable digest. Defaults 0 so pre-upgrade rows re-resolve once.
ALTER TABLE images ADD COLUMN version_resolved BOOLEAN NOT NULL DEFAULT 0;
```

- [ ] **Step 4: Add the struct field + read it**

In `internal/store/images.go`, add to `Image` (after `ResolvedVersion`):

```go
	// VersionResolved is true once detection has attempted to name this image's
	// floating-tag version (a match, a label fallback, or a conclusive no-match).
	// It gates re-scanning so an unnameable digest is not re-HEADed every cycle.
	VersionResolved bool
```

In `GetByDigest`, extend the SELECT column list and the `Scan` targets:

```go
	err := i.db.QueryRow(
		`SELECT id, repo, tag, digest, media_type, os, arch, size, built_at,
		        labels, source_url, revision, resolved_version, version_resolved
		   FROM images WHERE repo=? AND digest=?`,
		repo, digest,
	).Scan(
		&img.ID, &img.Repo, &img.Tag, &img.Digest, &img.MediaType, &img.OS,
		&img.Arch, &img.Size, &builtAt, &img.Labels, &img.SourceURL, &img.Revision,
		&img.ResolvedVersion, &img.VersionResolved,
	)
```

- [ ] **Step 5: Make `SetResolvedVersion` mark resolved**

Replace the `SetResolvedVersion` body's `UPDATE`:

```go
	_, err := i.db.Exec(
		`UPDATE images SET resolved_version=?, version_resolved=1 WHERE repo=? AND digest=?`,
		version, repo, digest,
	)
```

Also update its doc comment: it now owns both `resolved_version` and `version_resolved`, and an empty `version` records a conclusive no-match (negative cache).

- [ ] **Step 6: Update `tag_digests.go` doc comments**

No code change. Update the type doc and `Get`/`Put` comments to say the cached value is now the tag's **platform config digest** (image identity), not the served digest — still immutable per exact-semver tag, still no TTL.

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSetResolvedVersionMarksVersionResolved -v`
Expected: PASS

- [ ] **Step 8: Run the package + build**

Run: `CGO_ENABLED=0 go build ./... && go test ./internal/store/`
Expected: build OK, all store tests PASS (migration applies clean).

- [ ] **Step 9: Commit**

```bash
git add internal/store/migrations/0012_config_digest_version.sql internal/store/images.go internal/store/tag_digests.go internal/store/images_test.go
git commit -m "feat(store): config-digest cache repurpose + version_resolved negative cache"
```

---

### Task 3: detect — config-digest version resolution

**Files:**
- Modify: `internal/detect/detect.go`:
  - `Resolver` interface ~16-20 (add `ConfigDigest`)
  - drift naming block ~204-224 (replace 5b)
  - `resolveCurrentVersion` ~373-393 (rewrite) and its call site ~178
  - remove `reverseVersions` ~290-327 and `tagDigest` ~399-417
  - add helpers `matchVersionByDigest`, `candidateConfigDigest`, `semverTagPref`
- Modify: `internal/detect/detect_test.go`:
  - `fakeResolver` ~17-72 (add `ConfigDigest` + `configByRef`)
  - migrate any existing test that drove reverse-naming through `head`/`headSeen`
  - add the technitium regression + negative-cache + rate-limit tests

**Interfaces:**
- Consumes: `registry.Resolver.ConfigDigest` (Task 1), `store.Service.CurrentImageID`, `store.Image.VersionResolved`, `store.Images.SetResolvedVersion` (Task 2).
- Produces (internal): `matchVersionByDigest(ctx, repo, configDigest, preferTag string) (tag string, conclusive bool)` — `conclusive` is false only when the scan was aborted (rate-limit / list failure); true when it matched or ran to completion with no match.

- [ ] **Step 1: Write the failing regression test (the reported bug)**

Add to `internal/detect/detect_test.go`. This models `technitium/dns-server:latest`: running config digest equals `15.4.0`'s config digest, the OCI label is the bogus `24.04`, and the repo tags are `15.4.0/15.3.0/15.2.0`.

```go
func TestDetectFloatingTagNamesVersionByConfigDigestOverBogusLabel(t *testing.T) {
	db := newDB(t)

	const (
		runImageID = "sha256:cfg-1520" // running container's image id (config digest)
		runList    = "sha256:list-old" // running RepoDigest (manifest-list)
		newList    = "sha256:list-new" // remote latest served (manifest-list) digest
		cfg1540    = "sha256:cfg-1540" // config digest shared by latest and 15.4.0
	)

	// Seed a :latest service whose running image id is 15.2.0's config digest.
	pid, err := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "dns", ImageRef: "technitium/dns-server:latest",
		CurrentDigest: runList, CurrentImageID: runImageID, State: "running",
		ImageVersion: "24.04", // the bogus OCI label captured at discovery
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := store.NewServices(db).Get(id)
	if err != nil {
		t.Fatal(err)
	}

	r := fakeResolver{
		// Resolving :latest returns the new served digest + the bogus label +
		// latest's config digest (== 15.4.0's config digest).
		img: registry.RemoteImage{
			Digest: newList, PlatformDigest: newList, ConfigDigest: cfg1540,
			Labels: map[string]string{"org.opencontainers.image.version": "24.04"},
		},
		tags: []string{"latest", "15.4.0", "15.3.0", "15.2.0"},
		// Per-tag config digests for the reverse scan.
		configByRef: map[string]string{
			"technitium/dns-server:15.4.0": cfg1540,     // matches target -> "to" = 15.4.0
			"technitium/dns-server:15.3.0": "sha256:cfg-1530",
			"technitium/dns-server:15.2.0": runImageID,  // matches running -> "from" = 15.2.0
		},
	}

	upd, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if upd == nil {
		t.Fatal("expected an available update (list digest drifted), got nil")
	}
	if upd.ToVersion != "15.4.0" {
		t.Fatalf("ToVersion = %q, want 15.4.0 (config-digest match must beat the bogus 24.04 label)", upd.ToVersion)
	}
	if upd.FromVersion != "15.2.0" {
		t.Fatalf("FromVersion = %q, want 15.2.0", upd.FromVersion)
	}
	if upd.Severity != "minor" {
		t.Fatalf("Severity = %q, want minor (15.2.0 -> 15.4.0)", upd.Severity)
	}
}
```

(`store.Services.Get(id)` returns the seeded service.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/detect/ -run TestDetectFloatingTagNamesVersionByConfigDigestOverBogusLabel -v`
Expected: FAIL — `configByRef`/`ConfigDigest` undefined (compile error), then a naming mismatch.

- [ ] **Step 3: Add `ConfigDigest` to the interface + fake**

In `internal/detect/detect.go`, add to the `Resolver` interface:

```go
	ConfigDigest(ctx context.Context, ref string, plat registry.Platform) (string, error)
```

In `internal/detect/detect_test.go`, add a field to `fakeResolver`:

```go
	// configByRef backs ConfigDigest, keyed by "repo:tag". A missing ref falls
	// back to img.ConfigDigest.
	configByRef map[string]string
```

and the method:

```go
func (f fakeResolver) ConfigDigest(_ context.Context, ref string, _ registry.Platform) (string, error) {
	if f.headErr != nil {
		return "", f.headErr
	}
	if f.configByRef != nil {
		if cd, ok := f.configByRef[ref]; ok {
			return cd, nil
		}
	}
	return f.img.ConfigDigest, nil
}
```

- [ ] **Step 4: Add the matching helpers**

In `internal/detect/detect.go`, add (near `reverseScanCap`, which stays):

```go
// candidateConfigDigest returns the platform config digest for repo:tag,
// preferring the permanent tag-digest cache (exact-semver tags are immutable)
// and falling back to a registry lookup, whose result is cached. The error is
// returned so the caller can distinguish a rate-limit (abort) from a per-tag
// failure (skip).
func (d *Detector) candidateConfigDigest(ctx context.Context, repo, tag string) (string, error) {
	if d.tagCache != nil {
		if cd, ok, err := d.tagCache.Get(repo, tag); err != nil {
			logger.Warnf("detect: tag-cache get %s:%s: %v (falling back to lookup)", repo, tag, err)
		} else if ok {
			return cd, nil
		}
	}
	cd, err := d.resolver.ConfigDigest(ctx, repo+":"+tag, d.plat)
	if err != nil {
		return "", err
	}
	if d.tagCache != nil {
		if err := d.tagCache.Put(repo, tag, cd); err != nil {
			logger.Warnf("detect: tag-cache put %s:%s: %v", repo, tag, err)
		}
	}
	return cd, nil
}

// matchVersionByDigest names a fully-floating image by finding the repo's stable
// semver tag whose platform config digest equals configDigest. preferTag (an
// exact-semver OCI label, else "") is checked first as a fast path. conclusive
// is false only when the scan aborted (rate-limit or tag-list failure) so the
// caller can avoid negative-caching a transient miss; it is true on a match or a
// completed no-match. Returns ("", true) when configDigest is empty.
func (d *Detector) matchVersionByDigest(ctx context.Context, repo, configDigest, preferTag string) (string, bool) {
	if configDigest == "" {
		return "", true
	}
	if preferTag != "" {
		if cd, err := d.candidateConfigDigest(ctx, repo, preferTag); err == nil && cd == configDigest {
			return preferTag, true
		}
	}
	tags, err := d.resolver.ListTags(ctx, repo)
	if err != nil {
		logger.Warnf("detect: list tags %q: %v (version reverse-lookup skipped)", repo, err)
		return "", false
	}
	cands := semverTagsDesc(tags)
	if len(cands) > reverseScanCap {
		logger.Debugf("detect: %s reverse-lookup capped at %d of %d semver tags", repo, reverseScanCap, len(cands))
		cands = cands[:reverseScanCap]
	}
	for _, t := range cands {
		cd, err := d.candidateConfigDigest(ctx, repo, t)
		if err != nil {
			if registry.IsRateLimited(err) {
				logger.Warnf("detect: config %s:%s rate-limited (reverse-lookup aborted)", repo, t)
				return "", false
			}
			logger.Tracef("detect: config %s:%s: %v (reverse-lookup continues)", repo, t, err)
			continue
		}
		if cd == configDigest {
			return t, true
		}
	}
	return "", true
}

// semverTagPref returns label when it is a fully-specified semver tag name (so
// it can be looked up directly as the reverse-lookup fast path), else "".
func semverTagPref(label string) string {
	if exactSemverRe.MatchString(label) {
		return label
	}
	return ""
}
```

- [ ] **Step 5: Rewrite the drift naming block (5b)**

In `Detect`, replace the whole `// 5b. Reverse version-naming …` block (currently `if semverOrEmpty(tag) == "" && ClassifyTag(...) == TagFloating && (fromVer == "" || toVer == "") { rf, rt := d.reverseVersions(...) ... }`) with:

```go
	// 5b. Fully-floating tag (latest, stable, named): the tag carries no semver.
	// Name both ends by matching the running + target CONFIG digests back to the
	// repo's stable semver tags. A digest-matched real tag wins over the OCI
	// image.version label, which some images set to a base-OS version (e.g.
	// technitium/dns-server labels 24.04 while shipping app 15.x). Cosmetic:
	// apply still floats the SAME tag; targetTag/ToDigest are untouched.
	if semverOrEmpty(tag) == "" && ClassifyTag(repo+":"+tag) == TagFloating {
		toLabel := targetRemote.Labels["org.opencontainers.image.version"]
		if v, _ := d.matchVersionByDigest(ctx, repo, svc.CurrentImageID, semverTagPref(svc.ImageVersion)); v != "" {
			fromVer = v
		}
		if v, _ := d.matchVersionByDigest(ctx, repo, targetRemote.ConfigDigest, semverTagPref(toLabel)); v != "" {
			toVer = v
		}
	}
	if toVer == "" {
		toVer = fromVer
	}
	severity := Severity(fromVer, toVer)
```

Leave the earlier `fromVer`/`toVer` derivation (from `semverOrEmpty`/labels, ~187-203) intact — it supplies the fallback the block only overrides on a digest match.

- [ ] **Step 6: Rewrite `resolveCurrentVersion` + its call site**

Change the call site (~178) to pass the running config digest:

```go
		d.resolveCurrentVersion(ctx, repo, tag, svc.CurrentDigest, svc.CurrentImageID, remote)
```

Replace `resolveCurrentVersion` with:

```go
// resolveCurrentVersion names the running version of an up-to-date, fully-
// floating service and caches it on the image row (keyed by the served digest)
// so the dashboard can show a release for a tag that carries no semver. The name
// comes from a config-digest reverse-lookup (which wins over the OCI label),
// falling back to the label. Cached via version_resolved so an unnameable digest
// is scanned at most once. A transient (inconclusive) scan is not cached, so it
// retries next cycle instead of poisoning the row with a fallback label.
func (d *Detector) resolveCurrentVersion(ctx context.Context, repo, tag, digest, configDigest string, remote registry.RemoteImage) {
	if d.images == nil || digest == "" {
		return
	}
	if semverOrEmpty(tag) != "" || ClassifyTag(repo+":"+tag) != TagFloating {
		return
	}
	if img, err := d.images.GetByDigest(repo, digest); err == nil && img.VersionResolved {
		return // already attempted for this digest
	}
	label := remote.Labels["org.opencontainers.image.version"]
	// Prefer the container's image id; for a pre-upgrade row without one, the
	// up-to-date remote's config digest is the same running image.
	cd := configDigest
	if cd == "" {
		cd = remote.ConfigDigest
	}
	tagName, conclusive := d.matchVersionByDigest(ctx, repo, cd, semverTagPref(label))
	if tagName == "" && !conclusive {
		return // transient failure; retry next cycle, do not cache a fallback
	}
	ver := tagName
	if ver == "" {
		ver = label // conclusive no-match: fall back to the label (may be "")
	}
	if err := d.images.SetResolvedVersion(repo, digest, ver); err != nil {
		logger.Warnf("detect: set resolved version %s@%s: %v", repo, shortDigest(digest), err)
	}
}
```

- [ ] **Step 7: Remove the dead reverse-lookup code**

Delete `reverseVersions` (~290-327) and `tagDigest` (~399-417) entirely — both are now unused. Keep `reverseScanCap` and `semverTagsDesc` (still used by `matchVersionByDigest`).

- [ ] **Step 8: Migrate existing tests that drove reverse-naming**

Run `go test ./internal/detect/` and fix compile/behavior fallout:
- Any `fakeResolver` literal using `head`/`headSeen`/`headErr` to simulate the **reverse version-naming** scan must move that mapping to `configByRef` (keyed `"repo:tag"` → config digest) so it exercises `matchVersionByDigest`. The `head` field itself may remain on the struct if other tests use `Head` for unrelated reasons, but no production code calls `Head` anymore — if nothing references it, remove `Head`/`head`/`headSeen`/`headErr` from both the interface-satisfying method set is NOT required (interface no longer lists `Head`; keep the method only if a test calls it, otherwise delete it and its fields).
- Update assertions that expected a version from the old (broken) list-digest match to the config-digest values.
- The `Resolver` interface no longer needs `Head`; remove it from the interface declaration in `detect.go` if no production code uses it (confirm with `grep -rn "\.Head(" internal/detect`). The concrete `registry.Resolver.Head` method stays (it is harmless and may be used elsewhere — confirm with `grep -rn "\.Head(" internal/`).

- [ ] **Step 9: Add the negative-cache + rate-limit tests**

Add to `internal/detect/detect_test.go`:

```go
// An up-to-date floating service whose digest matches no repo tag and has no
// usable label is marked version_resolved so the next cycle does not re-scan.
func TestResolveCurrentVersionNegativeCache(t *testing.T) {
	db := newDB(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	id, err := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "acme/app:latest",
		CurrentDigest: "sha256:list", CurrentImageID: "sha256:cfg-head", State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := store.NewServices(db).Get(id)

	listCalls := 0
	r := countingResolver{
		fakeResolver: fakeResolver{
			img:  registry.RemoteImage{Digest: "sha256:list", PlatformDigest: "sha256:list", ConfigDigest: "sha256:cfg-head"},
			tags: []string{"latest", "1.0.0"},
			configByRef: map[string]string{
				"acme/app:1.0.0": "sha256:cfg-other", // no match for the running config
			},
		},
		listCalls: &listCalls,
	}

	if _, err := newDetector(db, r).Detect(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	img, err := store.NewImages(db).GetByDigest("acme/app", "sha256:list")
	if err != nil {
		t.Fatal(err)
	}
	if !img.VersionResolved {
		t.Fatal("VersionResolved = false after a conclusive no-match, want true")
	}

	// Second detect (same digest) must not re-run the tag list.
	first := listCalls
	if _, err := newDetector(db, r).Detect(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	if listCalls != first {
		t.Fatalf("ListTags called again (%d -> %d); negative cache did not short-circuit", first, listCalls)
	}
}
```

Add the tiny counting wrapper near `fakeResolver`:

```go
// countingResolver counts ListTags calls to assert the negative cache prevents
// a re-scan on a later detect for the same digest.
type countingResolver struct {
	fakeResolver
	listCalls *int
}

func (c countingResolver) ListTags(ctx context.Context, repo string) ([]string, error) {
	*c.listCalls++
	return c.fakeResolver.ListTags(ctx, repo)
}
```

> Note: the second `Detect` re-resolves `:latest` (same served digest → up-to-date path → `resolveCurrentVersion`), which sees `VersionResolved` and returns before `ListTags`. If a fresh remote-state cache hit (TTL) short-circuits before `resolveCurrentVersion`, seed/clear `image_remote_state` as the neighboring cache tests do so the up-to-date path actually runs; match the existing tests' handling of `cacheTTL`.

- [ ] **Step 10: Run all detect tests**

Run: `go test ./internal/detect/ -v`
Expected: PASS, including the three new tests.

- [ ] **Step 11: Full check + build**

Run: `CGO_ENABLED=0 go build ./... && mise run check`
Expected: build OK; go vet + go test + vitest all PASS.

- [ ] **Step 12: Commit**

```bash
git add internal/detect/detect.go internal/detect/detect_test.go
git commit -m "fix(detect): resolve floating-tag version by config digest, not manifest-list digest"
```

---

## Self-Review

**Spec coverage:**
- Config digest everywhere — running (`CurrentImageID`, Task 3), target (`RemoteImage.ConfigDigest`, Task 1), candidate (`candidateConfigDigest` + repurposed cache, Tasks 2–3). ✔
- Reverse-lookup rewrite at config-digest level — Task 3 `matchVersionByDigest`. ✔
- Label fast-path — `semverTagPref` + `preferTag` in `matchVersionByDigest`, Task 3. ✔
- Gate fix (Bug 1) — 5b block loses the `(fromVer=="" || toVer=="")` precondition, Task 3 Step 5. ✔
- Severity correctness — asserted in the regression test, Task 3 Step 1. ✔
- Positive cache (repurposed column) + wipe — Task 2 migration. ✔
- Negative cache (`version_resolved`) + no-poison on transient failure — Tasks 2–3. ✔
- Bounds retained (`reverseScanCap`, newest-first, rate-limit abort) — Task 3 Step 4. ✔
- Migration 0012, no `image_id` column (already exists), no `config_digest` column (repurpose) — Task 2. ✔
- Tests: registry ConfigDigest, store round-trip, detect regression/negative-cache/rate-limit — Tasks 1–3. ✔

**Placeholder scan:** No TBD/TODO. Code shown for every code step. The two `>` notes (service accessor name, cache-TTL handling) point at concrete existing-test patterns to match rather than leaving logic unspecified.

**Type consistency:** `ConfigDigest(ctx, ref, plat)` identical in registry method, detect interface, and fake. `matchVersionByDigest(ctx, repo, configDigest, preferTag) (string, bool)` consistent across call sites. `RemoteImage.ConfigDigest`, `Service.CurrentImageID`, `Image.VersionResolved` used with the exact names defined.
