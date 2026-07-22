package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func openImagesStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "img.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestImagesUpsertInsertsAndGets(t *testing.T) {
	im := store.NewImages(openImagesStore(t))
	built := time.Now().UTC().Truncate(time.Second)
	id, err := im.Upsert(store.Image{
		Repo: "ghcr.io/acme/web", Tag: "1.2.3", Digest: "sha256:aaa",
		MediaType: "application/vnd.oci.image.index.v1+json",
		OS:        "linux", Arch: "amd64", Size: 123, BuiltAt: &built,
		Labels:    `{"org.opencontainers.image.version":"1.2.3"}`,
		SourceURL: "https://github.com/acme/web", Revision: "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
	got, err := im.GetByDigest("ghcr.io/acme/web", "sha256:aaa")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "1.2.3" || got.OS != "linux" || got.Arch != "amd64" {
		t.Fatalf("row = %+v", got)
	}
	if got.SourceURL != "https://github.com/acme/web" {
		t.Fatalf("source_url = %q", got.SourceURL)
	}
	if got.BuiltAt == nil || !got.BuiltAt.Equal(built) {
		t.Fatalf("BuiltAt = %v, want %v", got.BuiltAt, built)
	}
}

func TestImagesUpsertByDigestUpdatesMutableColumns(t *testing.T) {
	im := store.NewImages(openImagesStore(t))
	id1, err := im.Upsert(store.Image{Repo: "r", Digest: "sha256:x", Tag: "old"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := im.Upsert(store.Image{Repo: "r", Digest: "sha256:x", Tag: "new", Revision: "rev2"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("upsert produced new id %d, want %d", id2, id1)
	}
	got, err := im.GetByDigest("r", "sha256:x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "new" || got.Revision != "rev2" {
		t.Fatalf("mutable columns not updated: %+v", got)
	}
}

func TestImagesGetByDigestMissingReturnsSentinel(t *testing.T) {
	im := store.NewImages(openImagesStore(t))
	_, err := im.GetByDigest("r", "sha256:nope")
	if !errors.Is(err, store.ErrImageNotFound) {
		t.Fatalf("err = %v, want ErrImageNotFound", err)
	}
}

func TestRemoteStatesUpsertAndGet(t *testing.T) {
	rs := store.NewRemoteStates(openImagesStore(t))
	now := time.Now().UTC().Truncate(time.Second)
	if err := rs.Upsert(store.RemoteState{
		Repo: "r", Tag: "latest", RemoteDigest: "sha256:bbb",
		ResolvedAt: &now, ManifestLabels: `{"k":"v"}`, Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := rs.Get("r", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteDigest != "sha256:bbb" || got.Status != "ok" {
		t.Fatalf("row = %+v", got)
	}
	if got.ResolvedAt == nil || !got.ResolvedAt.Equal(now) {
		t.Fatalf("resolved_at = %v, want %v", got.ResolvedAt, now)
	}
}

func TestRemoteStatesUpsertReplacesStatus(t *testing.T) {
	rs := store.NewRemoteStates(openImagesStore(t))
	_ = rs.Upsert(store.RemoteState{Repo: "r", Tag: "t", RemoteDigest: "sha256:a", Status: "ok"})
	if err := rs.Upsert(store.RemoteState{Repo: "r", Tag: "t", Status: "rate_limited"}); err != nil {
		t.Fatal(err)
	}
	got, _ := rs.Get("r", "t")
	if got.Status != "rate_limited" {
		t.Fatalf("status = %q, want rate_limited", got.Status)
	}
	if got.RemoteDigest != "" {
		t.Fatalf("remote_digest = %q, want empty (replaced)", got.RemoteDigest)
	}
}

func TestRemoteStatesGetMissingReturnsSentinel(t *testing.T) {
	rs := store.NewRemoteStates(openImagesStore(t))
	_, err := rs.Get("r", "nope")
	if !errors.Is(err, store.ErrRemoteStateNotFound) {
		t.Fatalf("err = %v, want ErrRemoteStateNotFound", err)
	}
}

func TestRemoteStatesAll(t *testing.T) {
	rs := store.NewRemoteStates(openImagesStore(t))
	now := time.Now().UTC()
	if err := rs.Upsert(store.RemoteState{Repo: "nginx", Tag: "1.27", RemoteDigest: "sha256:x", Status: "ok", ResolvedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := rs.Upsert(store.RemoteState{Repo: "redis", Tag: "7", Status: "rate_limited", ResolvedAt: &now}); err != nil {
		t.Fatal(err)
	}
	all, err := rs.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2, got %d", len(all))
	}
	if all[[2]string{"redis", "7"}].Status != "rate_limited" {
		t.Errorf("missing redis state: %+v", all)
	}
	if all[[2]string{"nginx", "1.27"}].Status != "ok" {
		t.Errorf("missing nginx state: %+v", all)
	}
}

func TestSetResolvedVersionMarksVersionResolved(t *testing.T) {
	db := openImagesStore(t)
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
