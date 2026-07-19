package store_test

import (
	"path/filepath"
	"testing"

	"dockbrr/internal/store"
)

func newTagDigestsDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestTagDigestsGetMissing(t *testing.T) {
	td := store.NewTagDigests(newTagDigestsDB(t))
	dg, ok, err := td.Get("repo", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if ok || dg != "" {
		t.Fatalf("missing lookup = (%q, %v), want (\"\", false)", dg, ok)
	}
}

func TestTagDigestsPutGet(t *testing.T) {
	td := store.NewTagDigests(newTagDigestsDB(t))
	if err := td.Put("ghcr.io/acme/web", "v1.2.3", "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	dg, ok, err := td.Get("ghcr.io/acme/web", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || dg != "sha256:abc" {
		t.Fatalf("get = (%q, %v), want (sha256:abc, true)", dg, ok)
	}
}

func TestTagDigestsPutEmptyIgnored(t *testing.T) {
	td := store.NewTagDigests(newTagDigestsDB(t))
	if err := td.Put("repo", "v1.0.0", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := td.Get("repo", "v1.0.0"); ok {
		t.Fatal("empty digest must not be cached")
	}
}

func TestTagDigestsPutIdempotent(t *testing.T) {
	td := store.NewTagDigests(newTagDigestsDB(t))
	for i := 0; i < 3; i++ {
		if err := td.Put("repo", "v1.0.0", "sha256:same"); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	dg, ok, _ := td.Get("repo", "v1.0.0")
	if !ok || dg != "sha256:same" {
		t.Fatalf("get = (%q, %v), want (sha256:same, true)", dg, ok)
	}
}
