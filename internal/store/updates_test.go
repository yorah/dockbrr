package store_test

import (
	"errors"
	"testing"

	"dockbrr/internal/store"
)

func seedService(t *testing.T, db *store.DB) int64 {
	t.Helper()
	return seedServiceNamed(t, db, "app")
}

// seedServiceNamed is seedService parameterized by service name, so tests
// needing multiple distinct services in the same project (e.g. a per-service
// aggregate like ListLastAppliedByService) don't collide on the (project_id,
// name) upsert key.
func seedServiceNamed(t *testing.T, db *store.DB, name string) int64 {
	t.Helper()
	pid, err := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: name, State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func TestUpdatesUpsertInsertsAndListsOpen(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:old", ToDigest: "sha256:new",
		FromVersion: "1.2.3", ToVersion: "1.3.0", Tag: "1.3.0",
		Severity: "minor", Status: "available",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id = %d", id)
	}
	open, err := u.ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("len = %d, want 1", len(open))
	}
	if open[0].ToDigest != "sha256:new" || open[0].Severity != "minor" {
		t.Fatalf("row = %+v", open[0])
	}
}

func TestUpdatesUpsertByNaturalKeyRefreshes(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id1, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Severity: "digest-only", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Severity: "patch", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("new id %d, want %d", id2, id1)
	}
	open, err := u.ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if open[0].Severity != "patch" {
		t.Fatalf("severity not refreshed: %q", open[0].Severity)
	}
}

func TestUpdatesUpsertPreservesStatusOnConflict(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// Insert the initial update; it defaults to available.
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Severity: "digest-only", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a user dismiss (Phase 5/6 action).
	if _, err := db.Exec(`UPDATE updates SET status='dismissed' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	// Re-detect: same (service_id, to_digest) with Status:"available" and new Severity.
	id2, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Severity: "patch", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("conflict should return existing id %d, got %d", id, id2)
	}

	// Query the row directly. ListOpen filters to available, so would not return a dismissed row.
	var status, severity string
	if err := db.QueryRow(`SELECT status, severity FROM updates WHERE id=?`, id).Scan(&status, &severity); err != nil {
		t.Fatal(err)
	}
	if status != "dismissed" {
		t.Fatalf("status = %q, want dismissed (should be preserved, not resurrected to available)", status)
	}
	if severity != "patch" {
		t.Fatalf("severity = %q, want patch (mutable columns should be refreshed)", severity)
	}
}

func TestUpdatesSupersedePriorOpen(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	if _, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:a", Status: "available"}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:b", Status: "available"}); err != nil {
		t.Fatal(err)
	}
	n, err := u.SupersedePriorOpen(sid, "sha256:b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("superseded %d rows, want 1", n)
	}
	open, err := u.ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ToDigest != "sha256:b" {
		t.Fatalf("open = %+v, want only sha256:b", open)
	}
}

func TestUpdatesSetChangelog(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelog(id, "https://github.com/acme/web/releases/tag/v1.2.4", "## 1.2.4\n- fix"); err != nil {
		t.Fatal(err)
	}
	open, _ := u.ListOpen()
	if len(open) != 1 {
		t.Fatalf("len = %d, want 1", len(open))
	}
	if open[0].ChangelogURL != "https://github.com/acme/web/releases/tag/v1.2.4" {
		t.Fatalf("changelog_url = %q", open[0].ChangelogURL)
	}
	if open[0].ChangelogText != "## 1.2.4\n- fix" {
		t.Fatalf("changelog_text = %q", open[0].ChangelogText)
	}
}

func TestUpdatesUpsertPreservesChangelogOnConflict(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Severity: "minor", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelog(id, "https://example.com/notes", "notes body"); err != nil {
		t.Fatal(err)
	}
	// Re-detect: same to_digest re-upserted with empty changelog fields.
	if _, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Severity: "minor", Status: "available"}); err != nil {
		t.Fatal(err)
	}
	open, err := u.ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("len = %d, want 1", len(open))
	}
	if open[0].ChangelogURL != "https://example.com/notes" || open[0].ChangelogText != "notes body" {
		t.Fatalf("changelog wiped on conflict: url=%q text=%q", open[0].ChangelogURL, open[0].ChangelogText)
	}
}

func TestUpdatesGetLatestOpenByService(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	if _, err := u.GetLatestOpenByService(sid); !errors.Is(err, store.ErrNoOpenUpdate) {
		t.Fatalf("empty: err = %v, want ErrNoOpenUpdate", err)
	}
	_, _ = u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "available"})
	got, err := u.GetLatestOpenByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.ToDigest != "sha256:new" {
		t.Fatalf("to_digest = %q", got.ToDigest)
	}
}

func TestHasAnyByService(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	has, err := u.HasAnyByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("HasAnyByService = true for a service with no update rows, want false")
	}

	if _, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "1.0", Status: "dismissed",
	}); err != nil {
		t.Fatal(err)
	}
	has, err = u.HasAnyByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("HasAnyByService = false after inserting a row, want true")
	}
}

func TestUpdatesRecordDriftNewThenIdempotent(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	id1, isNew1, err := u.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:a", Tag: "1.0.0", Severity: "minor"})
	if err != nil {
		t.Fatal(err)
	}
	if !isNew1 {
		t.Fatal("first RecordDrift isNew=false, want true")
	}

	// Re-detect the SAME digest: not new, same id, mutable cols refreshed.
	id2, isNew2, err := u.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:a", Tag: "1.0.1", Severity: "patch"})
	if err != nil {
		t.Fatal(err)
	}
	if isNew2 {
		t.Fatal("re-detect isNew=true, want false")
	}
	if id2 != id1 {
		t.Fatalf("re-detect id = %d, want %d", id2, id1)
	}
	var tag string
	_ = db.QueryRow(`SELECT tag FROM updates WHERE id=?`, id1).Scan(&tag)
	if tag != "1.0.1" {
		t.Fatalf("tag = %q, want refreshed 1.0.1", tag)
	}
}

func TestUpdatesRecordDriftSupersedesPriorOpen(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	oldID, _, _ := u.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:old"})
	// A newer digest arrives -> the old open update is superseded.
	if _, _, err := u.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:new"}); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM updates WHERE id=?`, oldID).Scan(&status)
	if status != "superseded" {
		t.Fatalf("old update status = %q, want superseded", status)
	}
	open, _ := u.ListOpen()
	if len(open) != 1 || open[0].ToDigest != "sha256:new" {
		t.Fatalf("open updates = %+v, want only sha256:new", open)
	}
}

func TestUpdatesRecordDriftPreservesChangelogAndStatus(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, _, _ := u.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:a", Tag: "1.0.0"})
	_ = u.SetChangelog(id, "https://ex.com/rel", "notes")
	_ = u.SetStatus(id, "dismissed")
	// Re-detect must not resurrect status to available nor clobber changelog.
	if _, isNew, _ := u.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:a", Tag: "1.0.2"}); isNew {
		t.Fatal("isNew=true for existing row")
	}
	var status, url, text string
	_ = db.QueryRow(`SELECT status, changelog_url, changelog_text FROM updates WHERE id=?`, id).Scan(&status, &url, &text)
	if status != "dismissed" || url != "https://ex.com/rel" || text != "notes" {
		t.Fatalf("clobbered: status=%q url=%q text=%q", status, url, text)
	}
}

func TestUpdatesRecordDriftPreservesVersionsWhenBlank(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	// A network cycle names both ends of a floating-tag ("latest") drift.
	id, _, err := u.RecordDrift(store.Update{
		ServiceID: sid, ToDigest: "sha256:to", FromDigest: "sha256:from",
		FromVersion: "v1.13.0", ToVersion: "v1.14.1", Tag: "latest", Severity: "minor",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A later cache-hit cycle re-detects the SAME to_digest but carries no
	// version info (blank from/to, digest-only severity). It must NOT wipe the
	// names/severity the network cycle computed: versions are a function of the
	// (keyed) to_digest, so a blank incoming means "not computed", not "changed".
	if _, isNew, err := u.RecordDrift(store.Update{
		ServiceID: sid, ToDigest: "sha256:to", FromDigest: "sha256:from",
		Tag: "latest", Severity: "digest-only",
	}); err != nil {
		t.Fatal(err)
	} else if isNew {
		t.Fatal("isNew=true for existing row")
	}
	var fromV, toV, sev string
	_ = db.QueryRow(`SELECT from_version, to_version, severity FROM updates WHERE id=?`, id).Scan(&fromV, &toV, &sev)
	if fromV != "v1.13.0" || toV != "v1.14.1" || sev != "minor" {
		t.Fatalf("clobbered by digest-only cycle: from=%q to=%q sev=%q, want v1.13.0/v1.14.1/minor", fromV, toV, sev)
	}
}

func TestUpdatesGetByID(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, _ := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Status: "available"})
	got, err := u.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServiceID != sid || got.ToDigest != "sha256:x" {
		t.Fatalf("update = %+v", got)
	}
	if _, err := u.Get(9999); !errors.Is(err, store.ErrUpdateNotFound) {
		t.Fatalf("missing Get err = %v, want ErrUpdateNotFound", err)
	}
}

func TestUpdatesSetStatusOnlyTouchesStatus(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, _ := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Status: "available"})
	_ = u.SetChangelog(id, "https://example.com/rel", "notes")
	if err := u.SetStatus(id, "applied"); err != nil {
		t.Fatal(err)
	}
	// SetStatus removes it from ListOpen but must NOT wipe the changelog.
	if open, _ := u.ListOpen(); len(open) != 0 {
		t.Fatalf("ListOpen len = %d, want 0 after applied", len(open))
	}
	var url, text string
	if err := db.QueryRow(`SELECT changelog_url, changelog_text FROM updates WHERE id=?`, id).Scan(&url, &text); err != nil {
		t.Fatal(err)
	}
	if url != "https://example.com/rel" || text != "notes" {
		t.Fatalf("changelog clobbered: url=%q text=%q", url, text)
	}
}

func TestRecordDriftResurrectsFailedAndAppliedButPreservesDismissed(t *testing.T) {
	db := openImagesStore(t)
	updates := store.NewUpdates(db)
	svcID := seedService(t, db)

	up := store.Update{ServiceID: svcID, FromDigest: "sha256:a", ToDigest: "sha256:b", Tag: "latest"}

	for _, tc := range []struct {
		prior string
		want  string
	}{
		{"failed", "available"},    // transient failure → drift re-opens
		{"dismissed", "dismissed"}, // user intent preserved
		{"applied", "available"},   // recreate diverged from applied target → re-opens
	} {
		id, _, err := updates.RecordDrift(up)
		if err != nil {
			t.Fatal(err)
		}
		if err := updates.SetStatus(id, tc.prior); err != nil {
			t.Fatal(err)
		}
		id2, isNew, err := updates.RecordDrift(up)
		if err != nil {
			t.Fatal(err)
		}
		if isNew || id2 != id {
			t.Fatalf("%s: re-detection must hit the same row (isNew=%v)", tc.prior, isNew)
		}
		got, err := updates.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != tc.want {
			t.Errorf("prior=%s: status=%s, want %s", tc.prior, got.Status, tc.want)
		}
	}
}

// TestRecordDriftResurrectsSupersededOnDriftBack covers the real-world scenario
// the failed-resurrection fix must also handle: a tag moves forward (digest A ->
// B, so A is superseded) and later flaps back to A. Re-detecting A must re-open
// it (not leave it invisible) and supersede B. Otherwise the dashboard shows
// "Up to date" while the service is genuinely drifted to A.
func TestRecordDriftResurrectsSupersededOnDriftBack(t *testing.T) {
	db := openImagesStore(t)
	updates := store.NewUpdates(db)
	svcID := seedService(t, db)

	a := store.Update{ServiceID: svcID, FromDigest: "sha256:cur", ToDigest: "sha256:a", Tag: "latest"}
	b := store.Update{ServiceID: svcID, FromDigest: "sha256:cur", ToDigest: "sha256:b", Tag: "latest"}

	idA, _, err := updates.RecordDrift(a)
	if err != nil {
		t.Fatal(err)
	}
	// Tag moves forward to B: A is superseded, B is the open update.
	idB, _, err := updates.RecordDrift(b)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := updates.Get(idA); err != nil {
		t.Fatal(err)
	} else if got.Status != "superseded" {
		t.Fatalf("A should be superseded after B recorded, got %q", got.Status)
	}

	// Registry flaps BACK to A. Re-detection must re-open A's existing row and
	// supersede B.
	idA2, isNew, err := updates.RecordDrift(a)
	if err != nil {
		t.Fatal(err)
	}
	if isNew || idA2 != idA {
		t.Fatalf("drift-back must hit A's existing row (isNew=%v, id=%d want %d)", isNew, idA2, idA)
	}
	open, err := updates.ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ToDigest != "sha256:a" {
		t.Fatalf("after drift-back, open should be exactly [A], got %+v", open)
	}
	if got, err := updates.Get(idB); err != nil {
		t.Fatal(err)
	} else if got.Status != "superseded" {
		t.Fatalf("B should be superseded after drift-back to A, got %q", got.Status)
	}
}

func TestRecordDriftReopensApplied(t *testing.T) {
	db := openImagesStore(t)
	svcID := seedService(t, db)
	u := store.NewUpdates(db)
	// Applied update at digest A.
	id, _, err := u.RecordDrift(store.Update{ServiceID: svcID, FromDigest: "old", ToDigest: "A", Status: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	// Re-detect the SAME target (service diverged back, e.g. recreate): must re-open to available.
	if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, FromDigest: "current", ToDigest: "A"}); err != nil {
		t.Fatal(err)
	}
	got, err := u.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "available" {
		t.Fatalf("applied re-detect status = %q, want available (recreate must re-surface)", got.Status)
	}
}

func TestRecordDriftPreservesRolledBackAndDismissed(t *testing.T) {
	db := openImagesStore(t)
	svcID := seedService(t, db)
	u := store.NewUpdates(db)
	for _, status := range []string{"rolled_back", "dismissed"} {
		id, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "D-" + status, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "D-" + status}); err != nil {
			t.Fatal(err)
		}
		got, err := u.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != status {
			t.Fatalf("%s must be preserved on re-detect, got %q", status, got.Status)
		}
	}
}

func TestMarkRolledBack(t *testing.T) {
	db := openImagesStore(t)
	svcID := seedService(t, db)
	u := store.NewUpdates(db)
	id, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "A", Status: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.MarkRolledBack(svcID, "A"); err != nil {
		t.Fatal(err)
	}
	got, err := u.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "rolled_back" {
		t.Fatalf("status = %q, want rolled_back", got.Status)
	}
	// Only touches an APPLIED row: a call on an available row is a no-op.
	id2, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "B", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.MarkRolledBack(svcID, "B"); err != nil {
		t.Fatal(err)
	}
	got2, err := u.Get(id2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != "available" {
		t.Fatalf("MarkRolledBack must only affect applied rows, got %q", got2.Status)
	}
}

func TestReopenRolledBack(t *testing.T) {
	db := openImagesStore(t)
	svcID := seedService(t, db)
	other := seedServiceNamed(t, db, "other")
	u := store.NewUpdates(db)

	id, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "A", Status: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.MarkRolledBack(svcID, "A"); err != nil {
		t.Fatal(err)
	}
	// Another service's rolled_back row must not be touched.
	oid, _, err := u.RecordDrift(store.Update{ServiceID: other, ToDigest: "A", Status: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.MarkRolledBack(other, "A"); err != nil {
		t.Fatal(err)
	}

	n, err := u.ReopenRolledBack(svcID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reopened %d, want 1", n)
	}
	if got, _ := u.Get(id); got.Status != "available" {
		t.Fatalf("status = %q, want available", got.Status)
	}
	if got, _ := u.Get(oid); got.Status != "rolled_back" {
		t.Fatalf("other service status = %q, want rolled_back (untouched)", got.Status)
	}
}

func TestListVisibleIncludesRolledBack(t *testing.T) {
	db := openImagesStore(t)
	svcID := seedService(t, db)
	u := store.NewUpdates(db)
	// Insert `available` LAST: RecordDrift's supersede tail demotes any earlier
	// available row with a different to_digest, so a first-inserted available
	// would be flipped to superseded by the later inserts.
	if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "di", Status: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "rb", Status: "rolled_back"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "ap", Status: "applied"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := u.RecordDrift(store.Update{ServiceID: svcID, ToDigest: "av", Status: "available"}); err != nil {
		t.Fatal(err)
	}
	list, err := u.ListVisible()
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]bool{}
	for _, up := range list {
		statuses[up.Status] = true
	}
	if !statuses["available"] || !statuses["dismissed"] || !statuses["rolled_back"] {
		t.Fatalf("ListVisible must include available+dismissed+rolled_back, got %v", statuses)
	}
	if statuses["applied"] {
		t.Fatalf("ListVisible must NOT include applied")
	}
}

func TestUpdatesListLastAppliedByService(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// An older applied update, a newer applied update, and a still-open one.
	oldID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "1.0", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	newID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:b", ToDigest: "sha256:c",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.1", ChangelogText: "# 1.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:c", ToDigest: "sha256:d",
		Tag: "1.2", Severity: "minor", Status: "available",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].ID != newID {
		t.Fatalf("got id %d, want newest applied %d (older was %d)", got[0].ID, newID, oldID)
	}
	if got[0].ChangelogText != "# 1.1" || got[0].ChangelogURL != "https://x/1.1" {
		t.Fatalf("changelog not carried: %+v", got[0])
	}
	if got[0].Status != "applied" {
		t.Fatalf("status = %q, want applied", got[0].Status)
	}
}

func TestUpdatesListLastAppliedByServiceExcludesNonApplied(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	for _, st := range []string{"available", "dismissed", "superseded", "rolled_back", "failed"} {
		if _, err := u.Upsert(store.Update{
			ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:" + st,
			Tag: st, Severity: "minor", Status: st,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (%+v)", len(got), got)
	}
}

func TestUpdatesListLastAppliedByServiceMultipleServices(t *testing.T) {
	db := openImagesStore(t)
	u := store.NewUpdates(db)

	webID := seedServiceNamed(t, db, "web")
	cacheID := seedServiceNamed(t, db, "cache")

	// web: an older applied update, a newer applied update (expected), and
	// still-open noise, mirrors TestUpdatesListLastAppliedByService above.
	if _, err := u.Upsert(store.Update{
		ServiceID: webID, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "1.0", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/web-1.0", ChangelogText: "# web 1.0",
	}); err != nil {
		t.Fatal(err)
	}
	webNewID, err := u.Upsert(store.Update{
		ServiceID: webID, FromDigest: "sha256:b", ToDigest: "sha256:c",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/web-1.1", ChangelogText: "# web 1.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Upsert(store.Update{
		ServiceID: webID, FromDigest: "sha256:c", ToDigest: "sha256:d",
		Tag: "1.2", Severity: "minor", Status: "available",
	}); err != nil {
		t.Fatal(err)
	}

	// cache: a single applied update plus dismissed/failed noise, to prove
	// the aggregate doesn't cross-contaminate between services.
	cacheNewID, err := u.Upsert(store.Update{
		ServiceID: cacheID, FromDigest: "sha256:x", ToDigest: "sha256:y",
		Tag: "2.0", Severity: "major", Status: "applied",
		ChangelogURL: "https://x/cache-2.0", ChangelogText: "# cache 2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Upsert(store.Update{
		ServiceID: cacheID, FromDigest: "sha256:y", ToDigest: "sha256:z",
		Tag: "2.1", Severity: "minor", Status: "dismissed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Upsert(store.Update{
		ServiceID: cacheID, FromDigest: "sha256:y", ToDigest: "sha256:z2",
		Tag: "2.1-retry", Severity: "minor", Status: "failed",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (one per service) (%+v)", len(got), got)
	}

	byService := make(map[int64]store.Update, len(got))
	for _, up := range got {
		if _, dup := byService[up.ServiceID]; dup {
			t.Fatalf("service %d appeared more than once in %+v", up.ServiceID, got)
		}
		byService[up.ServiceID] = up
	}

	webGot, ok := byService[webID]
	if !ok {
		t.Fatalf("no row for web service (id %d) in %+v", webID, got)
	}
	if webGot.ID != webNewID {
		t.Fatalf("web: got id %d, want newest applied %d", webGot.ID, webNewID)
	}
	if webGot.ChangelogText != "# web 1.1" {
		t.Fatalf("web: changelog = %q, want newest applied changelog", webGot.ChangelogText)
	}

	cacheGot, ok := byService[cacheID]
	if !ok {
		t.Fatalf("no row for cache service (id %d) in %+v", cacheID, got)
	}
	if cacheGot.ID != cacheNewID {
		t.Fatalf("cache: got id %d, want newest applied %d", cacheGot.ID, cacheNewID)
	}
	if cacheGot.ChangelogText != "# cache 2.0" {
		t.Fatalf("cache: changelog = %q, want newest applied changelog", cacheGot.ChangelogText)
	}
}

func TestUpdatesMarkAppliedSetsStatusAndAppliedAt(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:x", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := u.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.AppliedAt != nil {
		t.Fatalf("AppliedAt = %v before MarkApplied, want nil", got.AppliedAt)
	}

	if err := u.MarkApplied(id); err != nil {
		t.Fatal(err)
	}

	got, err = u.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "applied" {
		t.Fatalf("status = %q, want applied", got.Status)
	}
	if got.AppliedAt == nil {
		t.Fatal("AppliedAt = nil after MarkApplied, want set")
	}
}

// TestUpdatesListLastAppliedByServiceOrdersByApplyTimeNotDetectTime is the
// regression test for [lac-M1]: two updates for one service are both
// status='applied', but the one detected EARLIER was applied LATER (the
// restore-then-reapply scenario from the backlog item: A detected+dismissed,
// B detected+applied, then A is restored and applied after B). The dashboard
// must show A (the one most recently applied), not B (the one with the newer
// detected_at), the old ordering (detected_at DESC) would return B.
//
// B's timestamps are explicit past literals while A is stamped by a REAL
// MarkApplied call: the two are years apart, so the test is deterministic
// regardless of SQLite's 1-second CURRENT_TIMESTAMP granularity, and it also
// pins the invariant the COALESCE ordering rests on, that MarkApplied's
// CURRENT_TIMESTAMP is stored in the same text format as detected_at. Writing
// both rows by literal would let a MarkApplied rewritten to bind a Go
// time.Time (RFC3339, with a 'T') pass while mixed-format rows sort wrong.
func TestUpdatesListLastAppliedByServiceOrdersByApplyTimeNotDetectTime(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// updateA: detected first (older), but applied LAST (newest applied_at).
	updateA, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:A",
		Tag: "1.0", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/A", ChangelogText: "# A",
	})
	if err != nil {
		t.Fatal(err)
	}
	// updateB: detected second (newer), but applied FIRST (older applied_at).
	updateB, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:b", ToDigest: "sha256:B",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/B", ChangelogText: "# B",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(
		`UPDATE updates SET detected_at='2024-01-01 00:00:00' WHERE id=?`, updateA,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE updates SET detected_at='2024-01-02 00:00:00', applied_at='2024-01-01 12:00:00' WHERE id=?`,
		updateB,
	); err != nil {
		t.Fatal(err)
	}
	// A is applied for real, now, years after B's literal applied_at.
	if err := u.MarkApplied(updateA); err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].ID != updateA {
		t.Fatalf("got id %d, want %d (applied later, despite older detected_at); B (id %d) has the newer detected_at but older applied_at", got[0].ID, updateA, updateB)
	}
	if got[0].ChangelogText != "# A" {
		t.Fatalf("changelog = %q, want # A", got[0].ChangelogText)
	}
}

// TestUpdatesListLastAppliedByServiceLegacyRowFallsBackToDetectedAt covers a
// row applied before migration 0007 introduced applied_at (so applied_at is
// NULL) competing against a row with an explicit applied_at. The legacy row
// must still participate in the ordering via COALESCE(applied_at, detected_at).
// It is neither skipped nor always-loses/always-wins, it competes on its
// detected_at.
func TestUpdatesListLastAppliedByServiceLegacyRowFallsBackToDetectedAt(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// legacyUpdate: applied_at NULL (as any row applied via the old SetStatus
	// path, pre-migration, would be); its detected_at is the newest timestamp
	// in this test, so COALESCE must fall back to it and let it win.
	legacyUpdate, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:legacy",
		Tag: "1.0", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/legacy", ChangelogText: "# legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE updates SET detected_at='2024-06-01 00:00:00' WHERE id=?`,
		legacyUpdate,
	); err != nil {
		t.Fatal(err)
	}

	// modernUpdate: has an explicit applied_at, but earlier than legacyUpdate's
	// fallback detected_at, so it must lose.
	modernUpdate, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:b", ToDigest: "sha256:modern",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/modern", ChangelogText: "# modern",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE updates SET detected_at='2024-01-01 00:00:00', applied_at='2024-01-02 00:00:00' WHERE id=?`,
		modernUpdate,
	); err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].ID != legacyUpdate {
		t.Fatalf("got id %d, want legacy update %d (its detected_at fallback beats modern's applied_at)", got[0].ID, legacyUpdate)
	}
	if got[0].AppliedAt != nil {
		t.Fatalf("AppliedAt = %v, want nil for a legacy (pre-migration) row", got[0].AppliedAt)
	}
	if got[0].ChangelogText != "# legacy" {
		t.Fatalf("changelog = %q, want # legacy", got[0].ChangelogText)
	}
}

func TestUpdatesSetChangelogStatus(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelogStatus(id, "rate_limited"); err != nil {
		t.Fatal(err)
	}
	open, err := u.ListOpen()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ChangelogStatus != "rate_limited" {
		t.Fatalf("ChangelogStatus = %q (rows=%d), want rate_limited", func() string {
			if len(open) == 0 {
				return ""
			}
			return open[0].ChangelogStatus
		}(), len(open))
	}
}

func TestUpdatesSetChangelogClearsStatus(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	id, err := u.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelogStatus(id, "rate_limited"); err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelog(id, "https://example.com/notes", "notes body"); err != nil {
		t.Fatal(err)
	}
	got, err := u.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChangelogStatus != "" {
		t.Fatalf("ChangelogStatus = %q, want cleared", got.ChangelogStatus)
	}
	if got.ChangelogText != "notes body" || got.ChangelogURL != "https://example.com/notes" {
		t.Fatalf("changelog content = (%q,%q)", got.ChangelogURL, got.ChangelogText)
	}
}

func TestListLastAppliedPrefersAppliedOverCurrent(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// A synthetic current-version row (from == to), then a real applied update.
	if _, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:cur",
		FromVersion: "1.0", ToVersion: "1.0", Tag: "1.0", Severity: "current", Status: "current",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0 current",
	}); err != nil {
		t.Fatal(err)
	}
	appliedID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:new",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.1", ChangelogText: "# 1.1 applied",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].ID != appliedID {
		t.Fatalf("got id %d, want applied %d (current must not win)", got[0].ID, appliedID)
	}
}

// TestListLastAppliedTieBreakIgnoresIDAndTimestamp isolates the status sort key
// from the id/timestamp fallback: the applied row is inserted FIRST, then the
// current row SECOND, so the current row has the higher id and an equal-or-later
// detected_at. If applied still wins, it can only be the primary
// ORDER BY (status='current') key doing it, not the id/timestamp tie-break. This
// is the reversed-insert counterpart to TestListLastAppliedPrefersAppliedOverCurrent
// (where applied was also the newest row, so that test alone could not tell the
// two orderings apart).
func TestListLastAppliedTieBreakIgnoresIDAndTimestamp(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	// Applied first (lower id), current second (higher id, equal-or-later ts).
	appliedID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:new",
		Tag: "1.1", Severity: "minor", Status: "applied",
		ChangelogURL: "https://x/1.1", ChangelogText: "# 1.1 applied",
	})
	if err != nil {
		t.Fatal(err)
	}
	curID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:cur",
		FromVersion: "1.0", ToVersion: "1.0", Tag: "1.0", Severity: "current", Status: "current",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0 current",
	})
	if err != nil {
		t.Fatal(err)
	}
	if curID <= appliedID {
		t.Fatalf("test precondition broken: current id %d should exceed applied id %d", curID, appliedID)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != appliedID {
		t.Fatalf("got %+v, want applied id %d to win over newer current id %d (status key must dominate id/timestamp)", got, appliedID, curID)
	}
}

func TestListLastAppliedReturnsCurrentWhenOnly(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)

	curID, err := u.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:cur", ToDigest: "sha256:cur",
		FromVersion: "1.0", ToVersion: "1.0", Tag: "1.0", Severity: "current", Status: "current",
		ChangelogURL: "https://x/1.0", ChangelogText: "# 1.0 current",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := u.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != curID {
		t.Fatalf("got %+v, want single current row id %d", got, curID)
	}
	if got[0].Status != "current" || got[0].ChangelogText != "# 1.0 current" {
		t.Fatalf("current row not carried faithfully: %+v", got[0])
	}
}
