package store_test

import (
	"errors"
	"testing"

	"dockbrr/internal/store"
)

func TestSnapshotsInsertAndGetLatest(t *testing.T) {
	db := openImagesStore(t)
	_, sid := seedProjectService(t, db)
	jobs := store.NewJobs(db)
	pid2 := int64(1)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid2, ServiceID: &sid})
	snaps := store.NewSnapshots(db)

	id1, err := snaps.Insert(store.Snapshot{
		ServiceID: sid, JobID: &jid, PrevRepo: "ghcr.io/acme/web",
		PrevDigest: "sha256:old", PrevImageID: "sha256:img1",
		PrevContainerInspect: `{"Id":"c1"}`, ComposeFileHash: "hash1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 <= 0 {
		t.Fatalf("id = %d", id1)
	}
	// A newer snapshot for the same service.
	if _, err := snaps.Insert(store.Snapshot{ServiceID: sid, PrevDigest: "sha256:old2", ComposeFileHash: "hash2"}); err != nil {
		t.Fatal(err)
	}
	got, err := snaps.GetLatestForService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrevDigest != "sha256:old2" {
		t.Fatalf("latest prev_digest = %q, want sha256:old2", got.PrevDigest)
	}
}

func TestSnapshotsGetLatestMissingReturnsSentinel(t *testing.T) {
	db := openImagesStore(t)
	_, sid := seedProjectService(t, db)
	if _, err := store.NewSnapshots(db).GetLatestForService(sid); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Fatalf("err = %v, want ErrSnapshotNotFound", err)
	}
}

func TestSnapshotsPreservesContainerInspect(t *testing.T) {
	db := openImagesStore(t)
	_, sid := seedProjectService(t, db)
	snaps := store.NewSnapshots(db)
	_, err := snaps.Insert(store.Snapshot{ServiceID: sid, PrevContainerInspect: `{"State":{"Status":"running"}}`})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := snaps.GetLatestForService(sid)
	if got.PrevContainerInspect != `{"State":{"Status":"running"}}` {
		t.Fatalf("container inspect = %q", got.PrevContainerInspect)
	}
}
