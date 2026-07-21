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
	ConfigDigest(ctx context.Context, ref string, plat registry.Platform) (string, error)
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

	// A locally built image (compose build: directive) has no registry to check.
	// Record it as such and skip all network resolution: the periodic scan never
	// probes it, and the dashboard reads it as intentional rather than an error.
	if svc.ImageLocal {
		_ = d.states.Upsert(store.RemoteState{Repo: repo, Tag: tag, Status: "local", ResolvedAt: &now})
		return nil, nil
	}

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
		d.resolveCurrentVersion(ctx, repo, tag, svc.CurrentDigest, svc.CurrentImageID, remote)
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
	// 5b. Fully-floating tag (latest, stable, named): the tag carries no semver.
	// Name both ends by matching the running + target CONFIG digests back to the
	// repo's stable semver tags. A digest-matched real tag wins over the OCI
	// image.version label, which some images set to a base-OS version (e.g.
	// technitium/dns-server labels 24.04 while shipping app 15.x). Cosmetic:
	// apply still floats the SAME tag; targetTag/ToDigest are untouched.
	if semverOrEmpty(tag) == "" && ClassifyTag(repo+":"+tag) == TagFloating {
		toLabel := targetRemote.Labels["org.opencontainers.image.version"]
		if v, _ := d.matchVersionByDigest(ctx, repo, svc.CurrentImageID, semverTagPref(svc.ImageVersion)); v != "" && preferDigestTag(v, fromVer) {
			fromVer = v
		}
		if v, _ := d.matchVersionByDigest(ctx, repo, targetRemote.ConfigDigest, semverTagPref(toLabel)); v != "" && preferDigestTag(v, toVer) {
			toVer = v
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

// preferDigestTag reports whether a bare, digest-reverse-matched tag should
// replace the current OCI-label-derived version string. It replaces the label
// only when the label does not already name the same version: an unparseable
// label (a base-OS value, a rolling word, or empty) yields the tag; a label whose
// lenient core differs from the tag's is wrong for this image and yields the tag;
// a label whose core matches the tag is the same version but potentially more
// precise (carries "-lsNNN"), so it is kept.
func preferDigestTag(digestTag, label string) bool {
	lc, lok := parseCore(StripNamePrefix(label))
	if !lok {
		return true // label not a version: use the digest-matched tag
	}
	dc, _ := parseCore(digestTag) // digestTag is always bare semver
	return lc != dc               // differ: correct the label; equal: keep it
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
	if ver == "" || !preferDigestTag(tagName, label) {
		ver = label // no match, or the label already names this version (keep it)
	}
	if err := d.images.SetResolvedVersion(repo, digest, ver); err != nil {
		logger.Warnf("detect: set resolved version %s@%s: %v", repo, shortDigest(digest), err)
	}
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
