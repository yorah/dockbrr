package detect_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"dockbrr/internal/detect"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

type fakeResolver struct {
	img registry.RemoteImage
	err error

	// tags/tagsErr back ListTags, consulted by the detector's semver scan.
	tags    []string
	tagsErr error

	// byRef, when non-nil, overrides img on a per-ref basis (needed to
	// simulate a cross-tag resolve returning a different digest than the
	// tracked tag's same-tag resolve).
	byRef map[string]registry.RemoteImage

	// head/headErr back Head, consulted by the floating-tag reverse
	// version-naming scan. A missing ref falls back to img.Digest.
	head    map[string]string
	headErr error

	// headSeen, when non-nil, counts Head calls per ref (maps are reference
	// types, so a value-receiver method still mutates the shared map).
	headSeen map[string]int
}

func (f fakeResolver) Resolve(_ context.Context, ref string, _ registry.Platform) (registry.RemoteImage, error) {
	if f.err != nil {
		return registry.RemoteImage{}, f.err
	}
	if f.byRef != nil {
		if img, ok := f.byRef[ref]; ok {
			img.Ref = ref
			return img, nil
		}
	}
	out := f.img
	out.Ref = ref
	return out, nil
}

func (f fakeResolver) ListTags(_ context.Context, _ string) ([]string, error) {
	return f.tags, f.tagsErr
}

func (f fakeResolver) Head(_ context.Context, ref string) (string, error) {
	if f.headSeen != nil {
		f.headSeen[ref]++
	}
	if f.headErr != nil {
		return "", f.headErr
	}
	if f.head != nil {
		if d, ok := f.head[ref]; ok {
			return d, nil
		}
	}
	return f.img.Digest, nil
}

func newDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedSvc(t *testing.T, db *store.DB, ref, digest string, pinned bool) store.Service {
	t.Helper()
	pid, err := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: ref, CurrentDigest: digest,
		Pinned: pinned, State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store.Service{ID: id, ProjectID: pid, Name: "app", ImageRef: ref, CurrentDigest: digest, Pinned: pinned}
}

func newDetector(db *store.DB, r detect.Resolver) *detect.Detector {
	return detect.NewDetector(
		r, store.NewUpdates(db), store.NewImages(db),
		store.NewRemoteStates(db), store.NewEvents(db), store.NewTagDigests(db),
		registry.HostPlatform(), func() time.Duration { return time.Minute },
	)
}

func TestDetectProducesUpdateOnDigestDrift(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:old", false)
	r := fakeResolver{img: registry.RemoteImage{
		Digest: "sha256:new", PlatformDigest: "sha256:new",
		Labels: map[string]string{"org.opencontainers.image.version": "1.2.4"},
	}}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected an update, got nil")
	}
	if u.FromDigest != "sha256:old" || u.ToDigest != "sha256:new" {
		t.Fatalf("digests = %s -> %s", u.FromDigest, u.ToDigest)
	}
	if u.Severity != "patch" {
		t.Fatalf("severity = %q, want patch", u.Severity)
	}
	// Update is persisted and open.
	open, _ := store.NewUpdates(db).ListOpen()
	if len(open) != 1 {
		t.Fatalf("open updates = %d, want 1", len(open))
	}
	// A detected event was emitted.
	evs, _ := store.NewEvents(db).ListByService(svc.ID)
	if len(evs) != 1 || evs[0].Kind != "detected" {
		t.Fatalf("events = %+v, want one detected", evs)
	}
	// Remote state cached ok.
	rs, _ := store.NewRemoteStates(db).Get("ghcr.io/acme/web", "1.2.3")
	if rs.Status != "ok" || rs.RemoteDigest != "sha256:new" {
		t.Fatalf("remote_state = %+v", rs)
	}
}

func TestDetectNoUpdateWhenDigestMatches(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:same", false)
	r := fakeResolver{img: registry.RemoteImage{Digest: "sha256:same", PlatformDigest: "sha256:same"}}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected no update, got %+v", u)
	}
	open, _ := store.NewUpdates(db).ListOpen()
	if len(open) != 0 {
		t.Fatalf("open updates = %d, want 0", len(open))
	}
}

func TestDetectMatchesOnPlatformDigest(t *testing.T) {
	db := newDB(t)
	// Container recorded the platform manifest digest; index digest differs.
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:plat", false)
	r := fakeResolver{img: registry.RemoteImage{Digest: "sha256:index", PlatformDigest: "sha256:plat"}}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("platform-digest match should mean up-to-date, got %+v", u)
	}
}

func TestDetectRateLimitedIsNonFatal(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "docker.io/library/nginx:latest", "sha256:old", false)
	r := fakeResolver{err: &transport.Error{StatusCode: 429}}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatalf("rate-limit must be non-fatal, got err %v", err)
	}
	if u != nil {
		t.Fatalf("no update on rate limit, got %+v", u)
	}
	rs, _ := store.NewRemoteStates(db).Get("docker.io/library/nginx", "latest")
	if rs.Status != "rate_limited" {
		t.Fatalf("status = %q, want rate_limited", rs.Status)
	}
}

func TestDetectResolveErrorIsNonFatal(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:old", false)
	r := fakeResolver{err: errors.New("boom")}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatalf("resolve error must be non-fatal, got %v", err)
	}
	if u != nil {
		t.Fatal("no update on error")
	}
	rs, _ := store.NewRemoteStates(db).Get("ghcr.io/acme/web", "1.2.3")
	if rs.Status != "error" {
		t.Fatalf("status = %q, want error", rs.Status)
	}
}

func TestDetectUnmonitorableSkips(t *testing.T) {
	db := newDB(t)
	// No current digest → unmonitorable (local/never-pushed image).
	svc := seedSvc(t, db, "myapp:dev", "", false)
	u, err := newDetector(db, fakeResolver{}).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatal("unmonitorable service must not produce an update")
	}
}

func TestDetectUnmonitorableEmptyImageRef(t *testing.T) {
	db := newDB(t)
	// No image ref → unmonitorable (service has no tracked image).
	svc := seedSvc(t, db, "", "sha256:something", false)
	u, err := newDetector(db, fakeResolver{}).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("unmonitorable service (empty ImageRef) must not produce an update, got %+v", u)
	}
	open, err := store.NewUpdates(db).ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("open updates = %d, want 0", len(open))
	}
}

func TestDetectPinnedStillInforms(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web@sha256:old", "sha256:old", true)
	r := fakeResolver{img: registry.RemoteImage{Digest: "sha256:new", PlatformDigest: "sha256:new"}}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("pinned service with newer digest should still produce an informational update")
	}
	open, err := store.NewUpdates(db).ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("open updates = %d, want 1", len(open))
	}
}

func TestDetectUsesFreshCache(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:old", false)
	// Pre-seed a fresh ok cache entry; resolver would error if called.
	now := time.Now().UTC()
	_ = store.NewRemoteStates(db).Upsert(store.RemoteState{
		Repo: "ghcr.io/acme/web", Tag: "1.2.3", RemoteDigest: "sha256:cached",
		Status: "ok", ResolvedAt: &now,
	})
	r := fakeResolver{err: errors.New("resolver must not be called on a fresh cache hit")}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.ToDigest != "sha256:cached" {
		t.Fatalf("expected update from cached digest, got %+v", u)
	}
	if u.Severity != "digest-only" {
		t.Fatalf("cache-path severity = %q, want digest-only", u.Severity)
	}
}

func TestDetectIgnoresEmptyCachedDigest(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:old", false)

	// Pre-seed a fresh ok RemoteState with an empty RemoteDigest.
	now := time.Now().UTC()
	if err := store.NewRemoteStates(db).Upsert(store.RemoteState{
		Repo: "ghcr.io/acme/web", Tag: "1.2.3", RemoteDigest: "",
		Status: "ok", ResolvedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	// Resolver returns a real digest when called; if the empty cache row is
	// NOT skipped, the resolver would not be called and no update would be
	// recorded.
	resolverDigest := "sha256:real"
	r := fakeResolver{img: registry.RemoteImage{Digest: resolverDigest, PlatformDigest: resolverDigest}}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	// The resolver must have been consulted; the result must have the resolver's digest.
	if u == nil || u.ToDigest != resolverDigest {
		t.Fatalf("expected update with digest %q from resolver (empty cache should be skipped), got %+v", resolverDigest, u)
	}
}

func TestDetectorSuppressesDuplicateDetectedEvent(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:local", false)
	r := fakeResolver{img: registry.RemoteImage{Digest: "sha256:remote", PlatformDigest: "sha256:remote"}}
	det := newDetector(db, r)

	if _, err := det.Detect(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	if _, err := det.Detect(context.Background(), svc); err != nil { // re-detect same drift
		t.Fatal(err)
	}
	events, err := store.NewEvents(db).ListByService(svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	detected := 0
	for _, e := range events {
		if e.Kind == "detected" {
			detected++
		}
	}
	if detected != 1 {
		t.Fatalf("detected events = %d, want exactly 1 (duplicate suppressed)", detected)
	}
}

func TestDetectSuggestsNewerSemverTag(t *testing.T) {
	db := newDB(t)
	// Service tracks nginx:1.2.3, running digest sha256:old. The tracked tag's
	// own remote digest is unchanged; a newer stable tag (1.3.0) exists.
	svc := seedSvc(t, db, "docker.io/library/nginx:1.2.3", "sha256:old", false)
	r := fakeResolver{
		img:  registry.RemoteImage{Digest: "sha256:old", PlatformDigest: "sha256:old"},
		tags: []string{"1.2.3", "1.3.0", "1.3.0-rc1", "latest"},
		byRef: map[string]registry.RemoteImage{
			"docker.io/library/nginx:1.3.0": {Digest: "sha256:new", PlatformDigest: "sha256:new"},
		},
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected an update, got nil")
	}
	if u.ToDigest != "sha256:new" {
		t.Fatalf("to_digest = %q, want sha256:new", u.ToDigest)
	}
	if u.Tag != "1.3.0" {
		t.Fatalf("tag = %q, want 1.3.0", u.Tag)
	}
	if u.FromVersion != "1.2.3" || u.ToVersion != "1.3.0" {
		t.Fatalf("versions = %s -> %s, want 1.2.3 -> 1.3.0", u.FromVersion, u.ToVersion)
	}
	if u.Severity != "minor" {
		t.Fatalf("severity = %q, want minor", u.Severity)
	}
	// The target image's identity must be persisted too (changelog resolver
	// reads labels for upd.ToDigest).
	img, err := store.NewImages(db).GetByDigest("docker.io/library/nginx", "sha256:new")
	if err != nil {
		t.Fatalf("target image not recorded: %v", err)
	}
	if img.Tag != "1.3.0" {
		t.Fatalf("recorded image tag = %q, want 1.3.0", img.Tag)
	}
}

// TestDetectFloatingPartialSemverSkipsCrossTagScan covers the merge-blocking
// bug: a partial-semver tag (1.31) is FLOATING per detect.ClassifyTag, not an
// exact pin. Apply floats a floating tag's stream on `pull && up` (it never
// writes an out-of-stream tag to the compose file), so the cross-tag semver
// scan must not run for it, even though a strictly-newer out-of-stream tag
// (1.32.0) exists in the registry. Only same-tag digest drift (the "1.31"
// stream itself moving) may be reported.
func TestDetectFloatingPartialSemverSkipsCrossTagScan(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "docker.io/library/nginx:1.31", "sha256:old", false)
	r := fakeResolver{
		// Same-tag ("1.31") resolve: the stream moved to a new digest.
		img:  registry.RemoteImage{Digest: "sha256:new", PlatformDigest: "sha256:new"},
		tags: []string{"1.31", "1.32.0"},
		byRef: map[string]registry.RemoteImage{
			// If the (buggy) cross-tag scan ran, it would resolve this ref and
			// the test would observe THIS digest/tag instead.
			"docker.io/library/nginx:1.32.0": {Digest: "sha256:crosstag", PlatformDigest: "sha256:crosstag"},
		},
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected a same-tag digest-drift update, got nil")
	}
	if u.Tag != "1.31" {
		t.Fatalf("tag = %q, want tracked tag 1.31 (no cross-tag suggestion for a floating tag)", u.Tag)
	}
	if u.ToDigest != "sha256:new" {
		t.Fatalf("to_digest = %q, want sha256:new (same-tag resolve, not the cross-tag digest)", u.ToDigest)
	}
	// The out-of-stream tag must never have been resolved at all.
	if _, err := store.NewImages(db).GetByDigest("docker.io/library/nginx", "sha256:crosstag"); err == nil {
		t.Fatal("cross-tag image must not be recorded for a floating tracked tag")
	}
}

func TestDetectTagListFailureFallsBackToDigestCompare(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:old", false)
	r := fakeResolver{
		img:     registry.RemoteImage{Digest: "sha256:new", PlatformDigest: "sha256:new"},
		tagsErr: errors.New("list tags: boom"),
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatalf("tag-list failure must be non-fatal, got %v", err)
	}
	if u == nil {
		t.Fatal("expected a digest-only update despite the tag-list failure")
	}
	if u.ToDigest != "sha256:new" {
		t.Fatalf("to_digest = %q, want sha256:new", u.ToDigest)
	}
	if u.Tag != "1.2.3" {
		t.Fatalf("tag = %q, want tracked tag 1.2.3 (semver scan skipped)", u.Tag)
	}
	if u.Severity != "digest-only" {
		t.Fatalf("severity = %q, want digest-only (no version label, tag unchanged)", u.Severity)
	}
}

// countingResolver counts Resolve calls so a test can assert whether the
// network path was consulted.
type countingResolver struct {
	calls  *int
	digest string
}

func (c countingResolver) Resolve(_ context.Context, ref string, _ registry.Platform) (registry.RemoteImage, error) {
	*c.calls++
	return registry.RemoteImage{Ref: ref, Digest: c.digest, PlatformDigest: c.digest}, nil
}

func (c countingResolver) ListTags(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (c countingResolver) Head(_ context.Context, _ string) (string, error) {
	return c.digest, nil
}

// TestDetectCacheTTLReadPerCall proves cacheTTL is consulted on every Detect
// call rather than captured once at NewDetector time: the same detector
// instance, given the same stale-ish cache row, behaves differently across
// two calls purely because the closure's returned value changed in between.
func TestDetectCacheTTLReadPerCall(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:old", false)

	// Seed a cache row that is 90s old.
	seededAt := time.Now().UTC().Add(-90 * time.Second)
	if err := store.NewRemoteStates(db).Upsert(store.RemoteState{
		Repo: "ghcr.io/acme/web", Tag: "1.2.3", RemoteDigest: "sha256:cached",
		Status: "ok", ResolvedAt: &seededAt,
	}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	r := countingResolver{calls: &calls, digest: "sha256:resolved"}

	ttl := 5 * time.Minute // long enough to cover the 90s-old row
	d := detect.NewDetector(
		r, store.NewUpdates(db), store.NewImages(db),
		store.NewRemoteStates(db), store.NewEvents(db), store.NewTagDigests(db),
		registry.HostPlatform(), func() time.Duration { return ttl },
	)

	// 1st Detect: ttl is long, so the 90s-old row reads as fresh -> cache hit,
	// resolver untouched.
	u1, err := d.Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("expected cache hit (no resolver call) on first Detect, got %d calls", calls)
	}
	if u1 == nil || u1.ToDigest != "sha256:cached" {
		t.Fatalf("expected update from cached digest on first Detect, got %+v", u1)
	}

	// Flip ttl short. The row itself hasn't changed (a cache hit never
	// upserts) -- only the closure's return value did. If cacheTTL were
	// captured once at construction, this call would still hit the cache.
	ttl = 1 * time.Second
	u2, err := d.Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected resolver to be called after ttl flip (cache now stale), got %d calls", calls)
	}
	if u2 == nil || u2.ToDigest != "sha256:resolved" {
		t.Fatalf("expected update from resolver after ttl flip, got %+v", u2)
	}
}

// TestDetectFloatingTagNamesVersionsViaReverseLookup covers the backrest case:
// a fully-floating tag (latest) with no version label. Both the running and the
// target digests are named by HEAD-matching them back to the repo's stable
// semver tags. The reported tag/to_digest stay the floating tag (apply floats
// latest, it never moves to v1.14.1); only the version strings are enriched.
func TestDetectFloatingTagNamesVersionsViaReverseLookup(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "docker.io/garethgeorge/backrest:latest", "sha256:v1130", false)
	r := fakeResolver{
		// latest resolves to the newest release digest (drift vs running), no
		// version label (mirrors backrest, which sets only image.source).
		img:  registry.RemoteImage{Digest: "sha256:v1141", PlatformDigest: "sha256:v1141"},
		tags: []string{"v1.13.0", "v1.14.0", "v1.14.1", "v1.14.1-rc1", "latest"},
		head: map[string]string{
			"docker.io/garethgeorge/backrest:v1.13.0": "sha256:v1130",
			"docker.io/garethgeorge/backrest:v1.14.0": "sha256:v1140",
			"docker.io/garethgeorge/backrest:v1.14.1": "sha256:v1141",
		},
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected an update, got nil")
	}
	// Apply floats latest: the update stays on the tracked tag + its digest.
	if u.Tag != "latest" {
		t.Fatalf("tag = %q, want latest (apply floats the tracked tag)", u.Tag)
	}
	if u.ToDigest != "sha256:v1141" {
		t.Fatalf("to_digest = %q, want sha256:v1141", u.ToDigest)
	}
	// ...but the version strings are named from the digests.
	if u.FromVersion != "v1.13.0" || u.ToVersion != "v1.14.1" {
		t.Fatalf("versions = %s -> %s, want v1.13.0 -> v1.14.1", u.FromVersion, u.ToVersion)
	}
	if u.Severity != "minor" {
		t.Fatalf("severity = %q, want minor", u.Severity)
	}
}

// TestDetectFloatingReverseLookupNonFatal proves the reverse scan is best-effort:
// a tag-list failure leaves the version strings blank and the update is still
// recorded as a plain digest-only drift.
func TestDetectFloatingReverseLookupNonFatal(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "docker.io/garethgeorge/backrest:latest", "sha256:v1130", false)
	r := fakeResolver{
		img:     registry.RemoteImage{Digest: "sha256:v1141", PlatformDigest: "sha256:v1141"},
		tagsErr: errors.New("list tags: boom"),
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatalf("reverse-lookup tag-list failure must be non-fatal, got %v", err)
	}
	if u == nil {
		t.Fatal("expected a digest-only update despite the tag-list failure")
	}
	if u.ToDigest != "sha256:v1141" || u.Tag != "latest" {
		t.Fatalf("update = %s@%s, want latest@sha256:v1141", u.Tag, u.ToDigest)
	}
	if u.FromVersion != "" || u.ToVersion != "" {
		t.Fatalf("versions = %q -> %q, want both blank", u.FromVersion, u.ToVersion)
	}
	if u.Severity != "digest-only" {
		t.Fatalf("severity = %q, want digest-only", u.Severity)
	}
}

// TestDetectReverseLookupUsesTagCache proves the permanent tag->digest cache
// spares a re-HEAD: a pre-cached version tag is matched from the cache (no Head
// call), while an uncached tag is HEADed once and then cached.
func TestDetectReverseLookupUsesTagCache(t *testing.T) {
	db := newDB(t)
	const repo = "docker.io/garethgeorge/backrest"
	// Pre-cache the running release's digest so its HEAD is unnecessary.
	if err := store.NewTagDigests(db).Put(repo, "v1.13.0", "sha256:v1130"); err != nil {
		t.Fatal(err)
	}
	svc := seedSvc(t, db, repo+":latest", "sha256:v1130", false)
	seen := map[string]int{}
	r := fakeResolver{
		img:  registry.RemoteImage{Digest: "sha256:v1141", PlatformDigest: "sha256:v1141"},
		tags: []string{"v1.13.0", "v1.14.0", "v1.14.1", "latest"},
		head: map[string]string{
			repo + ":v1.14.0": "sha256:v1140",
			repo + ":v1.14.1": "sha256:v1141",
		},
		headSeen: seen,
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.FromVersion != "v1.13.0" || u.ToVersion != "v1.14.1" {
		t.Fatalf("versions = %+v, want v1.13.0 -> v1.14.1", u)
	}
	// The cached tag must not have been HEADed; the uncached target tag must.
	if n := seen[repo+":v1.13.0"]; n != 0 {
		t.Fatalf("v1.13.0 HEAD count = %d, want 0 (served from cache)", n)
	}
	if n := seen[repo+":v1.14.1"]; n != 1 {
		t.Fatalf("v1.14.1 HEAD count = %d, want 1", n)
	}
	// The HEAD result must have been written back to the cache.
	if dg, ok, err := store.NewTagDigests(db).Get(repo, "v1.14.1"); err != nil || !ok || dg != "sha256:v1141" {
		t.Fatalf("cache after Detect: dg=%q ok=%v err=%v, want sha256:v1141", dg, ok, err)
	}
}

func TestDetectSplitRef(t *testing.T) {
	cases := []struct{ ref, repo, tag string }{
		{"nginx", "nginx", "latest"},
		{"nginx:1.25", "nginx", "1.25"},
		{"ghcr.io/acme/web:1.2.3", "ghcr.io/acme/web", "1.2.3"},
		{"ghcr.io/acme/web@sha256:abc", "ghcr.io/acme/web", "latest"},
		{"localhost:5000/app:dev", "localhost:5000/app", "dev"},
	}
	for _, c := range cases {
		repo, tag := detect.SplitRef(c.ref)
		if repo != c.repo || tag != c.tag {
			t.Errorf("SplitRef(%q) = (%q, %q), want (%q, %q)", c.ref, repo, tag, c.repo, c.tag)
		}
	}
}
