// Package selfupdate reports whether a newer stable dockbrr release is
// available on GitHub. It is read-only and best-effort: a GitHub outage or
// rate-limit never blocks or errors the UI, it degrades to "no update".
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"dockbrr/internal/detect"
	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

const (
	repoPath = "yorah/dockbrr"
	keyCache = "selfupdate_cache"
)

// cacheEntry is the single-row cache payload. Encoding all three fields under
// one settings key makes writeCache a single atomic Set: a reader never sees a
// half-written cache, so there is no partial-key state to guard against.
type cacheEntry struct {
	Tag       string    `json:"tag"`
	URL       string    `json:"url"`
	CheckedAt time.Time `json:"checked_at"`
}

// Result is a point-in-time answer to "is there a newer release?".
type Result struct {
	Current         string    // the running build version
	Latest          string    // latest stable tag from GitHub (as returned, e.g. "v0.5.0")
	HTMLURL         string    // release page URL
	UpdateAvailable bool      // Latest's numeric core is greater than Current's
	CheckedAt       time.Time // when Latest was last fetched from GitHub
}

// Checker resolves the latest release, caching it in the settings store so a
// busy dashboard does not hammer the GitHub API.
type Checker struct {
	http     *http.Client
	settings *store.Settings
	current  string
	ttl      time.Duration
	apiBase  string
	tokenFn  func() string
}

// NewChecker wires a Checker. apiBase defaults to https://api.github.com; a nil
// tokenFn is treated as "no token".
func NewChecker(httpClient *http.Client, settings *store.Settings, current, apiBase string, ttl time.Duration, tokenFn func() string) *Checker {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	if tokenFn == nil {
		tokenFn = func() string { return "" }
	}
	return &Checker{http: httpClient, settings: settings, current: current, ttl: ttl, apiBase: apiBase, tokenFn: tokenFn}
}

// Check returns the latest-release verdict. It serves a cached answer when the
// cache is younger than the TTL, otherwise it refetches. On a GitHub failure it
// falls back to a stale cache when one exists (returning nil error), and only
// returns an error when there is nothing cached to fall back on.
func (c *Checker) Check(ctx context.Context) (Result, error) {
	tag, url, checkedAt, haveCache := c.readCache()
	if haveCache && time.Since(checkedAt) < c.ttl {
		return c.result(tag, url, checkedAt), nil
	}
	// The background poll is best-effort: a GitHub outage must never surface in
	// the UI, so a stale cache is served with a nil error.
	return c.refresh(ctx, true, haveCache, tag, url, checkedAt)
}

// CheckFresh always refetches from GitHub, ignoring the cache TTL. Used by the
// manual "Check for updates" action, which must reflect a brand-new release
// rather than a verdict cached minutes ago. Unlike Check, it does NOT swallow a
// fetch failure: it returns the error (alongside the stale body, if any) so the
// endpoint can tell the user the check itself failed instead of masquerading a
// stale cache as a fresh "up to date" verdict.
func (c *Checker) CheckFresh(ctx context.Context) (Result, error) {
	tag, url, checkedAt, haveCache := c.readCache()
	return c.refresh(ctx, false, haveCache, tag, url, checkedAt)
}

// refresh performs the GitHub fetch, cache-write on success, and stale-cache
// fallback on failure shared by Check and CheckFresh. bestEffort controls the
// failure contract: the poll (bestEffort=true) serves a stale cache with a nil
// error; the manual check (bestEffort=false) returns the same stale body but
// surfaces the error so the caller can report the failure.
func (c *Checker) refresh(ctx context.Context, bestEffort, haveCache bool, tag, url string, checkedAt time.Time) (Result, error) {
	fTag, fURL, err := c.fetchLatest(ctx)
	if err != nil {
		if haveCache {
			// Serve stale and leave checked_at untouched so the next request
			// retries GitHub rather than waiting out the TTL. The poll swallows
			// the error; the manual check surfaces it.
			logger.Debugf("selfupdate: github fetch failed, serving stale cache: %v", err)
			if bestEffort {
				return c.result(tag, url, checkedAt), nil
			}
			return c.result(tag, url, checkedAt), err
		}
		logger.Debugf("selfupdate: github fetch failed, no cache: %v", err)
		return Result{Current: c.current}, err
	}

	now := time.Now().UTC()
	c.writeCache(fTag, fURL, now)
	return c.result(fTag, fURL, now), nil
}

func (c *Checker) result(tag, url string, checkedAt time.Time) Result {
	return Result{
		Current:         c.current,
		Latest:          tag,
		HTMLURL:         url,
		UpdateAvailable: isNewer(c.current, tag),
		CheckedAt:       checkedAt,
	}
}

// isNewer reports whether latest's numeric core exceeds current's. An
// unparsable current (dev build) yields false, so dev builds are never nagged.
func isNewer(current, latest string) bool {
	cur, ok1 := detect.ParseCore(current)
	lat, ok2 := detect.ParseCore(latest)
	if !ok1 || !ok2 {
		return false
	}
	return detect.CoreLess(cur, lat)
}

func (c *Checker) fetchLatest(ctx context.Context) (tag, htmlURL string, err error) {
	// /releases/latest excludes drafts and pre-releases: stable only.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/repos/"+repoPath+"/releases/latest", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := c.tokenFn(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github releases/latest: status %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	if body.TagName == "" {
		return "", "", errors.New("github releases/latest: empty tag_name")
	}
	return body.TagName, body.HTMLURL, nil
}

func (c *Checker) readCache() (tag, url string, checkedAt time.Time, ok bool) {
	raw, err := c.settings.Get(keyCache)
	if err != nil || raw == "" {
		return "", "", time.Time{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return "", "", time.Time{}, false
	}
	if e.Tag == "" || e.CheckedAt.IsZero() {
		return "", "", time.Time{}, false
	}
	return e.Tag, e.URL, e.CheckedAt, true
}

func (c *Checker) writeCache(tag, url string, at time.Time) {
	// Best-effort persistence: a failed write just means the next request
	// refetches. One key, one Set: atomic, no partial-cache window.
	raw, err := json.Marshal(cacheEntry{Tag: tag, URL: url, CheckedAt: at})
	if err != nil {
		return
	}
	_ = c.settings.Set(keyCache, string(raw))
}
