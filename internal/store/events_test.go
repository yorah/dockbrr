package store_test

import (
	"testing"

	"dockbrr/internal/store"
)

func TestEventsInsertAndListByService(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	e := store.NewEvents(db)
	id, err := e.Insert(store.Event{
		ServiceID: sid, Kind: "detected",
		FromDigest: "sha256:old", ToDigest: "sha256:new",
		Message: "update available",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id = %d", id)
	}
	got, err := e.ListByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Kind != "detected" || got[0].ToDigest != "sha256:new" {
		t.Fatalf("row = %+v", got[0])
	}
}

func TestEventsListByServiceIncludesMatchingUpdateChangelog(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	u := store.NewUpdates(db)
	e := store.NewEvents(db)

	id, err := u.Upsert(store.Update{
		ServiceID: sid, ToDigest: "sha256:d", Status: "applied",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SetChangelog(id, "https://example.com/rel", "## notes"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Insert(store.Event{
		ServiceID: sid, Kind: "succeeded", ToDigest: "sha256:d", Message: "applied",
	}); err != nil {
		t.Fatal(err)
	}
	// An event whose to_digest matches no update must still be returned, with
	// empty changelog fields (LEFT JOIN, not INNER JOIN).
	if _, err := e.Insert(store.Event{
		ServiceID: sid, Kind: "detected", ToDigest: "sha256:unmatched",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := e.ListByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Newest first: "detected" (unmatched) then "succeeded" (matched).
	if got[0].Kind != "detected" || got[0].ChangelogURL != "" || got[0].ChangelogText != "" {
		t.Fatalf("unmatched row = %+v, want empty changelog fields", got[0])
	}
	if got[1].Kind != "succeeded" {
		t.Fatalf("second row kind = %q, want succeeded", got[1].Kind)
	}
	if got[1].ChangelogURL != "https://example.com/rel" || got[1].ChangelogText != "## notes" {
		t.Fatalf("matched row changelog = %+v, want populated", got[1])
	}
}

func TestEventsListByServiceNewestFirst(t *testing.T) {
	db := openImagesStore(t)
	sid := seedService(t, db)
	e := store.NewEvents(db)
	if _, err := e.Insert(store.Event{ServiceID: sid, Kind: "detected"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Insert(store.Event{ServiceID: sid, Kind: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	got, err := e.ListByService(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != "dismissed" {
		t.Fatalf("expected newest-first [dismissed, detected], got %+v", got)
	}
}
