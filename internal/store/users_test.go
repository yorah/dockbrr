package store_test

import (
	"errors"
	"testing"

	"dockbrr/internal/store"
)

func TestUsersCountCreateGet(t *testing.T) {
	db := openTempStore(t)
	users := store.NewUsers(db)

	n, err := users.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("initial user count = %d, want 0", n)
	}

	id, err := users.Create("admin", "$argon2id$hash")
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}

	if n, _ := users.Count(); n != 1 {
		t.Fatalf("user count after create = %d, want 1", n)
	}

	got, err := users.GetByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "admin" || got.PasswordHash != "$argon2id$hash" {
		t.Fatalf("user = %+v", got)
	}
}

func TestUsersGetMissingReturnsSentinel(t *testing.T) {
	db := openTempStore(t)
	if _, err := store.NewUsers(db).GetByUsername("nope"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestUsersGetByID(t *testing.T) {
	db := openTempStore(t)
	users := store.NewUsers(db)
	id, err := users.Create("admin", "$argon2id$hash")
	if err != nil {
		t.Fatal(err)
	}
	got, err := users.GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "admin" {
		t.Fatalf("user = %+v", got)
	}
	if _, err := users.GetByID(999); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestUsersUsernameIsUnique(t *testing.T) {
	db := openTempStore(t)
	users := store.NewUsers(db)
	if _, err := users.Create("admin", "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create("admin", "h2"); err == nil {
		t.Fatal("expected UNIQUE(username) violation on duplicate create")
	}
}

func TestUsersUpdatePasswordHash(t *testing.T) {
	db := openTempStore(t)
	users := store.NewUsers(db)
	id, err := users.Create("admin", "$argon2id$old")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.UpdatePasswordHash(id, "$argon2id$new"); err != nil {
		t.Fatal(err)
	}
	got, err := users.GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != "$argon2id$new" {
		t.Fatalf("hash = %q, want %q", got.PasswordHash, "$argon2id$new")
	}
}
