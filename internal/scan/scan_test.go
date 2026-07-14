package scan_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/scan"
	"dockbrr/internal/store"
)

type fakeDetector struct {
	upd *store.Update
	err error
}

func (f fakeDetector) Detect(context.Context, store.Service) (*store.Update, error) {
	return f.upd, f.err
}

// togglingDetector returns whatever upd is set to at call time, letting a test
// simulate drift appearing, clearing, and reappearing across polls.
type togglingDetector struct{ upd *store.Update }

func (d *togglingDetector) Detect(context.Context, store.Service) (*store.Update, error) {
	return d.upd, nil
}

type fakeChangelog struct {
	text, url string
	gotLabels map[string]string
}

func (f *fakeChangelog) Resolve(_ context.Context, _ store.Update, img registry.RemoteImage) (string, string, error) {
	f.gotLabels = img.Labels
	return f.text, f.url, nil
}

func openScanStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCheckServicePersistsChangelogFromStoredLabels(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3", CurrentDigest: "sha256:old",
	})
	// The detector recorded the update + image labels; simulate both.
	updates := store.NewUpdates(db)
	uid, _ := updates.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})
	_, _ = store.NewImages(db).Upsert(store.Image{
		Repo: "ghcr.io/acme/web", Digest: "sha256:new",
		Labels: `{"org.opencontainers.image.source":"https://github.com/acme/web"}`,
	})

	det := fakeDetector{upd: &store.Update{ID: uid, ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0"}}
	cl := &fakeChangelog{text: "release notes", url: "https://github.com/acme/web/releases/tag/1.3.0"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	var url, text string
	_ = db.QueryRow(`SELECT changelog_url, changelog_text FROM updates WHERE id=?`, uid).Scan(&url, &text)
	if url != "https://github.com/acme/web/releases/tag/1.3.0" || text != "release notes" {
		t.Fatalf("changelog not persisted: url=%q text=%q", url, text)
	}
	if cl.gotLabels["org.opencontainers.image.source"] != "https://github.com/acme/web" {
		t.Fatalf("changelog got labels %v, want the stored source label", cl.gotLabels)
	}
}

func TestCheckServiceEmitsBreadcrumbs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	if _, err := logger.Init(logger.Config{Path: logPath, Level: "debug", MaxSizeMB: 1, MaxBackups: 1}); err != nil {
		t.Fatal(err)
	}

	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.0", CurrentDigest: "sha256:old"})

	// up-to-date -> debug "checking" + "up to date", no info update line
	upToDate := scan.New(fakeDetector{upd: nil}, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil, nil)
	if err := upToDate.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	// update found -> info "update available"
	det := fakeDetector{upd: &store.Update{ServiceID: sid, ToDigest: "sha256:new", FromVersion: "1.2.0", ToVersion: "1.3.0", Severity: "minor"}}
	withUpd := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil, nil)
	if err := withUpd.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"checking service", "up to date", "update available service", "1.2.0 -> 1.3.0"} {
		if !strings.Contains(s, want) {
			t.Errorf("log missing %q; got:\n%s", want, s)
		}
	}
}

func TestCheckServiceNoUpdateIsNoop(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	det := fakeDetector{upd: nil} // up-to-date
	cl := &fakeChangelog{}
	notified := false
	s := scan.New(det, cl, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil, func(int64) { notified = true })
	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if cl.gotLabels != nil {
		t.Fatal("changelog resolver called when there was no update")
	}
	if notified {
		t.Fatal("notify called when there was no update")
	}
}

func TestCheckServiceNotifiesOnFreshUpdate(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.0"})
	det := fakeDetector{upd: &store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0"}}
	var gotID int64
	s := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil, func(id int64) { gotID = id })
	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if gotID != sid {
		t.Fatalf("notify got service id %d, want %d", gotID, sid)
	}
}

func TestCheckServiceNotifyDedupsSameStandingDrift(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.0"})
	// Detector returns the SAME standing update on every poll (drift persists).
	det := fakeDetector{upd: &store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0"}}
	calls := 0
	s := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil, func(int64) { calls++ })
	for i := 0; i < 3; i++ {
		if err := s.CheckService(context.Background(), sid); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("notify fired %d times for one standing drift, want 1", calls)
	}
}

func TestCheckServiceNotifyRefiresAfterDriftClearsAndReturns(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.0"})
	det := &togglingDetector{upd: &store.Update{ServiceID: sid, ToDigest: "sha256:v2", Tag: "2.0.0"}}
	calls := 0
	s := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil, func(int64) { calls++ })
	check := func() {
		if err := s.CheckService(context.Background(), sid); err != nil {
			t.Fatal(err)
		}
	}
	check()       // fresh drift → notify (1)
	check()       // same drift → deduped
	det.upd = nil // drift cleared
	check()       // clears the dedup memory
	det.upd = &store.Update{ServiceID: sid, ToDigest: "sha256:v3", Tag: "3.0.0"}
	check() // new drift → notify (2)
	if calls != 2 {
		t.Fatalf("notify fired %d times, want 2 (clear then re-drift re-fires)", calls)
	}
}

func TestCheckAllSweepsAllServices(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	_, _ = store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "a"})
	_, _ = store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "b"})
	det := fakeDetector{upd: nil}
	s := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil, nil)
	if err := s.CheckAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// spyInvalidator records the (repo, tag) passed to Invalidate.
type spyInvalidator struct {
	repo, tag string
	calls     int
}

func (s *spyInvalidator) Invalidate(repo, tag string) error {
	s.calls++
	s.repo, s.tag = repo, tag
	return nil
}

func TestCheckServiceFreshInvalidatesDetectCache(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "cache", ImageRef: "redis:7.2.0", CurrentDigest: "sha256:old",
	})
	spy := &spyInvalidator{}
	// Detector returns no drift; we only assert the cache was invalidated first.
	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), spy, nil)

	if err := s.CheckServiceFresh(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Fatalf("Invalidate calls = %d, want 1", spy.calls)
	}
	if spy.repo != "redis" || spy.tag != "7.2.0" {
		t.Fatalf("Invalidate(%q, %q), want (redis, 7.2.0)", spy.repo, spy.tag)
	}
}

func TestCheckServiceDoesNotInvalidate(t *testing.T) {
	// The periodic poll path (CheckService) must NOT touch the detect cache.
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "cache", ImageRef: "redis:7.2.0", CurrentDigest: "sha256:old",
	})
	spy := &spyInvalidator{}
	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), spy, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 0 {
		t.Fatalf("Invalidate calls = %d, want 0 (poll path must keep the cache)", spy.calls)
	}
}
