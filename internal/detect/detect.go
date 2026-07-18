package detect

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// Resolver is the registry-resolution dependency the detector needs. The
// concrete *registry.Resolver satisfies it; tests pass a fake.
type Resolver interface {
	Resolve(ctx context.Context, ref string, plat registry.Platform) (registry.RemoteImage, error)
	ListTags(ctx context.Context, repo string) ([]string, error)
	Head(ctx context.Context, ref string) (string, error)
}

// Detector compares a service's running digest against remote registry state
// and records an updates row on drift. It never mutates Docker, and a registry
// failure is recorded (image_remote_state) rather than returned.
type Detector struct {
	resolver Resolver
	updates  *store.Updates
	images   *store.Images
	states   *store.RemoteStates
	events   *store.Events
	tagCache *store.TagDigests
	plat     registry.Platform
	cacheTTL func() time.Duration
}

// NewDetector wires the detector. cacheTTL is consulted on every Detect call
// (not just at construction) so a live settings change bounding reuse of a
// prior ok remote resolution takes effect without a restart.
func NewDetector(
	resolver Resolver,
	updates *store.Updates,
	images *store.Images,
	states *store.RemoteStates,
	events *store.Events,
	tagCache *store.TagDigests,
	plat registry.Platform,
	cacheTTL func() time.Duration,
) *Detector {
	if cacheTTL == nil {
		cacheTTL = func() time.Duration { return 10 * time.Minute }
	}
	return &Detector{
		resolver: resolver, updates: updates, images: images,
		states: states, events: events, tagCache: tagCache,
		plat: plat, cacheTTL: cacheTTL,
	}
}

// Detect resolves the remote digest for svc's tracked tag and, on drift,
// records and returns an available update. Returns (nil, nil) when the service
// is unmonitorable, already up to date, or the registry resolution failed
// (the failure is recorded in image_remote_state, never fatal).
func (d *Detector) Detect(ctx context.Context, svc store.Service) (*store.Update, error) {
	if svc.ImageRef == "" || svc.CurrentDigest == "" {
		return nil, nil // unmonitorable: nothing to compare
	}
	repo, tag := SplitRef(svc.ImageRef)
	now := time.Now().UTC()

	// 1. Try a fresh cache hit (digest only, no labels). A cache hit
	// intentionally skips the semver tag scan below (digest-only path) until
	// the cache TTL expires.
	if remoteDigest, ok := d.freshCachedDigest(repo, tag, now); ok {
		logger.Tracef("detect: %s cache hit digest %s", svc.ImageRef, shortDigest(remoteDigest))
		if remoteDigest == svc.CurrentDigest {
			// Only close updates whose target the service now RUNS. This path
			// skipped the semver tag scan, so an open update targeting a
			// different (newer) tag may still be perfectly current; supersede
			// -all here would flap it closed on every cache-window re-check
			// (e.g. a scan-all right after a manual per-service check) until
			// the TTL expires and the full resolve re-opens it.
			d.closeReachedUpdates(svc.ID, remoteDigest)
			return nil, nil
		}
		return d.record(svc, tag, remoteDigest, "", "", "digest-only")
	}

	// 2. Resolve from the network.
	logger.Tracef("detect: %s resolving from registry", svc.ImageRef)
	remote, err := d.resolver.Resolve(ctx, svc.ImageRef, d.plat)
	if err != nil {
		// Classify: a 404 means the repo/tag does not exist, and a 401 is how
		// public registries answer an anonymous request for a nonexistent repo
		// (Docker Hub hides existence behind Unauthorized). Both are the
		// expected steady state for a locally built image, or a private image
		// with no credentials configured, so they get their own status (the
		// dashboard explains it) at debug log level instead of an error spammed
		// on every scan. Anything else (network, 5xx) stays a hard error.
		status := "error"
		switch {
		case registry.IsRateLimited(err):
			status = "rate_limited"
		case registry.IsUnauthorized(err) || registry.IsNotFound(err):
			status = "not_found"
		}
		if status == "not_found" {
			logger.Debugf("detect: resolve %q: %v (status=not_found)", svc.ImageRef, err)
		} else {
			logger.Errorf("detect: resolve %q: %v (status=%s)", svc.ImageRef, err, status)
		}
		_ = d.states.Upsert(store.RemoteState{Repo: repo, Tag: tag, Status: status, ResolvedAt: &now})
		return nil, nil // non-fatal
	}

	// 3. Cache the ok resolution + record the observed image (best effort).
	// Normalize nil labels to an empty map so json.Marshal emits {} not null.
	if remote.Labels == nil {
		remote.Labels = map[string]string{}
	}
	labelsJSON, _ := json.Marshal(remote.Labels)
	labelsStr := string(labelsJSON)
	_ = d.states.Upsert(store.RemoteState{
		Repo: repo, Tag: tag, RemoteDigest: remote.Digest,
		ManifestLabels: labelsStr, Status: "ok", ResolvedAt: &now,
	})
	d.recordImage(repo, tag, remote, labelsStr)
	logger.Debugf("detect: %s resolved digest %s (current %s)", svc.ImageRef, shortDigest(remote.Digest), shortDigest(svc.CurrentDigest))

	// 3b. Semver tag scan (design §5.4): when the tracked tag is an EXACT
	// (full-semver) pin, look for a strictly newer stable tag and, if found,
	// target THAT tag's digest. This must NOT run for floating tags (latest,
	// named, or partial semver like "1" / "1.31"), since apply floats a floating
	// tag's stream via a plain pull+up, it never moves the container to an
	// out-of-stream tag, so suggesting one here would record an update that
	// apply can never actually deliver (see ClassifyTag). Failure here is
	// non-fatal; the same-tag digest compare below still runs.
	targetTag := tag
	targetRemote := remote
	if ClassifyTag(repo+":"+tag) == TagExact {
		if tags, terr := d.resolver.ListTags(ctx, repo); terr != nil {
			logger.Warnf("detect: list tags %q: %v (semver scan skipped)", repo, terr)
		} else if newer, ok := NewerSemverTag(tag, tags); ok {
			logger.Debugf("detect: %s newer semver tag %s", repo+":"+tag, newer)
			if nr, rerr := d.resolver.Resolve(ctx, repo+":"+newer, d.plat); rerr != nil {
				logger.Warnf("detect: resolve %s:%s: %v (semver scan skipped)", repo, newer, rerr)
			} else {
				targetTag, targetRemote = newer, nr
			}
		}
	}
	if targetTag != tag {
		// Normalize + persist the target image's labels too (the changelog
		// resolver reads labels for upd.ToDigest, see scan.CheckService).
		if targetRemote.Labels == nil {
			targetRemote.Labels = map[string]string{}
		}
		targetLabelsJSON, _ := json.Marshal(targetRemote.Labels)
		d.recordImage(repo, targetTag, targetRemote, string(targetLabelsJSON))
	}

	// 4. Compare (match on either the served or platform digest).
	if targetRemote.Digest == svc.CurrentDigest || targetRemote.PlatformDigest == svc.CurrentDigest {
		// Up to date. A floating tag (latest) with no version label would show
		// only ":latest" on the dashboard, since version enrichment otherwise
		// lives only on the update path below. Reverse-resolve the running
		// digest to a release tag once (cached on the image row, keyed by
		// digest) so the list can surface "v1.13.2" for a pending-nothing
		// service. Best-effort: any failure just leaves the version blank.
		d.closeStaleUpdates(svc.ID)
		d.resolveCurrentVersion(ctx, repo, tag, svc.CurrentDigest, remote)
		return nil, nil
	}

	// 5. Classify severity from versions. When the semver scan actually moved
	// to a different tag, that tag's own version string is authoritative.
	// Otherwise (same tracked tag), prefer the OCI version label as before:
	// it can be more precise than the tag itself (e.g. a floating tag whose
	// label carries the exact release).
	fromVer := semverOrEmpty(tag)
	// A floating tag (latest, 1, stable) carries no release in its name, so name
	// the running side from the image's own OCI version label, captured at
	// discovery. This mirrors how the "to" side falls back to the target's label
	// below, and is why e.g. linuxserver's "1.6.0-lsNNN" images (whose versioned
	// tags the reverse scan skips) now show a "from" version. Empty when the
	// image ships no version label; the reverse-lookup below then fills the gap.
	if fromVer == "" {
		fromVer = svc.ImageVersion
	}
	var toVer string
	if targetTag != tag {
		toVer = semverOrEmpty(targetTag)
	}
	if toVer == "" {
		toVer = targetRemote.Labels["org.opencontainers.image.version"]
	}
	// 5b. Reverse version-naming for a fully-floating tag (latest, stable,
	// named): the tag carries no semver and many images ship no version label
	// (e.g. backrest sets only image.source), so both ends read blank. Name them
	// by matching the running + target digests back to the repo's stable semver
	// tags. This is COSMETIC: apply still floats the SAME tag, so targetTag and
	// ToDigest are untouched (we never suggest moving latest -> v1.14.1).
	// Partial-semver floating tags (1, 1.31) are excluded (semverOrEmpty(tag) is
	// non-empty): their stream name is not a release name. Best-effort + bounded:
	// a list/head failure or rate-limit leaves the version blank.
	if semverOrEmpty(tag) == "" && ClassifyTag(repo+":"+tag) == TagFloating && (fromVer == "" || toVer == "") {
		rf, rt := d.reverseVersions(ctx, repo, svc.CurrentDigest, targetRemote)
		if fromVer == "" {
			fromVer = rf
		}
		if toVer == "" {
			toVer = rt
		}
	}
	if toVer == "" {
		toVer = fromVer
	}
	severity := Severity(fromVer, toVer)

	return d.record(svc, targetTag, targetRemote.Digest, fromVer, toVer, severity)
}

// record persists the drift (transactionally, via RecordDrift) and emits a
// `detected` event ONLY when the update row is newly created. A repeated
// detection of the same digest is idempotent and emits no duplicate event.
func (d *Detector) record(svc store.Service, tag, toDigest, fromVer, toVer, severity string) (*store.Update, error) {
	up := store.Update{
		ServiceID:   svc.ID,
		FromDigest:  svc.CurrentDigest,
		ToDigest:    toDigest,
		FromVersion: fromVer,
		ToVersion:   toVer,
		Tag:         tag,
		Severity:    severity,
		Status:      "available",
	}
	id, isNew, err := d.updates.RecordDrift(up)
	if err != nil {
		return nil, err
	}
	up.ID = id
	if isNew {
		if _, err := d.events.Insert(store.Event{
			ServiceID:  svc.ID,
			Kind:       "detected",
			FromDigest: svc.CurrentDigest,
			ToDigest:   toDigest,
			Message:    "update available",
		}); err != nil {
			return nil, err
		}
	}
	return &up, nil
}

// recordImage best-effort persists the observed remote image identity for the
// changelog phase to reuse. labelsJSON is the pre-marshaled labels string
// (caller normalizes nil→{} and marshals once). Errors are logged, never returned.
func (d *Detector) recordImage(repo, tag string, remote registry.RemoteImage, labelsJSON string) {
	var builtAt *time.Time
	if !remote.BuiltAt.IsZero() {
		t := remote.BuiltAt
		builtAt = &t
	}
	if _, err := d.images.Upsert(store.Image{
		Repo: repo, Tag: tag, Digest: remote.Digest, MediaType: remote.MediaType,
		OS: remote.OS, Arch: remote.Architecture, BuiltAt: builtAt,
		Labels:    labelsJSON,
		SourceURL: remote.Labels["org.opencontainers.image.source"],
		Revision:  remote.Labels["org.opencontainers.image.revision"],
	}); err != nil {
		logger.Errorf("detect: record image %s@%s: %v", repo, remote.Digest, err)
	}
}

// reverseScanCap bounds how many stable semver tags the floating-tag reverse
// version-naming scan will HEAD before giving up. A floating tag's running and
// target images sit near the head of the release list in practice, so a modest
// cap names them while keeping registry traffic (and rate-limit exposure)
// bounded on large repos (e.g. 300+ tags).
const reverseScanCap = 50

// reverseVersions best-effort names the from (running) and to (target) digests
// of a fully-floating tag by HEAD-matching them against the repo's stable semver
// tags, newest-first. It stops once both ends are named, the scan cap is hit, or
// the registry rate-limits. Returns ("", "") on a tag-list failure. It uses Head
// (digest only) rather than Resolve to avoid pulling a config blob per tag.
func (d *Detector) reverseVersions(ctx context.Context, repo, fromDigest string, target registry.RemoteImage) (fromVer, toVer string) {
	tags, err := d.resolver.ListTags(ctx, repo)
	if err != nil {
		logger.Warnf("detect: list tags %q: %v (version reverse-lookup skipped)", repo, err)
		return "", ""
	}
	cands := semverTagsDesc(tags)
	if len(cands) > reverseScanCap {
		logger.Debugf("detect: %s reverse-lookup capped at %d of %d semver tags", repo, reverseScanCap, len(cands))
		cands = cands[:reverseScanCap]
	}
	for _, t := range cands {
		if fromVer != "" && toVer != "" {
			break
		}
		dg, err := d.tagDigest(ctx, repo, t)
		if err != nil {
			if registry.IsRateLimited(err) {
				logger.Warnf("detect: head %s:%s rate-limited (reverse-lookup aborted)", repo, t)
				break
			}
			logger.Tracef("detect: head %s:%s: %v (reverse-lookup continues)", repo, t, err)
			continue
		}
		if toVer == "" && (dg == target.Digest || dg == target.PlatformDigest) {
			toVer = t
		}
		if fromVer == "" && dg == fromDigest {
			fromVer = t
		}
	}
	return fromVer, toVer
}

// closeStaleUpdates supersedes any still-available update for an up-to-date
// service. The running image already matches the tracked tag's remote, so no
// prior drift is actionable; a lingering row would keep the dashboard showing
// the old target as available and label the running image with its stale
// from_version. This fires when a container reached its target outside the
// apply path, e.g. dockbrr updating its own container (the recreate kills the
// process before MarkApplied runs) or an image updated outside dockbrr.
// Only called from the FULL-resolve path, where the semver scan has run and
// "up to date" is authoritative for every candidate target. Best-effort: a
// failure is logged, never fatal.
func (d *Detector) closeStaleUpdates(serviceID int64) {
	if d.updates == nil {
		return
	}
	if n, err := d.updates.SupersedeAllOpen(serviceID); err != nil {
		logger.Warnf("detect: supersede stale updates for service %d: %v", serviceID, err)
	} else if n > 0 {
		logger.Debugf("detect: service %d up to date, superseded %d stale update(s)", serviceID, n)
	}
}

// closeReachedUpdates is closeStaleUpdates' narrow sibling for the digest-only
// cache-hit path: it supersedes only updates whose target digest the service
// now runs, leaving different-tag (semver) targets open since that path cannot
// judge them. Best-effort: a failure is logged, never fatal.
func (d *Detector) closeReachedUpdates(serviceID int64, reachedDigest string) {
	if d.updates == nil {
		return
	}
	if n, err := d.updates.SupersedeOpenAtDigest(serviceID, reachedDigest); err != nil {
		logger.Warnf("detect: supersede reached updates for service %d: %v", serviceID, err)
	} else if n > 0 {
		logger.Debugf("detect: service %d reached %s, superseded %d update(s)", serviceID, shortDigest(reachedDigest), n)
	}
}

// resolveCurrentVersion names the running version of an up-to-date service and
// caches it on the image row (keyed by digest) so the dashboard can show a
// release for a floating tag that has no pending update. It only acts for a
// fully-floating tag (latest, stable, named) whose tag carries no semver: an
// exact or partial-semver tag already reads as its own version. The name comes
// from the OCI version label when present, else a digest reverse-lookup. Cached
// per digest: once resolved, later scans short-circuit before any network call.
// Best-effort throughout; any miss leaves the version blank.
func (d *Detector) resolveCurrentVersion(ctx context.Context, repo, tag, digest string, remote registry.RemoteImage) {
	if d.images == nil || digest == "" {
		return
	}
	if semverOrEmpty(tag) != "" || ClassifyTag(repo+":"+tag) != TagFloating {
		return
	}
	if img, err := d.images.GetByDigest(repo, digest); err == nil && img.ResolvedVersion != "" {
		return // already resolved for this digest
	}
	ver := remote.Labels["org.opencontainers.image.version"]
	if ver == "" {
		ver, _ = d.reverseVersions(ctx, repo, digest, remote)
	}
	if ver == "" {
		return
	}
	if err := d.images.SetResolvedVersion(repo, digest, ver); err != nil {
		logger.Warnf("detect: set resolved version %s@%s: %v", repo, shortDigest(digest), err)
	}
}

// tagDigest returns the served digest for repo:tag, preferring the permanent
// tag-digest cache (exact-semver tags are immutable) and falling back to a
// registry HEAD, whose result is then cached. A HEAD error is returned so the
// caller can distinguish a rate-limit (abort) from a per-tag failure (skip).
func (d *Detector) tagDigest(ctx context.Context, repo, tag string) (string, error) {
	if d.tagCache != nil {
		if dg, ok, err := d.tagCache.Get(repo, tag); err != nil {
			logger.Warnf("detect: tag-cache get %s:%s: %v (falling back to head)", repo, tag, err)
		} else if ok {
			return dg, nil
		}
	}
	dg, err := d.resolver.Head(ctx, repo+":"+tag)
	if err != nil {
		return "", err
	}
	if d.tagCache != nil {
		if err := d.tagCache.Put(repo, tag, dg); err != nil {
			logger.Warnf("detect: tag-cache put %s:%s: %v", repo, tag, err)
		}
	}
	return dg, nil
}

// freshCachedDigest returns the cached remote digest for (repo, tag) when the
// last resolution was ok and within cacheTTL. An empty cached digest is
// rejected (falls through to the network) to prevent recording an update with
// an empty to_digest.
func (d *Detector) freshCachedDigest(repo, tag string, now time.Time) (string, bool) {
	st, err := d.states.Get(repo, tag)
	if err != nil || st.Status != "ok" || st.ResolvedAt == nil || st.RemoteDigest == "" {
		return "", false
	}
	if now.Sub(*st.ResolvedAt) > d.cacheTTL() {
		return "", false
	}
	return st.RemoteDigest, true
}

// SplitRef splits an image reference into its repo and tag. A digest-only or
// untagged reference yields tag "latest".
// shortDigest truncates a "sha256:<hex>" digest to a log-friendly prefix.
func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

func SplitRef(ref string) (repo, tag string) {
	// Drop any @digest first.
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	// A tag is the last ":"-segment that contains no "/".
	if colon := strings.LastIndex(ref, ":"); colon >= 0 && !strings.Contains(ref[colon+1:], "/") {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}

// semverOrEmpty returns v when it parses as a semver core, else "".
func semverOrEmpty(v string) string {
	if _, ok := parseCore(v); ok {
		return v
	}
	return ""
}
