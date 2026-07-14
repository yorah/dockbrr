package store_test

import (
	"path/filepath"
	"testing"

	"dockbrr/internal/secret"
	"dockbrr/internal/store"
)

func newCreds(t *testing.T) (*store.Credentials, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, _ := secret.LoadOrCreateKey(t.TempDir())
	sealer, _ := secret.NewSealer(key)
	return store.NewCredentials(db, sealer), db
}

func TestCredentialsUpsertAndLookup(t *testing.T) {
	creds, _ := newCreds(t)
	if _, err := creds.Upsert("ghcr.io", "alice", "s3cret"); err != nil {
		t.Fatal(err)
	}
	user, pass, ok := creds.Lookup("ghcr.io")
	if !ok {
		t.Fatal("Lookup ok=false for a stored host")
	}
	if user != "alice" || pass != "s3cret" {
		t.Fatalf("lookup = %q/%q, want alice/s3cret", user, pass)
	}
}

func TestCredentialsSecretSealedAtRest(t *testing.T) {
	creds, db := newCreds(t)
	_, _ = creds.Upsert("ghcr.io", "alice", "s3cret")
	var stored string
	if err := db.QueryRow(`SELECT secret FROM registry_credentials WHERE registry_host='ghcr.io'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "s3cret" || stored == "" {
		t.Fatalf("secret stored in plaintext or empty: %q", stored)
	}
}

func TestCredentialsUpsertOverwrites(t *testing.T) {
	creds, _ := newCreds(t)
	_, _ = creds.Upsert("ghcr.io", "alice", "old")
	_, _ = creds.Upsert("ghcr.io", "alice", "new")
	_, pass, _ := creds.Lookup("ghcr.io")
	if pass != "new" {
		t.Fatalf("pass = %q, want new", pass)
	}
	list, _ := creds.List()
	if len(list) != 1 {
		t.Fatalf("credential rows = %d, want 1 (upsert not insert)", len(list))
	}
}

func TestCredentialsListHasNoSecret(t *testing.T) {
	creds, _ := newCreds(t)
	_, _ = creds.Upsert("ghcr.io", "alice", "s3cret")
	list, err := creds.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RegistryHost != "ghcr.io" || list[0].Username != "alice" {
		t.Fatalf("list = %+v", list)
	}
}

func TestCredentialsDelete(t *testing.T) {
	creds, _ := newCreds(t)
	_, _ = creds.Upsert("ghcr.io", "alice", "s3cret")
	if err := creds.Delete("ghcr.io"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := creds.Lookup("ghcr.io"); ok {
		t.Fatal("Lookup ok=true after delete")
	}
}

func TestCredentialsLookupMissIsAnonymous(t *testing.T) {
	creds, _ := newCreds(t)
	if _, _, ok := creds.Lookup("no.such.host"); ok {
		t.Fatal("Lookup ok=true for an unknown host")
	}
}
