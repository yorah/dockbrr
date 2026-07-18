package store_test

import (
	"errors"
	"testing"
	"time"

	"dockbrr/internal/store"
)

func seedProject(t *testing.T, db *store.DB) int64 {
	t.Helper()
	id, err := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "proj", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServicesUpsertAndListByProject(t *testing.T) {
	db := openTempStore(t)
	pid := seedProject(t, db)
	s := store.NewServices(db)
	id, err := s.Upsert(store.Service{
		ProjectID: pid, Name: "app", ContainerIDs: []string{"abc"},
		ImageRef: "nginx:latest", CurrentDigest: "sha256:aaa",
		CurrentImageID: "sha256:img", Pinned: false, State: "running", Healthcheck: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 0 {
		t.Fatalf("id = %d", id)
	}
	got, err := s.ListByProject(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	sv := got[0]
	if sv.Name != "app" || sv.ImageRef != "nginx:latest" || sv.CurrentDigest != "sha256:aaa" {
		t.Fatalf("row = %+v", sv)
	}
	if len(sv.ContainerIDs) != 1 || sv.ContainerIDs[0] != "abc" {
		t.Fatalf("container_ids = %v", sv.ContainerIDs)
	}
	if !sv.Healthcheck {
		t.Fatal("healthcheck = false, want true")
	}
	if sv.AutoUpdateEnabled != nil {
		t.Fatalf("auto_update_enabled = %v, want nil", *sv.AutoUpdateEnabled)
	}
}

func TestServicesUpsertPreservesAutoUpdateOverride(t *testing.T) {
	db := openTempStore(t)
	pid := seedProject(t, db)
	s := store.NewServices(db)
	id, err := s.Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE services SET auto_update_enabled=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	id2, err := s.Upsert(store.Service{ProjectID: pid, Name: "app", State: "exited"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("new id %d, want %d", id2, id)
	}
	got, err := s.ListByProject(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != "exited" {
		t.Fatalf("state not updated: %q", got[0].State)
	}
	if got[0].AutoUpdateEnabled == nil || !*got[0].AutoUpdateEnabled {
		t.Fatal("auto_update_enabled override was clobbered")
	}
}

func TestServicesMarkGone(t *testing.T) {
	db := openTempStore(t)
	pid := seedProject(t, db)
	s := store.NewServices(db)
	id, err := s.Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkGone(id); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListByProject(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != "gone" {
		t.Fatalf("state = %q, want gone", got[0].State)
	}
	if got[0].GoneSince == nil {
		t.Fatal("ListByProject: gone_since not populated after MarkGone")
	}
}

func TestMarkGoneSetsGoneSinceOnceThenPreserves(t *testing.T) {
	db := openTempStore(t)
	pid := seedProject(t, db)
	s := store.NewServices(db)
	id, err := s.Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkGone(id); err != nil {
		t.Fatal(err)
	}
	got1, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got1.State != "gone" {
		t.Fatalf("state = %q, want gone", got1.State)
	}
	if got1.GoneSince == nil {
		t.Fatal("gone_since not set on transition")
	}
	// Force a distinguishable sentinel timestamp so a repeat MarkGone that
	// (incorrectly) overwrites it is caught even under second-resolution clocks.
	sentinel := got1.GoneSince.Add(-time.Hour).Truncate(time.Second)
	if _, err := db.Exec(`UPDATE services SET gone_since=? WHERE id=?`, sentinel, id); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkGone(id); err != nil { // still gone
		t.Fatal(err)
	}
	got2, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got2.GoneSince == nil || !got2.GoneSince.Equal(sentinel) {
		t.Fatalf("gone_since must be preserved on repeat MarkGone: sentinel %v -> %v", sentinel, got2.GoneSince)
	}
}

func TestServicesUpsertClearsGoneSince(t *testing.T) {
	db := openTempStore(t)
	pid := seedProject(t, db)
	s := store.NewServices(db)
	id, err := s.Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkGone(id); err != nil {
		t.Fatal(err)
	}
	// Re-upsert the same (project_id, name) as present.
	id2, err := s.Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("upsert produced new id %d, want %d", id2, id)
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GoneSince != nil {
		t.Fatalf("gone_since should be cleared on upsert, got %v", got.GoneSince)
	}
	if got.State == "gone" {
		t.Fatal("state should no longer be gone after present upsert")
	}
}

func TestServicesDeleteCascades(t *testing.T) {
	db := openTempStore(t)
	pid := seedProject(t, db)
	s := store.NewServices(db)
	id, err := s.Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(id); !errors.Is(err, store.ErrServiceNotFound) {
		t.Fatalf("service should be gone after Delete, err=%v", err)
	}
}

func TestServicesGet(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		CurrentDigest: "sha256:cur", ContainerIDs: []string{"c1", "c2"}, State: "running",
	})
	got, err := store.NewServices(db).Get(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "app" || got.ImageRef != "ghcr.io/acme/web:1.2.3" {
		t.Fatalf("service = %+v", got)
	}
	if len(got.ContainerIDs) != 2 || got.ContainerIDs[0] != "c1" {
		t.Fatalf("container ids = %v", got.ContainerIDs)
	}
}

func TestServicesGetMissingReturnsSentinel(t *testing.T) {
	db := openTempStore(t)
	if _, err := store.NewServices(db).Get(999); !errors.Is(err, store.ErrServiceNotFound) {
		t.Fatalf("err = %v, want ErrServiceNotFound", err)
	}
}

func TestServicesSetAutoUpdateNullable(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	tru := true
	if err := store.NewServices(db).SetAutoUpdate(sid, &tru); err != nil {
		t.Fatal(err)
	}
	got, _ := store.NewServices(db).Get(sid)
	if got.AutoUpdateEnabled == nil || *got.AutoUpdateEnabled != true {
		t.Fatalf("auto = %v, want *true", got.AutoUpdateEnabled)
	}
	if err := store.NewServices(db).SetAutoUpdate(sid, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = store.NewServices(db).Get(sid)
	if got.AutoUpdateEnabled != nil {
		t.Fatalf("auto = %v, want nil (inherit)", got.AutoUpdateEnabled)
	}
}

func TestServicesUpdateState(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", State: "running"})
	if err := store.NewServices(db).UpdateState(sid, "exited"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.NewServices(db).Get(sid)
	if got.State != "exited" {
		t.Fatalf("state = %q, want exited", got.State)
	}
}

func TestServicesUpdateRuntime(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ContainerIDs: []string{"old"}, CurrentDigest: "sha256:old",
	})
	if err := store.NewServices(db).UpdateRuntime(sid, []string{"new1", "new2"}, "sha256:new"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.NewServices(db).Get(sid)
	if got.CurrentDigest != "sha256:new" {
		t.Fatalf("digest = %q, want sha256:new", got.CurrentDigest)
	}
	if len(got.ContainerIDs) != 2 || got.ContainerIDs[0] != "new1" {
		t.Fatalf("container ids = %v", got.ContainerIDs)
	}
}

func TestServicesList(t *testing.T) {
	db := openTempStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	_, _ = store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "a"})
	_, _ = store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "b"})
	all, err := store.NewServices(db).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List len = %d, want 2", len(all))
	}
}

func TestEffectiveAutoUpdate(t *testing.T) {
	tru, fls := true, false
	on := store.Project{AutoUpdateEnabled: true}
	off := store.Project{AutoUpdateEnabled: false}
	cases := []struct {
		name string
		p    store.Project
		s    store.Service
		want bool
	}{
		{"both on", on, store.Service{AutoUpdateEnabled: &tru}, true},
		{"project off", off, store.Service{AutoUpdateEnabled: &tru}, false},
		{"service off", on, store.Service{AutoUpdateEnabled: &fls}, false},
		{"service inherit(nil)=inherit project", on, store.Service{AutoUpdateEnabled: nil}, true},
		{"service inherit(nil) off project", off, store.Service{AutoUpdateEnabled: nil}, false},
		{"pinned excluded", on, store.Service{AutoUpdateEnabled: &tru, Pinned: true}, false},
	}
	for _, c := range cases {
		if got := store.EffectiveAutoUpdate(c.p, c.s); got != c.want {
			t.Fatalf("%s: EffectiveAutoUpdate = %v, want %v", c.name, got, c.want)
		}
	}
}
