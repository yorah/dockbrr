package changelog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"dockbrr/internal/changelog"
	"dockbrr/internal/registry"
)

// failTransport fails the test if any HTTP request is attempted.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("unexpected network request (defer path must make none)")
	return nil, nil
}

func failClient(t *testing.T) *http.Client { return &http.Client{Transport: failTransport{t}} }

type ghRel struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

// ghServer stands up a fake GitHub API + raw host. releases maps "<owner>/<repo>"
// to its release list; changelogTags is the set of "<owner>/<repo>/<tag>" for
// which a raw CHANGELOG.md exists. wantAuth, when non-empty, asserts the
// Authorization header on BOTH the releases request and the raw CHANGELOG.md
// request. The releases endpoint honors per_page/page like the real API.
func ghServer(t *testing.T, releases map[string][]ghRel, changelogTags map[string]bool, wantAuth string) *httptest.Server {
	t.Helper()
	return ghServerPages(t, releases, changelogTags, wantAuth, nil)
}

// ghServerPages is ghServer plus a counter of releases-list requests per
// "<owner>/<repo>" (nil to skip counting).
func ghServerPages(t *testing.T, releases map[string][]ghRel, changelogTags map[string]bool, wantAuth string, listCalls map[string]int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	assertAuth := func(r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			t.Errorf("%s Authorization = %q, want %q", r.URL.Path, r.Header.Get("Authorization"), wantAuth)
		}
	}
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		assertAuth(r)
		// /repos/<owner>/<repo>/releases
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
		if len(parts) != 3 || parts[2] != "releases" {
			http.NotFound(w, r)
			return
		}
		rels, ok := releases[parts[0]+"/"+parts[1]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if listCalls != nil {
			listCalls[parts[0]+"/"+parts[1]]++
		}
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		if perPage <= 0 {
			perPage = 30
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page <= 0 {
			page = 1
		}
		lo := (page - 1) * perPage
		if lo > len(rels) {
			lo = len(rels)
		}
		hi := lo + perPage
		if hi > len(rels) {
			hi = len(rels)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rels[lo:hi])
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 4 && parts[3] == "CHANGELOG.md" &&
			changelogTags[parts[0]+"/"+parts[1]+"/"+parts[2]] {
			assertAuth(r)
			_, _ = w.Write([]byte("# Changelog"))
			return
		}
		http.NotFound(w, r)
	})
	return srv
}

// ghInput builds an Input for an image ref + version (no labels).
func ghInput(ref, version string) changelog.Input {
	return changelog.Input{Image: registry.RemoteImage{Ref: ref}, Version: version}
}

func TestGitHubNginxStyleReleaseHit(t *testing.T) {
	// nginx tags releases "release-1.31.2"; the image ref is label-less.
	srv := ghServer(t, map[string][]ghRel{
		"nginx/nginx": {{TagName: "release-1.31.2", HTMLURL: "https://github.com/nginx/nginx/releases/tag/release-1.31.2", Body: "## 1.31.2\n- fixed a bug"}},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("nginx:1.31.2", "1.31.2"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/nginx/nginx/releases/tag/release-1.31.2" {
		t.Fatalf("URL = %q", res.URL)
	}
	if !strings.Contains(res.Text, "fixed a bug") {
		t.Fatalf("Text = %q", res.Text)
	}
}

func TestGitHubPlainTagReleaseHit(t *testing.T) {
	// redis uses plain "7.4.0".
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "7.4.0", HTMLURL: "https://github.com/redis/redis/releases/tag/7.4.0", Body: "redis 7.4.0"}},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "redis 7.4.0") {
		t.Fatalf("Text = %q", res.Text)
	}
}

func TestGitHubPartialTagMatchesFullSemverRelease(t *testing.T) {
	// A floating image tag "8.8" must resolve to the newest 8.8.x stable release
	// ("8.8.0"), skipping the "8.8-rc1" pre-release and unrelated "8.80.0".
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.80.0", HTMLURL: "wrong-major", Body: "unrelated"},
			{TagName: "8.8-rc1", HTMLURL: "prerelease", Body: "rc"},
			{TagName: "8.8.0", HTMLURL: "https://github.com/redis/redis/releases/tag/8.8.0", Body: "redis 8.8.0 stable"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:8.8", "8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/redis/redis/releases/tag/8.8.0" {
		t.Fatalf("URL = %q, want the 8.8.0 stable release", res.URL)
	}
	if !strings.Contains(res.Text, "8.8.0 stable") {
		t.Fatalf("Text = %q", res.Text)
	}
}

func TestGitHubPartialTagPicksNewestPatch(t *testing.T) {
	// "8.6" -> newest 8.6.x (releases are newest-first, so 8.6.4 precedes 8.6.3).
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.6.4", HTMLURL: "newest", Body: "8.6.4"},
			{TagName: "8.6.3", HTMLURL: "older", Body: "8.6.3"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:8.6", "8.6"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "newest" {
		t.Fatalf("URL = %q, want the newest 8.6.4 patch", res.URL)
	}
}

func TestGitHubFullVersionDoesNotPrefixMatch(t *testing.T) {
	// A full-semver version "8.8.0" (2 dots) must NOT prefix-match a 4-part tag;
	// only exact candidates apply. No exact release here -> fall through to empty.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "8.8.0.1", HTMLURL: "u", Body: "hotfix"}},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:8.8.0", "8.8.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("full version should not prefix-match, got %+v", res)
	}
}

func TestGitHubTokenSent(t *testing.T) {
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "7.4.0", HTMLURL: "u", Body: "notes"}},
	}, nil, "Bearer ghp_secret")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "ghp_secret" }, nil, time.Hour)
	if _, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0")); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubChangelogFallback(t *testing.T) {
	// Repo exists (empty release list) but a raw CHANGELOG.md exists at a tag.
	srv := ghServer(t, map[string][]ghRel{"redis/redis": {}}, map[string]bool{"redis/redis/7.4.0": true}, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/redis/redis/blob/7.4.0/CHANGELOG.md" {
		t.Fatalf("URL = %q, want blob CHANGELOG link", res.URL)
	}
	if res.Text != "" {
		t.Fatalf("CHANGELOG fallback must be link-only, got text %q", res.Text)
	}
}

func TestGitHubRepo404ReturnsEmpty(t *testing.T) {
	// Repo does not exist on the fake server -> releases 404 -> defer.
	srv := ghServer(t, nil, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:9.9.9", "9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

func TestGitHubUnresolvableDefersNoNetwork(t *testing.T) {
	// A non-Hub, non-ghcr registry cannot be resolved to a repo -> defer, no HTTP.
	s := changelog.NewGitHubSource(failClient(t), "http://127.0.0.1:0", "http://127.0.0.1:0", func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("quay.io/prometheus/prometheus:v2.50.0", "2.50.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("unresolvable ref should defer, got %+v", res)
	}
}

func TestGitHubNoVersionDefersNoNetwork(t *testing.T) {
	s := changelog.NewGitHubSource(failClient(t), "http://127.0.0.1:0", "http://127.0.0.1:0", func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("nginx:1.25.0", ""))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("no-version input should defer, got %+v", res)
	}
}

// spyCache records Get/Put calls and serves canned rows.
type spyCache struct {
	rows map[string][2]string // repo -> {owner,name}; present key = found
	gets int
	puts int
}

func (c *spyCache) Get(repo string, ttl time.Duration) (string, string, bool, bool, error) {
	c.gets++
	v, ok := c.rows[repo]
	if !ok {
		return "", "", false, false, nil
	}
	return v[0], v[1], v[0] != "", true, nil
}

func (c *spyCache) Put(repo, owner, name string) error {
	c.puts++
	if c.rows == nil {
		c.rows = map[string][2]string{}
	}
	c.rows[repo] = [2]string{owner, name}
	return nil
}

func TestGitHubCacheStoresResolution(t *testing.T) {
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "7.4.0", HTMLURL: "u", Body: "notes"}},
	}, nil, "")
	cache := &spyCache{}
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, cache, time.Hour)
	if _, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0")); err != nil {
		t.Fatal(err)
	}
	if cache.puts != 1 {
		t.Fatalf("puts = %d, want 1", cache.puts)
	}
}

func TestGitHubNegativeCacheSkipsNetwork(t *testing.T) {
	// Cache holds a negative row keyed on the RESOLVED repo (redis/redis) -> no
	// HTTP at all.
	cache := &spyCache{rows: map[string][2]string{"redis/redis": {"", ""}}}
	s := changelog.NewGitHubSource(failClient(t), "http://127.0.0.1:0", "http://127.0.0.1:0", func() string { return "" }, cache, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (changelog.Result{}) {
		t.Fatalf("negative cache should defer, got %+v", res)
	}
}

func TestGitHubPositiveCacheHitDoesNotReput(t *testing.T) {
	// A positive cache hit still fetches releases (for fresh notes) but must NOT
	// re-Put, since the entry already exists.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {{TagName: "7.4.0", HTMLURL: "u", Body: "notes"}},
	}, nil, "")
	cache := &spyCache{rows: map[string][2]string{"redis/redis": {"redis", "redis"}}}
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, cache, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "notes") {
		t.Fatalf("Text = %q, want release notes on positive hit", res.Text)
	}
	if cache.puts != 0 {
		t.Fatalf("puts = %d, want 0 (positive hit must not re-Put)", cache.puts)
	}
}

// ghRangeInput builds an Input carrying both the running (from) and target
// (to) versions.
func ghRangeInput(ref, from, to string) changelog.Input {
	return changelog.Input{Image: registry.RemoteImage{Ref: ref}, FromVersion: from, Version: to}
}

func TestGitHubAggregatesReleasesInRange(t *testing.T) {
	// 7.2 -> 8.0 must include every release in (7.2, 8.0], newest-first, and
	// exclude the already-running 7.2.0 and the newer-than-target 8.2.0.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.2.0", HTMLURL: "too-new", Body: "future notes"},
			{TagName: "8.0.0", HTMLURL: "https://github.com/redis/redis/releases/tag/8.0.0", Body: "eight oh notes"},
			{TagName: "7.4.0", HTMLURL: "u74", Body: "seven four notes"},
			{TagName: "7.2.0", HTMLURL: "u72", Body: "already running notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:8.0", "7.2", "8.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/redis/redis/releases/tag/8.0.0" {
		t.Fatalf("URL = %q, want the target release", res.URL)
	}
	for _, want := range []string{"eight oh notes", "seven four notes", "## 8.0.0", "## 7.4.0"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("Text missing %q:\n%s", want, res.Text)
		}
	}
	for _, unwanted := range []string{"already running notes", "future notes"} {
		if strings.Contains(res.Text, unwanted) {
			t.Fatalf("Text must not contain %q:\n%s", unwanted, res.Text)
		}
	}
	if i, j := strings.Index(res.Text, "## 8.0.0"), strings.Index(res.Text, "## 7.4.0"); i > j {
		t.Fatalf("sections must be newest-first:\n%s", res.Text)
	}
}

func TestGitHubRangeOrdersByVersionNotPublishDate(t *testing.T) {
	// GitHub lists releases by publish date. Redis ships 8.8.0, then backports
	// 8.6.4 / 8.2.7 onto older lines, so the target release is NOT first in the
	// API's list. The rendered span must still lead with the target version.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.6.4", HTMLURL: "u864", Body: "backport four"},
			{TagName: "8.2.7", HTMLURL: "u827", Body: "backport seven"},
			{TagName: "8.8.0", HTMLURL: "https://github.com/redis/redis/releases/tag/8.8.0", Body: "the big one"},
			{TagName: "8.6.3", HTMLURL: "u863", Body: "older backport"},
			{TagName: "7.2.0", HTMLURL: "u720", Body: "running notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:8.8", "7.2.0", "8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/redis/redis/releases/tag/8.8.0" {
		t.Fatalf("URL = %q, want the 8.8.0 target", res.URL)
	}
	if !strings.HasPrefix(res.Text, "## 8.8.0") {
		t.Fatalf("target version must lead the changelog, got:\n%.60s", res.Text)
	}
	want := []string{"## 8.8.0", "## 8.6.4", "## 8.6.3", "## 8.2.7"}
	at := make([]int, len(want))
	for i, h := range want {
		if at[i] = strings.Index(res.Text, h); at[i] < 0 {
			t.Fatalf("Text missing %q", h)
		}
		if i > 0 && at[i-1] > at[i] {
			t.Fatalf("sections must run highest version first, %q precedes %q", want[i], want[i-1])
		}
	}
	if strings.Contains(res.Text, "running notes") {
		t.Fatalf("from-version notes must be excluded")
	}
}

func TestGitHubPartialTagPicksHighestPatchNotNewestPublished(t *testing.T) {
	// 8.8.1 exists but was published before an 8.6.x backport, so it is not first
	// in the list. The target for "8.8" is still 8.8.1.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.6.4", HTMLURL: "u864", Body: "backport"},
			{TagName: "8.8.1", HTMLURL: "u881", Body: "newest patch"},
			{TagName: "8.8.0", HTMLURL: "u880", Body: "first patch"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:8.8", "8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "u881" {
		t.Fatalf("URL = %q, want the highest 8.8.x patch (8.8.1)", res.URL)
	}
}

func TestGitHubRangeSkipsPrereleases(t *testing.T) {
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.0.0", HTMLURL: "u80", Body: "stable notes"},
			{TagName: "8.0.0-rc1", HTMLURL: "urc", Body: "candidate notes"},
			{TagName: "7.4.0", HTMLURL: "u74", Body: "seven four notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:8.0.0", "7.2.0", "8.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "candidate notes") {
		t.Fatalf("pre-release must be excluded:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "stable notes") || !strings.Contains(res.Text, "seven four notes") {
		t.Fatalf("Text = %q", res.Text)
	}
}

func TestGitHubRangeBuildSuffixTagsKeepNotes(t *testing.T) {
	// LinuxServer.io tags every stable release with a "-lsNNN" build suffix
	// (6.3.0.10514-ls311). A from->to span across such tags must still surface
	// the target release's notes; the "-"/"+" pre-release filter must not empty
	// the span and discard the body.
	srv := ghServer(t, map[string][]ghRel{
		"linuxserver/docker-radarr": {
			{TagName: "6.3.0.10514-ls311", HTMLURL: "https://github.com/linuxserver/docker-radarr/releases/tag/6.3.0.10514-ls311", Body: "radarr six three notes"},
			{TagName: "6.2.1.10461-ls309", HTMLURL: "u621", Body: "already running notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("linuxserver/docker-radarr:6.3.0.10514-ls311", "6.2.1.10461-ls309", "6.3.0.10514-ls311"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "radarr six three notes") {
		t.Fatalf("Text missing target notes:\n%q", res.Text)
	}
	if res.URL != "https://github.com/linuxserver/docker-radarr/releases/tag/6.3.0.10514-ls311" {
		t.Fatalf("URL = %q, want the target release", res.URL)
	}
}

func TestGitHubRangeAggregatesBuildSuffixReleases(t *testing.T) {
	// A multi-version LinuxServer.io jump must aggregate the intermediate
	// "-lsNNN" stable releases, not just the target: the build suffix is not a
	// pre-release and must survive span filtering. Any real pre-release in the
	// same window (an "-rc1" build) still drops out.
	srv := ghServer(t, map[string][]ghRel{
		"linuxserver/docker-radarr": {
			{TagName: "6.3.0.10514-ls311", HTMLURL: "https://github.com/linuxserver/docker-radarr/releases/tag/6.3.0.10514-ls311", Body: "six three notes"},
			{TagName: "6.3.0.10500-rc1", HTMLURL: "urc", Body: "candidate notes"},
			{TagName: "6.2.0.10200-ls305", HTMLURL: "u620", Body: "six two notes"},
			{TagName: "6.1.0.10000-ls300", HTMLURL: "u610", Body: "already running notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("linuxserver/docker-radarr:6.3.0.10514-ls311", "6.1.0.10000-ls300", "6.3.0.10514-ls311"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## 6.3.0.10514-ls311", "six three notes", "## 6.2.0.10200-ls305", "six two notes"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("Text missing %q:\n%s", want, res.Text)
		}
	}
	for _, unwanted := range []string{"already running notes", "candidate notes"} {
		if strings.Contains(res.Text, unwanted) {
			t.Fatalf("Text must not contain %q:\n%s", unwanted, res.Text)
		}
	}
	if i, j := strings.Index(res.Text, "## 6.3"), strings.Index(res.Text, "## 6.2"); i > j {
		t.Fatalf("sections must be newest-first:\n%s", res.Text)
	}
}

func TestGitHubNoFromVersionKeepsSingleRelease(t *testing.T) {
	// A digest-only update (no parseable from-version) still gets exactly the
	// target release body, with no aggregation heading.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.0.0", HTMLURL: "u80", Body: "eight oh notes"},
			{TagName: "7.4.0", HTMLURL: "u74", Body: "seven four notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:8.0.0", "", "8.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "eight oh notes" {
		t.Fatalf("Text = %q, want the target body alone", res.Text)
	}
}

func TestGitHubRangeSameVersionKeepsSingleRelease(t *testing.T) {
	// from == to (a digest-only rebuild of the same tag): no range, no headings.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.0.0", HTMLURL: "u80", Body: "eight oh notes"},
			{TagName: "7.4.0", HTMLURL: "u74", Body: "seven four notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:8.0.0", "8.0.0", "8.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "eight oh notes" {
		t.Fatalf("Text = %q, want the target body alone", res.Text)
	}
}

func TestGitHubRangePartialTargetTagSpansPatches(t *testing.T) {
	// Floating target "8.8" resolves to 8.8.2; the range from 8.7 must include
	// every 8.8.x patch up to it, not just 8.8.2.
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.8.2", HTMLURL: "u882", Body: "patch two"},
			{TagName: "8.8.1", HTMLURL: "u881", Body: "patch one"},
			{TagName: "8.8.0", HTMLURL: "u880", Body: "minor bump"},
			{TagName: "8.7.0", HTMLURL: "u870", Body: "running notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:8.8", "8.7", "8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "u882" {
		t.Fatalf("URL = %q, want newest 8.8.x", res.URL)
	}
	for _, want := range []string{"patch two", "patch one", "minor bump"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("Text missing %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "running notes") {
		t.Fatalf("from-version notes must be excluded:\n%s", res.Text)
	}
}

func TestGitHubRangePaginatesToReachFromVersion(t *testing.T) {
	// The from-version sits on page 2: the source must page until it is reached.
	var rels []ghRel
	for i := 150; i >= 0; i-- { // newest-first: 1.0.150 ... 1.0.0
		rels = append(rels, ghRel{
			TagName: fmt.Sprintf("1.0.%d", i),
			HTMLURL: fmt.Sprintf("u%d", i),
			Body:    fmt.Sprintf("notes for %d", i),
		})
	}
	calls := map[string]int{}
	srv := ghServerPages(t, map[string][]ghRel{"redis/redis": rels}, nil, "", calls)
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	// per_page=100, so 1.0.20 only appears on page 2.
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:1.0.150", "1.0.20", "1.0.150"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "notes for 21") {
		t.Fatalf("Text missing the oldest in-range release (1.0.21)")
	}
	if strings.Contains(res.Text, "notes for 20") {
		t.Fatalf("from-version release must be excluded")
	}
	if calls["redis/redis"] < 2 {
		t.Fatalf("releases list calls = %d, want >= 2 (must paginate)", calls["redis/redis"])
	}
	if calls["redis/redis"] > 3 {
		t.Fatalf("releases list calls = %d, want <= 3 (page cap)", calls["redis/redis"])
	}
}

func TestGitHubRangeStopsAtPageCapAndSaysSo(t *testing.T) {
	// 400 releases, from-version older than page 3 can reach: cap at 3 pages and
	// tell the reader the range is incomplete.
	var rels []ghRel
	for i := 400; i >= 1; i-- {
		rels = append(rels, ghRel{TagName: fmt.Sprintf("1.0.%d", i), HTMLURL: "u", Body: fmt.Sprintf("notes %d", i)})
	}
	calls := map[string]int{}
	srv := ghServerPages(t, map[string][]ghRel{"redis/redis": rels}, nil, "", calls)
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:1.0.400", "1.0.1", "1.0.400"))
	if err != nil {
		t.Fatal(err)
	}
	if calls["redis/redis"] != 3 {
		t.Fatalf("releases list calls = %d, want exactly 3 (page cap)", calls["redis/redis"])
	}
	if !strings.Contains(res.Text, "https://github.com/redis/redis") {
		t.Fatalf("truncated range must link out to GitHub:\n%s", res.Text[max(0, len(res.Text)-400):])
	}
	if !strings.Contains(strings.ToLower(res.Text), "omitted") {
		t.Fatalf("truncated range must say releases were omitted:\n%s", res.Text[max(0, len(res.Text)-400):])
	}
}

func TestGitHubRangeCapsTextSizeWithNote(t *testing.T) {
	// Bodies far exceeding the 64KB cache cap: keep the newest releases, then
	// stop and point at the full comparison.
	big := strings.Repeat("x", 30*1024)
	srv := ghServer(t, map[string][]ghRel{
		"redis/redis": {
			{TagName: "8.0.0", HTMLURL: "u80", Body: "newest " + big},
			{TagName: "7.6.0", HTMLURL: "u76", Body: "middle " + big},
			{TagName: "7.4.0", HTMLURL: "u74", Body: "oldest " + big},
			{TagName: "7.2.0", HTMLURL: "u72", Body: "running"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("redis:8.0.0", "7.2.0", "8.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > 64*1024 {
		t.Fatalf("Text = %d bytes, want <= 64KB", len(res.Text))
	}
	if !strings.Contains(res.Text, "newest") {
		t.Fatalf("newest release must survive the cap")
	}
	if strings.Contains(res.Text, "oldest ") {
		t.Fatalf("oldest release should have been dropped by the cap")
	}
	if !strings.Contains(res.Text, "compare/7.2.0...8.0.0") {
		t.Fatalf("capped text must link the GitHub compare view:\n%s", res.Text[len(res.Text)-300:])
	}
}

func TestGitHubRateLimitedYieldsErrRateLimited(t *testing.T) {
	// Both a 403 (primary limit) and a 429 (secondary/too-many-requests) with
	// X-RateLimit-Remaining:0 are rate-limit exhaustion and must map to the
	// sentinel via the same code path.
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(status)
			}))
			t.Cleanup(srv.Close)
			s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
			_, err := s.Resolve(context.Background(), ghInput("ghcr.io/autobrr/autobrr:latest", "1.82.1"))
			if !errors.Is(err, changelog.ErrRateLimited) {
				t.Fatalf("err = %v, want ErrRateLimited", err)
			}
		})
	}
}

func TestGitHubForbiddenWithoutRateHeaderIsGenericError(t *testing.T) {
	// A 403 that is NOT a primary-limit exhaustion (no X-RateLimit-Remaining:0),
	// and a 401 auth failure, must stay generic errors, not ErrRateLimited.
	for _, tc := range []struct {
		name   string
		status int
		remain string
	}{
		{"403-remaining-positive", http.StatusForbidden, "42"},
		{"403-header-absent", http.StatusForbidden, ""},
		{"auth-401", http.StatusUnauthorized, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.remain != "" {
					w.Header().Set("X-RateLimit-Remaining", tc.remain)
				}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(srv.Close)
			s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
			_, err := s.Resolve(context.Background(), ghInput("ghcr.io/autobrr/autobrr:latest", "1.82.1"))
			if err == nil {
				t.Fatal("err = nil, want a non-nil generic error")
			}
			if errors.Is(err, changelog.ErrRateLimited) {
				t.Fatalf("err = %v, want NOT ErrRateLimited", err)
			}
		})
	}
}

func TestGitHubChangelogLinkTokenSent(t *testing.T) {
	// The token must ride the raw CHANGELOG.md request too, not just /repos/.
	srv := ghServer(t, map[string][]ghRel{"redis/redis": {}}, map[string]bool{"redis/redis/7.4.0": true}, "Bearer ghp_secret")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "ghp_secret" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("redis:7.4.0", "7.4.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://github.com/redis/redis/blob/7.4.0/CHANGELOG.md" {
		t.Fatalf("URL = %q, want blob CHANGELOG link", res.URL)
	}
}

func TestGitHubReleasesErrorFallsBackToRawChangelog(t *testing.T) {
	// Releases API is rate-limited (403 + X-RateLimit-Remaining: 0), but a raw
	// CHANGELOG.md exists at the v-prefixed tag. The source must recover the
	// GitHub blob link and drop the rate-limit error.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// raw CHANGELOG.md only at foo/bar/v1.2.3
		if r.URL.Path == "/foo/bar/v1.2.3/CHANGELOG.md" {
			_, _ = w.Write([]byte("# Changelog"))
			return
		}
		http.NotFound(w, r)
	})

	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("ghcr.io/foo/bar:latest", "1.2.3"))
	if err != nil {
		t.Fatalf("err = %v, want nil (raw fallback should drop the rate-limit error)", err)
	}
	if res.URL != "https://github.com/foo/bar/blob/v1.2.3/CHANGELOG.md" {
		t.Fatalf("URL = %q, want the GitHub blob CHANGELOG link", res.URL)
	}
	if res.Text != "" {
		t.Fatalf("Text = %q, want empty (CHANGELOG fallback is link-only)", res.Text)
	}
}

func TestResolveRollingTagLatestRelease(t *testing.T) {
	// scrutiny's ghcr image floats on the non-semver "master-omnibus" tag, which
	// never matches a release. The running digest is built from repo tip, so the
	// latest stable release is the right proxy for "what changed".
	srv := ghServer(t, map[string][]ghRel{
		"AnalogJ/scrutiny": {
			{TagName: "v0.9.2", HTMLURL: "https://github.com/AnalogJ/scrutiny/releases/tag/v0.9.2", Body: "scrutiny 0.9.2 notes"},
			{TagName: "v0.9.1", HTMLURL: "https://github.com/AnalogJ/scrutiny/releases/tag/v0.9.1", Body: "0.9.1 notes"},
			{TagName: "v0.9.0-rc1", HTMLURL: "x", Body: "rc"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	in := changelog.Input{
		Image: registry.RemoteImage{
			Ref:    "ghcr.io/analogj/scrutiny:master-omnibus",
			Labels: map[string]string{"org.opencontainers.image.source": "https://github.com/AnalogJ/scrutiny"},
		},
		// Mirror the current-row caller (scan.ensureCurrentChangelog), which sets
		// FromVersion == Version == the rolling tag. Both are non-parseable, so the
		// span walk stays disabled and the latest-release fallback still fires.
		FromVersion: "master-omnibus",
		Version:     "master-omnibus",
	}
	res, err := s.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.Contains(res.Text, "Latest release for rolling tag `master-omnibus`") {
		t.Fatalf("missing rolling-tag note, got: %q", res.Text)
	}
	if !strings.Contains(res.Text, "scrutiny 0.9.2 notes") {
		t.Fatalf("want v0.9.2 body, got: %q", res.Text)
	}
	if res.URL != "https://github.com/AnalogJ/scrutiny/releases/tag/v0.9.2" {
		t.Fatalf("want v0.9.2 html_url, got: %q", res.URL)
	}
}

func TestResolveRollingTagPrereleaseOnlyFallsThrough(t *testing.T) {
	// Only a pre-release exists (no stable release to fall back to) and no raw
	// CHANGELOG.md either: the rolling-tag fallback must not fabricate a result.
	srv := ghServer(t, map[string][]ghRel{
		"o/r": {{TagName: "v1.0.0-rc1", HTMLURL: "x", Body: "rc"}},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("ghcr.io/o/r:latest", "latest"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Text != "" {
		t.Fatalf("want no text (fall through), got: %q", res.Text)
	}
}

func TestResolveSemverMissNoLatestFallback(t *testing.T) {
	// A parseable semver version that matches no release must NOT fall back to
	// the latest release: that fallback is reserved for non-semver rolling tags.
	srv := ghServer(t, map[string][]ghRel{
		"o/r": {{TagName: "v3.0.0", HTMLURL: "x", Body: "v3 notes"}},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("ghcr.io/o/r:1.2.3", "1.2.3"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if strings.Contains(res.Text, "v3 notes") {
		t.Fatalf("semver miss must NOT fall back to latest release, got: %q", res.Text)
	}
}

func TestGitHubReleasesErrorNoRawChangelogPreservesError(t *testing.T) {
	// Releases API rate-limited AND no raw CHANGELOG.md anywhere: the original
	// ErrRateLimited must still surface (not be swallowed by the fallback).
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghInput("ghcr.io/foo/bar:latest", "1.2.3"))
	if !errors.Is(err, changelog.ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited preserved", err)
	}
	if res.URL != "" || res.Text != "" {
		t.Fatalf("res = %+v, want empty when nothing resolved", res)
	}
}
