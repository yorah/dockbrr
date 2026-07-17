package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func openTempStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestProjectsUpsertInsertsAndLists(t *testing.T) {
	p := store.NewProjects(openTempStore(t))
	now := time.Now().UTC().Truncate(time.Second)
	id, err := p.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web",
		WorkingDir: "/srv/web", ConfigFiles: []string{"docker-compose.yml"},
		Source: "discovered", LastSyncedAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
	got, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Name != "web" || got[0].Kind != "compose" || got[0].WorkingDir != "/srv/web" {
		t.Fatalf("row = %+v", got[0])
	}
	if len(got[0].ConfigFiles) != 1 || got[0].ConfigFiles[0] != "docker-compose.yml" {
		t.Fatalf("config_files = %v", got[0].ConfigFiles)
	}
	if got[0].LastSyncedAt == nil || !got[0].LastSyncedAt.Equal(now) {
		t.Fatalf("last_synced_at = %v, want %v", got[0].LastSyncedAt, now)
	}
}

func TestProjectsUpsertByNaturalKeyPreservesUserColumns(t *testing.T) {
	db := openTempStore(t)
	p := store.NewProjects(db)
	id1, err := p.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "web", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	// User enables auto-update out of band.
	if _, err := db.Exec(`UPDATE projects SET auto_update_enabled=1 WHERE id=?`, id1); err != nil {
		t.Fatal(err)
	}
	// Discovery re-upserts the same (host_id, name) with discovery defaults.
	id2, err := p.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "web", WorkingDir: "/new", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("upsert produced new id %d, want %d", id2, id1)
	}
	got, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].WorkingDir != "/new" {
		t.Fatalf("working_dir not updated: %q", got[0].WorkingDir)
	}
	if !got[0].AutoUpdateEnabled {
		t.Fatal("auto_update_enabled was clobbered by discovery upsert")
	}
	if got[0].Source != "manual" {
		t.Fatalf("source clobbered: %q, want manual", got[0].Source)
	}
}

func TestProjectsUpsertInsertsAutoUpdateEnabled(t *testing.T) {
	db := openTempStore(t)
	p := store.NewProjects(db)

	id, err := p.Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web", Source: "discovered",
		AutoUpdateEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoUpdateEnabled {
		t.Fatal("auto_update_enabled = false, want true (INSERT must persist pr.AutoUpdateEnabled)")
	}
}

func TestProjectsGet(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web-stack", Source: "discovered",
		WorkingDir: "/srv/stacks/web", ConfigFiles: []string{"/srv/stacks/web/compose.yml"},
	})
	got, err := store.NewProjects(db).Get(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web-stack" || got.WorkingDir != "/srv/stacks/web" {
		t.Fatalf("project = %+v", got)
	}
	if len(got.ConfigFiles) != 1 || got.ConfigFiles[0] != "/srv/stacks/web/compose.yml" {
		t.Fatalf("config files = %v", got.ConfigFiles)
	}
}

func TestProjectsGetMissingReturnsSentinel(t *testing.T) {
	db := openTempStore(t)
	if _, err := store.NewProjects(db).Get(999); !errors.Is(err, store.ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestProjectsSetAutoUpdate(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err := store.NewProjects(db).SetAutoUpdate(pid, true); err != nil {
		t.Fatal(err)
	}
	got, _ := store.NewProjects(db).Get(pid)
	if !got.AutoUpdateEnabled {
		t.Fatal("auto_update_enabled not set true")
	}
	_ = store.NewProjects(db).SetAutoUpdate(pid, false)
	got, _ = store.NewProjects(db).Get(pid)
	if got.AutoUpdateEnabled {
		t.Fatal("auto_update_enabled not cleared")
	}
}

func TestProjectsSetUnmanaged(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})

	got, err := store.NewProjects(db).Get(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Unmanaged {
		t.Fatal("unmanaged should default to false")
	}

	if err := store.NewProjects(db).SetUnmanaged(pid, true); err != nil {
		t.Fatal(err)
	}
	got, err = store.NewProjects(db).Get(pid)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Unmanaged {
		t.Fatal("unmanaged not set true")
	}

	if err := store.NewProjects(db).SetUnmanaged(pid, false); err != nil {
		t.Fatal(err)
	}
	got, err = store.NewProjects(db).Get(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Unmanaged {
		t.Fatal("unmanaged not cleared")
	}
}

func TestProjectsDelete(t *testing.T) {
	db := openTempStore(t)
	p := store.NewProjects(db)
	pid, err := p.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(pid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(pid); !errors.Is(err, store.ErrProjectNotFound) {
		t.Fatalf("project should be gone after Delete, err=%v", err)
	}
}

func TestEffectiveAutoUpdateMatrix(t *testing.T) {
	b := func(v bool) *bool { return &v }
	for _, tc := range []struct {
		name   string
		proj   bool
		svc    *bool
		pinned bool
		want   bool
	}{
		{"proj-on svc-inherit", true, nil, false, true},        // THE FIX: inherit follows project
		{"proj-on svc-on", true, b(true), false, true},
		{"proj-on svc-off", true, b(false), false, false},      // explicit veto wins
		{"proj-off svc-inherit", false, nil, false, false},     // default-safe
		{"proj-off svc-on", false, b(true), false, false},      // both must opt in
		{"proj-on svc-on pinned", true, b(true), true, false},
		{"proj-on svc-inherit pinned", true, nil, true, false},
	} {
		got := store.EffectiveAutoUpdate(
			store.Project{AutoUpdateEnabled: tc.proj},
			store.Service{AutoUpdateEnabled: tc.svc, Pinned: tc.pinned},
		)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEffectiveAutoUpdateGenuinePinVetoes(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	// User pinned a digest in the file: pinned, NOT drifted -> vetoed.
	s := store.Service{Pinned: true, Drifted: false}
	if store.EffectiveAutoUpdate(p, s) {
		t.Error("genuine digest pin (pinned && !drifted) must veto auto-update")
	}
}

func TestEffectiveAutoUpdateFallbackPinAllows(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	// Fallback runtime-pin (file still a tag): pinned AND drifted -> allowed.
	s := store.Service{Pinned: true, Drifted: true}
	if !store.EffectiveAutoUpdate(p, s) {
		t.Error("fallback pin (pinned && drifted) must NOT veto auto-update")
	}
}

func TestEffectiveAutoUpdateUnpinnedAllows(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	if !store.EffectiveAutoUpdate(p, store.Service{Pinned: false, Drifted: false}) {
		t.Error("unpinned service must allow auto-update")
	}
}

func TestEffectiveAutoUpdateProjectOffVetoes(t *testing.T) {
	if store.EffectiveAutoUpdate(store.Project{AutoUpdateEnabled: false}, store.Service{}) {
		t.Error("project auto-update off must veto")
	}
}

func TestEffectiveAutoUpdateServiceVetoOverride(t *testing.T) {
	p := store.Project{AutoUpdateEnabled: true}
	no := false
	if store.EffectiveAutoUpdate(p, store.Service{AutoUpdateEnabled: &no}) {
		t.Error("explicit per-service false must veto")
	}
}

func TestProjectsSetAutoNamed(t *testing.T) {
	db := openTempStore(t)
	p := store.NewProjects(db)
	id, err := p.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	// Default is false straight after insert.
	got, err := p.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutoNamed {
		t.Fatal("AutoNamed = true on fresh insert, want false")
	}
	// SetAutoNamed(true) is reflected by Get and List.
	if err := p.SetAutoNamed(id, true); err != nil {
		t.Fatal(err)
	}
	got, err = p.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoNamed {
		t.Fatal("AutoNamed = false after SetAutoNamed(true), want true")
	}
	all, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].AutoNamed {
		t.Fatalf("List AutoNamed = %+v, want one row with AutoNamed=true", all)
	}
}
