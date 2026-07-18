package scan_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dockbrr/internal/changelog"
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
	err       error
	gotLabels map[string]string
}

func (f *fakeChangelog) Resolve(_ context.Context, _ store.Update, img registry.RemoteImage) (string, string, error) {
	f.gotLabels = img.Labels
	return f.text, f.url, f.err
}

func openScanStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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

func TestCheckServicePersistsRateLimitedStatus(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3", CurrentDigest: "sha256:old",
	})
	updates := store.NewUpdates(db)
	uid, _ := updates.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})

	det := fakeDetector{upd: &store.Update{ID: uid, ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0"}}
	cl := &fakeChangelog{err: changelog.ErrRateLimited}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	got, err := updates.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChangelogStatus != "rate_limited" {
		t.Fatalf("ChangelogStatus = %q, want rate_limited", got.ChangelogStatus)
	}
	if got.ChangelogText != "" || got.ChangelogURL != "" {
		t.Fatalf("changelog content = (%q,%q), want empty", got.ChangelogURL, got.ChangelogText)
	}
}

// TestCheckServiceClearsRateLimitedStatusOnSuccess drives the full round-trip: a
// prior rate-limited resolve left changelog_status='rate_limited', then a later
// CheckService whose resolve returns content must persist that content AND clear
// the marker back to '' (guaranteed by SetChangelog's SQL).
func TestCheckServiceClearsRateLimitedStatusOnSuccess(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3", CurrentDigest: "sha256:old",
	})
	updates := store.NewUpdates(db)
	uid, _ := updates.Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0", Status: "available"})
	// Seed the pre-existing rate-limited marker a prior scan would have left.
	if err := updates.SetChangelogStatus(uid, "rate_limited"); err != nil {
		t.Fatal(err)
	}

	det := fakeDetector{upd: &store.Update{ID: uid, ServiceID: sid, ToDigest: "sha256:new", Tag: "1.3.0"}}
	cl := &fakeChangelog{text: "release notes", url: "https://github.com/acme/web/releases/tag/1.3.0"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	got, err := updates.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChangelogStatus != "" {
		t.Fatalf("ChangelogStatus = %q, want cleared", got.ChangelogStatus)
	}
	if got.ChangelogText != "release notes" || got.ChangelogURL != "https://github.com/acme/web/releases/tag/1.3.0" {
		t.Fatalf("changelog content = (%q,%q), want persisted", got.ChangelogURL, got.ChangelogText)
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

func TestCheckServiceCreatesCurrentRowWhenUpToDateNoHistory(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		// ImageVersion (the running image's version label) is deliberately
		// DIFFERENT from the reverse-looked ResolvedVersion below, so the row's
		// version asserted at the end proves ResolvedVersion wins the precedence.
		CurrentDigest: "sha256:cur", ImageVersion: "0.0.0-label",
	})
	// The reverse-looked release version the UI shows for the running digest.
	_, _ = store.NewImages(db).Upsert(store.Image{
		Repo: "ghcr.io/acme/web", Digest: "sha256:cur",
		Labels: `{"org.opencontainers.image.source":"https://github.com/acme/web"}`,
	})
	_ = store.NewImages(db).SetResolvedVersion("ghcr.io/acme/web", "sha256:cur", "1.2.3")

	updates := store.NewUpdates(db)
	det := fakeDetector{upd: nil} // up to date
	cl := &fakeChangelog{text: "# 1.2.3 notes", url: "https://github.com/acme/web/releases/tag/1.2.3"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}

	rows, err := updates.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1 current row (%+v)", len(rows), rows)
	}
	r := rows[0]
	if r.Status != "current" {
		t.Fatalf("status = %q, want current", r.Status)
	}
	if r.FromDigest != "sha256:cur" || r.ToDigest != "sha256:cur" {
		t.Fatalf("digests = (%q,%q), want both sha256:cur", r.FromDigest, r.ToDigest)
	}
	if r.ToVersion != "1.2.3" || r.FromVersion != "1.2.3" {
		t.Fatalf("versions = (%q,%q), want 1.2.3 (ResolvedVersion must win over ImageVersion 0.0.0-label)", r.FromVersion, r.ToVersion)
	}
	if r.ChangelogText != "# 1.2.3 notes" || r.ChangelogURL == "" {
		t.Fatalf("changelog not persisted on current row: %+v", r)
	}
	// The resolver saw the running image's labels (repo resolution).
	if cl.gotLabels["org.opencontainers.image.source"] != "https://github.com/acme/web" {
		t.Fatalf("resolver got labels %v, want stored source label", cl.gotLabels)
	}
}

func TestCheckServiceSkipsCurrentRowWhenHistoryExists(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		CurrentDigest: "sha256:cur", ImageVersion: "1.2.3",
	})
	updates := store.NewUpdates(db)
	// Pre-existing applied history: a current row must NOT be created.
	if _, err := updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:old", ToDigest: "sha256:cur",
		Tag: "1.2.3", Status: "applied",
	}); err != nil {
		t.Fatal(err)
	}

	det := fakeDetector{upd: nil}
	cl := &fakeChangelog{text: "should not be called for current", url: "https://x"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}

	var cnt int
	_ = db.QueryRow(`SELECT COUNT(*) FROM updates WHERE service_id=? AND status='current'`, sid).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("current rows = %d, want 0 (history existed)", cnt)
	}
}

func TestCheckServiceCurrentRowRateLimited(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		CurrentDigest: "sha256:cur", ImageVersion: "1.2.3",
	})
	updates := store.NewUpdates(db)
	det := fakeDetector{upd: nil}
	cl := &fakeChangelog{err: changelog.ErrRateLimited}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil, nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	rows, _ := updates.ListLastAppliedByService()
	if len(rows) != 1 || rows[0].ChangelogStatus != "rate_limited" {
		t.Fatalf("want single current row marked rate_limited, got %+v", rows)
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

func TestCheckAllFreshInvalidatesEveryService(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	_, _ = store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "a", ImageRef: "nginx:1.25.0"})
	_, _ = store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "b", ImageRef: "redis:7.2.0"})
	spy := &spyInvalidator{}
	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), spy, nil)
	if err := s.CheckAllFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 2 {
		t.Fatalf("Invalidate calls = %d, want 2 (one per service)", spy.calls)
	}
}

func TestCheckAllKeepsCache(t *testing.T) {
	// The scheduler path must not invalidate: within the cache TTL it takes the
	// cheap digest-only route by design.
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	_, _ = store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "a", ImageRef: "nginx:1.25.0"})
	spy := &spyInvalidator{}
	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), spy, nil)
	if err := s.CheckAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 0 {
		t.Fatalf("Invalidate calls = %d, want 0 (scheduler sweep keeps the cache)", spy.calls)
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

func TestCheckServiceFreshReopensRolledBack(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "web", ImageRef: "nginx:1.25.0", CurrentDigest: "sha256:old",
	})
	updates := store.NewUpdates(db)
	uid, _, err := updates.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	if err := updates.MarkRolledBack(sid, "sha256:new"); err != nil {
		t.Fatal(err)
	}

	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), updates, store.NewImages(db), &spyInvalidator{}, nil)
	if err := s.CheckServiceFresh(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if got, _ := updates.Get(uid); got.Status != "available" {
		t.Fatalf("status = %q, want available (manual check lifts rolled_back suppression)", got.Status)
	}
}

func TestCheckServiceKeepsRolledBackSuppressed(t *testing.T) {
	// The poll path must NOT lift the suppression: that is what stops the
	// scheduler from feeding a just-reverted target back to auto-apply.
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "web", ImageRef: "nginx:1.25.0", CurrentDigest: "sha256:old",
	})
	updates := store.NewUpdates(db)
	uid, _, err := updates.RecordDrift(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	if err := updates.MarkRolledBack(sid, "sha256:new"); err != nil {
		t.Fatal(err)
	}

	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), updates, store.NewImages(db), &spyInvalidator{}, nil)
	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if got, _ := updates.Get(uid); got.Status != "rolled_back" {
		t.Fatalf("status = %q, want rolled_back (poll path keeps the suppression)", got.Status)
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
