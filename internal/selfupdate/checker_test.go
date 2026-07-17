package selfupdate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"dockbrr/internal/secret"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

func newSettings(t *testing.T) *store.Settings {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := secret.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return store.NewSettings(db, sealer)
}

// ghServer returns an httptest server that serves one releases/latest payload
// and counts hits, so tests can assert the cache path skips the network.
func ghServer(t *testing.T, tag, htmlURL string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.URL.Path != "/repos/yorah/dockbrr/releases/latest" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"` + htmlURL + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedCache writes the single-row cache the checker reads, so tests can plant a
// stale entry without depending on the internal key layout.
func seedCache(t *testing.T, s *store.Settings, tag, url string, at time.Time) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"tag": tag, "url": url, "checked_at": at})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("selfupdate_cache", string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFreshFetchDetectsNewer(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://github.com/yorah/dockbrr/releases/tag/v0.5.0", &hits)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.UpdateAvailable {
		t.Errorf("want update available, got %+v", res)
	}
	if res.Latest != "v0.5.0" || res.HTMLURL == "" {
		t.Errorf("latest/url: %+v", res)
	}
	if hits != 1 {
		t.Errorf("want 1 GitHub hit, got %d", hits)
	}
}

func TestCheckCacheHitSkipsNetwork(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://x/y", &hits)
	s := newSettings(t)
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	if _, err := c.Check(context.Background()); err != nil { // warms cache
		t.Fatal(err)
	}
	if _, err := c.Check(context.Background()); err != nil { // served from cache
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("cache hit should not re-fetch; hits=%d", hits)
	}
}

func TestCheckStaleCacheRefetches(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://x/y", &hits)
	s := newSettings(t)
	// Seed a stale cache: checked_at far in the past, TTL short.
	seedCache(t, s, "v0.4.9", "https://old", time.Now().Add(-2*time.Hour).UTC())
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Latest != "v0.5.0" || hits != 1 {
		t.Errorf("stale cache should refetch; res=%+v hits=%d", res, hits)
	}
}

func TestCheckGitHubErrorServesStaleCache(t *testing.T) {
	// Server always 500s; a stale cache is present.
	var hits int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	s := newSettings(t)
	seedCache(t, s, "v0.5.0", "https://stale", time.Now().Add(-2*time.Hour).UTC())
	c := selfupdate.NewChecker(gh.Client(), s, "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if res.Latest != "v0.5.0" || !res.UpdateAvailable {
		t.Errorf("want stale v0.5.0 served, got %+v", res)
	}
}

func TestCheckGitHubErrorNoCache(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(gh.Close)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	res, err := c.Check(context.Background())
	if err == nil {
		t.Error("want soft error when no cache and GitHub fails")
	}
	if res.UpdateAvailable {
		t.Errorf("no cache + error must not claim an update: %+v", res)
	}
	if res.Current != "0.4.2" {
		t.Errorf("current should still be reported: %+v", res)
	}
}

func TestUpdateAvailableMatrix(t *testing.T) {
	cases := []struct {
		name, current, latest string
		want                  bool
	}{
		{"newer", "0.4.2", "v0.5.0", true},
		{"equal", "0.5.0", "v0.5.0", false},
		{"older", "0.5.1", "v0.5.0", false},
		{"dev-current-unparsable", "dev", "v0.5.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			gh := ghServer(t, tc.latest, "https://x/y", &hits)
			c := selfupdate.NewChecker(gh.Client(), newSettings(t), tc.current, gh.URL, time.Hour, nil)
			res, err := c.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.UpdateAvailable != tc.want {
				t.Errorf("%s: want %v, got %v", tc.name, tc.want, res.UpdateAvailable)
			}
		})
	}
}

func TestFetchSendsBearerToken(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		wantHeader string
	}{
		{"with-token", "ghp_secret", "Bearer ghp_secret"},
		{"empty-token", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth string
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"tag_name":"v0.5.0","html_url":"https://x/y"}`))
			}))
			t.Cleanup(gh.Close)
			c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, func() string { return tc.token })

			if _, err := c.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if gotAuth != tc.wantHeader {
				t.Errorf("Authorization = %q, want %q", gotAuth, tc.wantHeader)
			}
		})
	}
}

func TestCheckCancelledContext(t *testing.T) {
	var hits int
	gh := ghServer(t, "v0.5.0", "https://x/y", &hits)
	c := selfupdate.NewChecker(gh.Client(), newSettings(t), "0.4.2", gh.URL, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call so the outbound request never completes

	res, err := c.Check(ctx)
	if err == nil {
		t.Error("want error when context is cancelled and no cache exists")
	}
	if res.UpdateAvailable {
		t.Errorf("cancelled + no cache must not claim an update: %+v", res)
	}
	if hits != 0 {
		t.Errorf("cancelled request should not reach the server; hits=%d", hits)
	}
}
