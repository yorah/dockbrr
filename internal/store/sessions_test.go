package store_test

import (
	"errors"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func TestMigration0002CreatesSessions(t *testing.T) {
	db := openTempStore(t)
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='sessions'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("sessions table missing: %v", err)
	}
}

func seedUser(t *testing.T, db *store.DB) int64 {
	t.Helper()
	id, err := store.NewUsers(db).Create("admin", "$argon2id$hash")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSessionsCreateGet(t *testing.T) {
	db := openTempStore(t)
	uid := seedUser(t, db)
	sessions := store.NewSessions(db)
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	if err := sessions.Create("hash1", uid, "csrf1", exp); err != nil {
		t.Fatal(err)
	}
	got, err := sessions.Get("hash1", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != uid || got.CSRFToken != "csrf1" {
		t.Fatalf("session = %+v", got)
	}
}

func TestSessionsGetMissingReturnsSentinel(t *testing.T) {
	db := openTempStore(t)
	if _, err := store.NewSessions(db).Get("nope", time.Now()); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionsExpiredTreatedAsAbsent(t *testing.T) {
	db := openTempStore(t)
	uid := seedUser(t, db)
	sessions := store.NewSessions(db)
	past := time.Now().UTC().Add(-time.Minute)
	if err := sessions.Create("hash1", uid, "csrf1", past); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Get("hash1", time.Now().UTC()); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("expired session Get err = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionsDelete(t *testing.T) {
	db := openTempStore(t)
	uid := seedUser(t, db)
	sessions := store.NewSessions(db)
	now := time.Now().UTC()
	_ = sessions.Create("hash1", uid, "csrf1", now.Add(time.Hour))
	if err := sessions.Delete("hash1"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Get("hash1", now); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatal("session present after Delete")
	}
}

func TestSessionsDeleteByUserRevokesAll(t *testing.T) {
	db := openTempStore(t)
	uid := seedUser(t, db)
	sessions := store.NewSessions(db)
	now := time.Now().UTC()
	_ = sessions.Create("h1", uid, "c1", now.Add(time.Hour))
	_ = sessions.Create("h2", uid, "c2", now.Add(time.Hour))
	if err := sessions.DeleteByUser(uid); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Get("h1", now); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatal("h1 present after DeleteByUser")
	}
	if _, err := sessions.Get("h2", now); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatal("h2 present after DeleteByUser")
	}
}

func TestSessionsDeleteExpired(t *testing.T) {
	db := openTempStore(t)
	uid := seedUser(t, db)
	sessions := store.NewSessions(db)
	now := time.Now().UTC()
	_ = sessions.Create("live", uid, "c", now.Add(time.Hour))
	_ = sessions.Create("dead", uid, "c", now.Add(-time.Hour))
	n, err := sessions.DeleteExpired(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteExpired removed %d, want 1", n)
	}
}
