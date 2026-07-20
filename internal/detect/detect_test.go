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

	// configByRef backs ConfigDigest, keyed by "repo:tag". A missing ref falls
	// back to img.ConfigDigest.
	configByRef map[string]string

	// headErr, when set, is returned by ConfigDigest for every ref (simulates a
	// registry error, e.g. rate-limit, during the reverse version-naming scan).
	headErr error

	// configSeen, when non-nil, counts ConfigDigest calls per ref (maps are
	// reference types, so a value-receiver method still mutates the shared
	// map). Adaptation over the brief: needed to migrate
	// TestDetectReverseLookupUsesTagCache's call-count assertions from Head to
	// ConfigDigest.
	configSeen map[string]int
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

func (f fakeResolver) ConfigDigest(_ context.Context, ref string, _ registry.Platform) (string, error) {
	if f.configSeen != nil {
		f.configSeen[ref]++
	}
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

func newDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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

// TestDetectUpToDateSupersedesStaleOpenUpdate proves that once a service's
// running image reaches an available update's target, a later up-to-date detect
// closes that stale row. This is the dockbrr-on-itself case: applying a
// self-update recreates dockbrr's OWN container, killing the process before
// worker.MarkApplied runs, so the update stays 'available'. The next detect
// finds the tag already up to date and must supersede it, otherwise the
// dashboard keeps rendering the running image with the stale from_version.
func TestDetectUpToDateSupersedesStaleOpenUpdate(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/yorah/dockbrr:latest", "sha256:v041", false)
	// 1. Drift detected: remote latest moved to 0.4.2's digest.
	drift := fakeResolver{img: registry.RemoteImage{
		Digest: "sha256:v042", PlatformDigest: "sha256:v042",
		Labels: map[string]string{"org.opencontainers.image.version": "0.4.2"},
	}}
	if _, err := newDetector(db, drift).Detect(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	if open, _ := store.NewUpdates(db).ListOpen(); len(open) != 1 {
		t.Fatalf("after drift: open updates = %d, want 1", len(open))
	}

	// 2. Container reached the target OUTSIDE MarkApplied (self-update recreate).
	// A fresh detect now sees latest == the running digest.
	svc.CurrentDigest = "sha256:v042"
	upToDate := fakeResolver{img: registry.RemoteImage{
		Digest: "sha256:v042", PlatformDigest: "sha256:v042",
		Labels: map[string]string{"org.opencontainers.image.version": "0.4.2"},
	}}
	u, err := newDetector(db, upToDate).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("up-to-date service must yield no update, got %+v", u)
	}
	if open, _ := store.NewUpdates(db).ListOpen(); len(open) != 0 {
		t.Fatalf("stale update not superseded: open updates = %d, want 0", len(open))
	}
}

// TestDetectCacheHitUpToDateKeepsSemverUpdateOpen pins the digest-only
// cache-hit path's supersede scope: the tracked tag matching the running
// digest says NOTHING about a semver update targeting a different tag (the
// cache-hit path skips the tag scan), so that row must stay open. Before this
// was narrowed, a scan-all inside the cache TTL right after a manual check
// flapped the freshly (re)opened semver update back to superseded.
func TestDetectCacheHitUpToDateKeepsSemverUpdateOpen(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/acme/web:1.2.3", "sha256:old", false)
	updates := store.NewUpdates(db)

	// A semver update to a different tag's digest...
	semverID, _, err := updates.RecordDrift(store.Update{
		ServiceID: svc.ID, FromDigest: "sha256:old", ToDigest: "sha256:semver",
		Tag: "1.3.0", Severity: "minor", Status: "available",
	})
	if err != nil {
		t.Fatal(err)
	}
	// ...and a same-tag update whose target the service has since reached
	// (to_digest == running digest). RecordDrift's tail superseded the semver
	// row (one-available invariant); force both open to observe the scope.
	reachedID, _, err := updates.RecordDrift(store.Update{
		ServiceID: svc.ID, FromDigest: "sha256:older", ToDigest: "sha256:old",
		Tag: "1.2.3", Status: "available",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := updates.SetStatus(semverID, "available"); err != nil {
		t.Fatal(err)
	}

	// Fresh cache: tracked tag resolves to the running digest (up to date).
	now := time.Now().UTC()
	_ = store.NewRemoteStates(db).Upsert(store.RemoteState{
		Repo: "ghcr.io/acme/web", Tag: "1.2.3", RemoteDigest: "sha256:old",
		Status: "ok", ResolvedAt: &now,
	})
	r := fakeResolver{err: errors.New("resolver must not be called on a fresh cache hit")}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("expected no update on cache-hit up-to-date, got %+v", u)
	}
	if got, _ := updates.Get(reachedID); got.Status != "superseded" {
		t.Fatalf("reached-target row status = %q, want superseded", got.Status)
	}
	if got, _ := updates.Get(semverID); got.Status != "available" {
		t.Fatalf("semver row status = %q, want available (cache-hit path must not close it)", got.Status)
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

// TestDetectNotFoundStatus pins the classification of "image is not in the
// registry": a 404, or the 401 public registries answer for an anonymous
// request to a nonexistent repo (Docker Hub hides existence behind
// Unauthorized). Both are the steady state for a locally built image, so they
// record status not_found (dashboard: muted "not in registry" hint) instead of
// a hard error.
func TestDetectNotFoundStatus(t *testing.T) {
	for _, code := range []int{401, 404} {
		db := newDB(t)
		svc := seedSvc(t, db, "docker.io/library/localonly:latest", "sha256:old", false)
		r := fakeResolver{err: &transport.Error{StatusCode: code}}
		u, err := newDetector(db, r).Detect(context.Background(), svc)
		if err != nil {
			t.Fatalf("code %d must be non-fatal, got err %v", code, err)
		}
		if u != nil {
			t.Fatalf("no update on code %d, got %+v", code, u)
		}
		rs, _ := store.NewRemoteStates(db).Get("docker.io/library/localonly", "latest")
		if rs.Status != "not_found" {
			t.Fatalf("code %d: status = %q, want not_found", code, rs.Status)
		}
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

func (c countingResolver) ConfigDigest(_ context.Context, _ string, _ registry.Platform) (string, error) {
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
	svc.CurrentImageID = "sha256:cfg-v1130" // running container's config digest
	r := fakeResolver{
		// latest resolves to the newest release digest (drift vs running), no
		// version label (mirrors backrest, which sets only image.source).
		img:  registry.RemoteImage{Digest: "sha256:v1141", PlatformDigest: "sha256:v1141", ConfigDigest: "sha256:cfg-v1141"},
		tags: []string{"v1.13.0", "v1.14.0", "v1.14.1", "v1.14.1-rc1", "latest"},
		configByRef: map[string]string{
			"docker.io/garethgeorge/backrest:v1.13.0": "sha256:cfg-v1130",
			"docker.io/garethgeorge/backrest:v1.14.0": "sha256:cfg-v1140",
			"docker.io/garethgeorge/backrest:v1.14.1": "sha256:cfg-v1141",
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

// TestDetectUpToDateFloatingTagResolvesCurrentVersion covers the homepage case:
// a floating tag whose running digest already equals the remote latest (no
// update pending) still gets its running version named and cached on the image
// row, so the dashboard can show "v1.13.2" instead of just ":latest".
func TestDetectUpToDateFloatingTagResolvesCurrentVersion(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/gethomepage/homepage:latest", "sha256:v1132", false)
	// svc.CurrentImageID left blank: exercises resolveCurrentVersion's fallback
	// to the up-to-date remote's own ConfigDigest as the running image's config.
	r := fakeResolver{
		// latest resolves to the SAME digest the container runs (up to date),
		// with no version label (homepage ships none).
		img:  registry.RemoteImage{Digest: "sha256:v1132", PlatformDigest: "sha256:v1132", ConfigDigest: "sha256:cfg-v1132"},
		tags: []string{"v1.13.0", "v1.13.1", "v1.13.2", "latest"},
		configByRef: map[string]string{
			"ghcr.io/gethomepage/homepage:v1.13.0": "sha256:cfg-v1130",
			"ghcr.io/gethomepage/homepage:v1.13.1": "sha256:cfg-v1131",
			"ghcr.io/gethomepage/homepage:v1.13.2": "sha256:cfg-v1132",
		},
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("up-to-date service must yield no update, got %+v", u)
	}
	img, err := store.NewImages(db).GetByDigest("ghcr.io/gethomepage/homepage", "sha256:v1132")
	if err != nil {
		t.Fatalf("image row: %v", err)
	}
	if img.ResolvedVersion != "v1.13.2" {
		t.Fatalf("resolved_version = %q, want v1.13.2", img.ResolvedVersion)
	}
}

// TestDetectFloatingTagNamesFromViaRunningImageLabel proves a floating tag names
// its "from" version from the running image's own OCI version label (captured at
// discovery), without any reverse HEAD scan. This is the linuxserver case: the
// versioned tags are pre-release (1.6.0-lsNNN) and the reverse scan skips them,
// but both images carry org.opencontainers.image.version, so both ends resolve.
func TestDetectFloatingTagNamesFromViaRunningImageLabel(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "ghcr.io/linuxserver/bazarr:latest", "sha256:old", false)
	svc.ImageVersion = "1.6.0-ls354" // running image's label, set at discovery
	r := fakeResolver{
		// latest now resolves to a newer digest whose label carries the target
		// version; no tags/head are needed because both ends read from labels.
		img: registry.RemoteImage{
			Digest:         "sha256:new",
			PlatformDigest: "sha256:new",
			Labels:         map[string]string{"org.opencontainers.image.version": "1.6.0-ls355"},
		},
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected an update, got nil")
	}
	if u.Tag != "latest" || u.ToDigest != "sha256:new" {
		t.Fatalf("update = %s@%s, want latest@sha256:new", u.Tag, u.ToDigest)
	}
	if u.FromVersion != "1.6.0-ls354" || u.ToVersion != "1.6.0-ls355" {
		t.Fatalf("versions = %q -> %q, want 1.6.0-ls354 -> 1.6.0-ls355", u.FromVersion, u.ToVersion)
	}
}

// TestDetectFloatingReverseLookupNonFatal proves the reverse scan is best-effort:
// a tag-list failure leaves the version strings blank and the update is still
// recorded as a plain digest-only drift. Both svc.CurrentImageID and the
// target's ConfigDigest must be non-empty here, otherwise
// matchVersionByDigest's configDigest=="" early-return fires before
// d.resolver.ListTags is ever called and tagsErr goes unconsulted (see
// task-3-review.md: this test used to genuinely exercise the ListTags-failure
// path under the pre-refactor reverseVersions, then silently stopped doing so
// once matchVersionByDigest gained the config-digest empty-guard).
func TestDetectFloatingReverseLookupNonFatal(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "docker.io/garethgeorge/backrest:latest", "sha256:v1130", false)
	svc.CurrentImageID = "sha256:cfg-v1130" // non-empty so the "from" match reaches ListTags
	r := fakeResolver{
		img:     registry.RemoteImage{Digest: "sha256:v1141", PlatformDigest: "sha256:v1141", ConfigDigest: "sha256:cfg-v1141"},
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

// TestDetectReverseLookupUsesTagCache proves the permanent tag->config-digest
// cache spares a re-lookup: a pre-cached version tag is matched from the cache
// (no ConfigDigest call), while an uncached tag is looked up once and then
// cached.
func TestDetectReverseLookupUsesTagCache(t *testing.T) {
	db := newDB(t)
	const repo = "docker.io/garethgeorge/backrest"
	// Pre-cache the running release's config digest so its lookup is unnecessary.
	if err := store.NewTagDigests(db).Put(repo, "v1.13.0", "sha256:cfg-v1130"); err != nil {
		t.Fatal(err)
	}
	svc := seedSvc(t, db, repo+":latest", "sha256:v1130", false)
	svc.CurrentImageID = "sha256:cfg-v1130" // running container's config digest
	seen := map[string]int{}
	r := fakeResolver{
		img:  registry.RemoteImage{Digest: "sha256:v1141", PlatformDigest: "sha256:v1141", ConfigDigest: "sha256:cfg-v1141"},
		tags: []string{"v1.13.0", "v1.14.0", "v1.14.1", "latest"},
		configByRef: map[string]string{
			repo + ":v1.14.0": "sha256:cfg-v1140",
			repo + ":v1.14.1": "sha256:cfg-v1141",
		},
		configSeen: seen,
	}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.FromVersion != "v1.13.0" || u.ToVersion != "v1.14.1" {
		t.Fatalf("versions = %+v, want v1.13.0 -> v1.14.1", u)
	}
	// The cached tag must not have been looked up; the uncached target tag must.
	if n := seen[repo+":v1.13.0"]; n != 0 {
		t.Fatalf("v1.13.0 ConfigDigest count = %d, want 0 (served from cache)", n)
	}
	if n := seen[repo+":v1.14.1"]; n != 1 {
		t.Fatalf("v1.14.1 ConfigDigest count = %d, want 1", n)
	}
	// The lookup result must have been written back to the cache.
	if dg, ok, err := store.NewTagDigests(db).Get(repo, "v1.14.1"); err != nil || !ok || dg != "sha256:cfg-v1141" {
		t.Fatalf("cache after Detect: dg=%q ok=%v err=%v, want sha256:cfg-v1141", dg, ok, err)
	}
}

// TestDetectFloatingTagNamesVersionByConfigDigestOverBogusLabel is the reported
// bug: technitium/dns-server:latest labels org.opencontainers.image.version as
// the base-OS version ("24.04") while shipping app version 15.x, and multi-arch
// manifest-list digests differ per tag even for the same platform image (so a
// list-digest match can never work here). Matching on the PLATFORM CONFIG
// digest instead finds the real repo tag and must win over the bogus label.
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
			"technitium/dns-server:15.4.0": cfg1540,      // matches target -> "to" = 15.4.0
			"technitium/dns-server:15.3.0": "sha256:cfg-1530",
			"technitium/dns-server:15.2.0": runImageID, // matches running -> "from" = 15.2.0
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

// TestDetectSkipsLocalImage proves a locally built (compose build:) image
// short-circuits before any registry call: Detect must return (nil, nil) and
// record the remote state as "local" without ever invoking the resolver.
func TestDetectSkipsLocalImage(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "api:dev", "sha256:x", false)
	svc.ImageLocal = true
	r := fakeResolver{err: errors.New("resolver must not be called for a local image")}
	u, err := newDetector(db, r).Detect(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("upd = %+v, want nil (local image)", u)
	}
	st, err := store.NewRemoteStates(db).Get("api", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "local" {
		t.Fatalf("status = %q, want local", st.Status)
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

// tagListCountingResolver counts ListTags calls to assert the negative cache
// prevents a re-scan on a later detect for the same digest. Named distinctly
// from the existing countingResolver (which counts Resolve calls for the cache
// TTL test) to avoid a duplicate type declaration in this package.
type tagListCountingResolver struct {
	fakeResolver
	listCalls *int
}

func (c tagListCountingResolver) ListTags(ctx context.Context, repo string) ([]string, error) {
	*c.listCalls++
	return c.fakeResolver.ListTags(ctx, repo)
}

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
	r := tagListCountingResolver{
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

	// Force the remote-state cache row stale so the second Detect takes the
	// full resolve path (through resolveCurrentVersion) rather than a cheap
	// digest-only cache hit; the cache-hit path (step 1) returns before
	// resolveCurrentVersion ever runs, which would make the assertion below
	// pass without actually exercising the version_resolved negative cache.
	stale := time.Now().UTC().Add(-2 * time.Minute)
	if err := store.NewRemoteStates(db).Upsert(store.RemoteState{
		Repo: "acme/app", Tag: "latest", RemoteDigest: "sha256:list",
		Status: "ok", ResolvedAt: &stale,
	}); err != nil {
		t.Fatal(err)
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

// TestResolveCurrentVersionInconclusiveScanNotPersisted is the mirror image of
// TestResolveCurrentVersionNegativeCache: it drives matchVersionByDigest to
// conclusive=false (via a rate-limited per-tag ConfigDigest lookup, distinct
// from TestDetectFloatingReverseLookupNonFatal's ListTags-failure trigger) and
// proves resolveCurrentVersion's `if tagName == "" && !conclusive { return }`
// guard holds: a transient abort must NOT set version_resolved, or a
// rate-limit would permanently poison the negative cache and the image would
// never get a real version name once the registry recovers.
func TestResolveCurrentVersionInconclusiveScanNotPersisted(t *testing.T) {
	db := newDB(t)
	svc := seedSvc(t, db, "acme/floaty:latest", "sha256:list3", false)
	svc.CurrentImageID = "sha256:cfg-head3" // non-empty: skip the configDigest=="" early-return

	seen := map[string]int{}
	r := fakeResolver{
		// latest resolves to the SAME digest the container runs (up to date),
		// so Detect takes the resolveCurrentVersion path (step 4), not the
		// drift/record path.
		img:  registry.RemoteImage{Digest: "sha256:list3", PlatformDigest: "sha256:list3"},
		tags: []string{"1.0.0"},
		// Every per-tag ConfigDigest call is rate-limited, aborting the loop on
		// the first candidate (matchVersionByDigest's IsRateLimited branch).
		headErr:    &transport.Error{StatusCode: 429},
		configSeen: seen,
	}
	det := newDetector(db, r)

	if _, err := det.Detect(context.Background(), svc); err != nil {
		t.Fatalf("rate-limited reverse scan must be non-fatal, got %v", err)
	}
	img, err := store.NewImages(db).GetByDigest("acme/floaty", "sha256:list3")
	if err != nil {
		t.Fatalf("image row: %v", err)
	}
	if img.VersionResolved {
		t.Fatal("VersionResolved = true after an inconclusive (rate-limited) scan, want false (must not poison the negative cache)")
	}
	if n := seen["acme/floaty:1.0.0"]; n != 1 {
		t.Fatalf("ConfigDigest calls = %d, want 1 (loop aborts on first rate-limited candidate)", n)
	}

	// Force the remote-state cache row stale so a second Detect takes the full
	// resolve path again rather than a cheap digest-only cache hit (which
	// never calls resolveCurrentVersion at all).
	stale := time.Now().UTC().Add(-2 * time.Minute)
	if err := store.NewRemoteStates(db).Upsert(store.RemoteState{
		Repo: "acme/floaty", Tag: "latest", RemoteDigest: "sha256:list3",
		Status: "ok", ResolvedAt: &stale,
	}); err != nil {
		t.Fatal(err)
	}

	// Second detect: since VersionResolved is still false, the scan must
	// retry (not be short-circuited by the "already attempted" guard).
	if _, err := det.Detect(context.Background(), svc); err != nil {
		t.Fatalf("second detect: rate-limited reverse scan must be non-fatal, got %v", err)
	}
	if n := seen["acme/floaty:1.0.0"]; n != 2 {
		t.Fatalf("ConfigDigest calls after 2nd detect = %d, want 2 (inconclusive scan must retry, not be skipped)", n)
	}
	img, err = store.NewImages(db).GetByDigest("acme/floaty", "sha256:list3")
	if err != nil {
		t.Fatalf("image row after 2nd detect: %v", err)
	}
	if img.VersionResolved {
		t.Fatal("VersionResolved = true after a second inconclusive scan, want false (still not poisoned)")
	}
}
