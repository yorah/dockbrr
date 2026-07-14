package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func openChangelogReposStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "changelog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestChangelogReposPutGet(t *testing.T) {
	db := openChangelogReposStore(t)
	repos := store.NewChangelogRepos(db)

	// Unknown repo -> not found.
	if _, _, _, found, err := repos.Get("library/nginx", time.Hour); err != nil || found {
		t.Fatalf("unknown repo: found=%v err=%v, want found=false", found, err)
	}

	// Positive resolution.
	if err := repos.Put("library/nginx", "nginx", "nginx"); err != nil {
		t.Fatal(err)
	}
	owner, name, positive, found, err := repos.Get("library/nginx", time.Hour)
	if err != nil || !found || !positive || owner != "nginx" || name != "nginx" {
		t.Fatalf("positive get = %q/%q positive=%v found=%v err=%v", owner, name, positive, found, err)
	}

	// Negative resolution (owner="").
	if err := repos.Put("library/void", "", ""); err != nil {
		t.Fatal(err)
	}
	_, _, positive, found, err = repos.Get("library/void", time.Hour)
	if err != nil || !found || positive {
		t.Fatalf("negative get = positive=%v found=%v err=%v, want found=true positive=false", positive, found, err)
	}

	// Expired row -> not found (ttl of 0 makes any row stale).
	if _, _, _, found, err := repos.Get("library/nginx", 0); err != nil || found {
		t.Fatalf("expired get: found=%v err=%v, want found=false", found, err)
	}
}
