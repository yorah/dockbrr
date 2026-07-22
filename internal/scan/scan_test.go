package scan_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	calls     int
}

func (f *fakeChangelog) Resolve(_ context.Context, _ store.Update, img registry.RemoteImage) (string, string, error) {
	f.calls++
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

// newScannerWithServices builds a Scanner backed by a fresh store seeded with
// n up-to-date services (so CheckService is a fast no-drift no-op), returning
// the scanner and the seeded service ids in insertion order.
func newScannerWithServices(t *testing.T, n int) (*scan.Scanner, []int64) {
	t.Helper()
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		sid, err := store.NewServices(db).Upsert(store.Service{
			ProjectID: pid, Name: fmt.Sprintf("svc%d", i), ImageRef: "nginx:1.25.0",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = sid
	}
	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil)
	return s, ids
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
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

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
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

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
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

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
	upToDate := scan.New(fakeDetector{upd: nil}, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil)
	if err := upToDate.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	// update found -> info "update available"
	det := fakeDetector{upd: &store.Update{ServiceID: sid, ToDigest: "sha256:new", FromVersion: "1.2.0", ToVersion: "1.3.0", Severity: "minor"}}
	withUpd := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), nil)
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
	s := scan.New(det, cl, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), func(int64) { notified = true })
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
	s := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), func(id int64) { gotID = id })
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
	s := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), func(int64) { calls++ })
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
	s := scan.New(det, &fakeChangelog{}, store.NewServices(db), store.NewUpdates(db), store.NewImages(db), func(int64) { calls++ })
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
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

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
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

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
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	rows, _ := updates.ListLastAppliedByService()
	if len(rows) != 1 || rows[0].ChangelogStatus != "rate_limited" {
		t.Fatalf("want single current row marked rate_limited, got %+v", rows)
	}
}

// A service that reached a NEW digest out of band (e.g. dockbrr self-updated
// its own container) must have its stale 'current' baseline replaced, not kept.
// Regression: HasAnyByService blocked any rewrite, pinning the baseline (and its
// changelog) to the superseded version forever.
func TestCheckServiceRefreshesStaleCurrentRowOnNewDigest(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	updates := store.NewUpdates(db)
	// Service now runs sha256:new (v0.10.0); a stale baseline still points at the
	// old sha256:old (v0.6.0) it was created for before the self-update.
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "dockbrr", ImageRef: "ghcr.io/yorah/dockbrr:latest",
		CurrentDigest: "sha256:new", ImageVersion: "0.10.0",
	})
	if _, err := updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:old", ToDigest: "sha256:old",
		FromVersion: "0.6.0", ToVersion: "0.6.0", Tag: "latest",
		Severity: "current", Status: "current", ChangelogText: "# 0.6.0 notes",
	}); err != nil {
		t.Fatal(err)
	}

	det := fakeDetector{upd: nil} // up to date at sha256:new
	cl := &fakeChangelog{text: "# 0.10.0 notes", url: "https://github.com/yorah/dockbrr/releases/tag/0.10.0"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}

	rows, err := updates.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want exactly 1 (stale baseline replaced), got %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Status != "current" || r.ToDigest != "sha256:new" || r.ToVersion != "0.10.0" {
		t.Fatalf("row = {status:%q to:%q ver:%q}, want current/sha256:new/0.10.0", r.Status, r.ToDigest, r.ToVersion)
	}
	if r.ChangelogText != "# 0.10.0 notes" {
		t.Fatalf("changelog = %q, want the 0.10.0 notes (baseline re-resolved)", r.ChangelogText)
	}
	// The old digest's baseline must be gone, not merely shadowed.
	var stale int
	_ = db.QueryRow(`SELECT COUNT(*) FROM updates WHERE service_id=? AND to_digest='sha256:old'`, sid).Scan(&stale)
	if stale != 0 {
		t.Fatalf("stale current rows = %d, want 0", stale)
	}
}

// A service whose only history is SUPERSEDED rows (every dockbrr self-update /
// out-of-band pull leaves one) has no surfaced changelog, so it must still get a
// current-version baseline. Regression: gating on "any non-current row" left
// these services with a greyed, changelog-less button.
func TestCheckServiceWritesBaselineWhenOnlySupersededHistory(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	updates := store.NewUpdates(db)
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "scrutiny", ImageRef: "ghcr.io/analogj/scrutiny:latest",
		CurrentDigest: "sha256:cur", ImageVersion: "0.9.2",
	})
	// Only superseded history (no available/applied/dismissed): not surfaced.
	if _, err := updates.Upsert(store.Update{
		ServiceID: sid, FromDigest: "sha256:a", ToDigest: "sha256:b",
		Tag: "latest", Status: "superseded",
	}); err != nil {
		t.Fatal(err)
	}

	det := fakeDetector{upd: nil} // up to date
	cl := &fakeChangelog{text: "# 0.9.2 notes", url: "https://github.com/AnalogJ/scrutiny/releases/tag/v0.9.2"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}

	rows, err := updates.ListLastAppliedByService()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "current" {
		t.Fatalf("rows = %+v, want a single 'current' baseline", rows)
	}
	if rows[0].ChangelogText != "# 0.9.2 notes" {
		t.Fatalf("changelog = %q, want the 0.9.2 notes (baseline resolved despite superseded history)", rows[0].ChangelogText)
	}
}

// A baseline already at the running digest is NOT re-resolved on the next
// up-to-date scan: its changelog is cached, so the changelog source (an API) is
// not hit again.
func TestCheckServiceDoesNotReresolveFreshCurrentRow(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:latest",
		CurrentDigest: "sha256:cur", ImageVersion: "1.2.3",
	})
	updates := store.NewUpdates(db)
	det := fakeDetector{upd: nil}
	cl := &fakeChangelog{text: "# notes", url: "https://x"}
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if cl.calls != 1 {
		t.Fatalf("changelog resolved %d times, want 1 (fresh baseline must not re-resolve)", cl.calls)
	}
}

// A baseline whose first resolve came back empty (a transient miss) is retried
// on the next scan, so an instance left changelog-less self-heals.
func TestCheckServiceRetriesEmptyCurrentRow(t *testing.T) {
	db := openScanStore(t)
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:latest",
		CurrentDigest: "sha256:cur", ImageVersion: "1.2.3",
	})
	updates := store.NewUpdates(db)
	det := fakeDetector{upd: nil}
	cl := &fakeChangelog{text: "", url: ""} // first resolve: empty miss
	s := scan.New(det, cl, store.NewServices(db), updates, store.NewImages(db), nil)

	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if rows, _ := updates.ListLastAppliedByService(); len(rows) != 1 || rows[0].ChangelogText != "" {
		t.Fatalf("after empty resolve want a single blank baseline, got %+v", rows)
	}

	// Next scan: the changelog is now available; the empty baseline must retry.
	cl.text, cl.url = "# 1.2.3 notes", "https://x"
	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if cl.calls != 2 {
		t.Fatalf("changelog resolved %d times, want 2 (empty baseline must retry)", cl.calls)
	}
	rows, _ := updates.ListLastAppliedByService()
	if len(rows) != 1 || rows[0].ChangelogText != "# 1.2.3 notes" {
		t.Fatalf("baseline not healed: %+v", rows)
	}
}

func TestCheckServicesFreshStopsOnCancelledContext(t *testing.T) {
	sc, svcIDs := newScannerWithServices(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the sweep starts

	var calls int
	if err := sc.CheckServicesFresh(ctx, svcIDs, false, func(done, total int) {
		calls++
	}); err != nil {
		t.Fatalf("CheckServicesFresh: %v", err)
	}
	if calls != 0 {
		t.Fatalf("onDone called %d time(s), want 0 (cancelled ctx must stop the sweep before any service)", calls)
	}
}

func TestCheckServicesFreshReportsProgressPerService(t *testing.T) {
	sc, svcIDs := newScannerWithServices(t, 3) // helper mirrors existing scan_test setup; returns 3 seeded service ids
	var got [][2]int
	err := sc.CheckServicesFresh(context.Background(), svcIDs, false, func(done, total int) {
		got = append(got, [2]int{done, total})
	})
	if err != nil {
		t.Fatalf("CheckServicesFresh: %v", err)
	}
	want := [][2]int{{1, 3}, {2, 3}, {3, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %v, want %v", got, want)
	}
}

func TestCheckServicesFreshContinuesPastMissingService(t *testing.T) {
	sc, svcIDs := newScannerWithServices(t, 2)
	ids := append([]int64{999999}, svcIDs...) // 999999 does not exist
	var calls int
	err := sc.CheckServicesFresh(context.Background(), ids, false, func(done, total int) { calls++ })
	if err != nil {
		t.Fatalf("want nil error (per-service errors are logged, not returned), got %v", err)
	}
	if calls != len(ids) {
		t.Fatalf("onDone calls = %d, want %d (fires even for the missing id)", calls, len(ids))
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

	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), updates, store.NewImages(db), nil)
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

	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), updates, store.NewImages(db), nil)
	if err := s.CheckService(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if got, _ := updates.Get(uid); got.Status != "rolled_back" {
		t.Fatalf("status = %q, want rolled_back (poll path keeps the suppression)", got.Status)
	}
}

func TestCheckServicesFreshReopenTrueLiftsRolledBack(t *testing.T) {
	// A scoped (service/project) manual sweep must reopen rolled_back updates,
	// same as the single-service CheckServiceFresh gesture.
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

	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), updates, store.NewImages(db), nil)
	if err := s.CheckServicesFresh(context.Background(), []int64{sid}, true, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := updates.Get(uid); got.Status != "available" {
		t.Fatalf("status = %q, want available (reopen=true lifts rolled_back suppression)", got.Status)
	}
}

func TestCheckServicesFreshReopenFalseKeepsRolledBack(t *testing.T) {
	// An all-services sweep must NOT reopen: that would make a just-rolled-back
	// update auto-apply-eligible again, a safety regression.
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

	s := scan.New(fakeDetector{}, &fakeChangelog{}, store.NewServices(db), updates, store.NewImages(db), nil)
	if err := s.CheckServicesFresh(context.Background(), []int64{sid}, false, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := updates.Get(uid); got.Status != "rolled_back" {
		t.Fatalf("status = %q, want rolled_back (reopen=false keeps the suppression)", got.Status)
	}
}
