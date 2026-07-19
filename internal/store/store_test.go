package store_test

import (
	"path/filepath"
	"testing"

	"dockbrr/internal/store"
)

func openTemp(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenCreatesSchema(t *testing.T) {
	db := openTemp(t)
	wantTables := []string{
		"hosts", "projects", "services", "images", "image_remote_state",
		"updates", "jobs", "job_logs", "state_snapshots", "service_events",
		"registry_credentials", "settings", "users",
	}
	for _, name := range wantTables {
		var got string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&got)
		if err != nil {
			t.Fatalf("table %q missing: %v", name, err)
		}
	}
}

func TestSeedsLocalHost(t *testing.T) {
	db := openTemp(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE name='local'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("local host rows = %d, want 1", n)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	db := openTemp(t)
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")
	db1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db1.Close()
	db2, err := store.Open(path) // re-open must not re-apply / error
	if err != nil {
		t.Fatalf("second open failed: %v", err)
	}
	defer func() { _ = db2.Close() }()
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM hosts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("hosts after reopen = %d, want 1 (migration ran twice?)", n)
	}
}
