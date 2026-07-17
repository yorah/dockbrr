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
	repoPath     = "yorah/dockbrr"
	keyTag       = "selfupdate_latest_tag"
	keyURL       = "selfupdate_latest_url"
	keyCheckedAt = "selfupdate_checked_at"
)

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

	fTag, fURL, err := c.fetchLatest(ctx)
	if err != nil {
		if haveCache {
			// Best-effort: serve stale and leave checked_at untouched so the
			// next request retries GitHub rather than waiting out the TTL.
			logger.Debugf("selfupdate: github fetch failed, serving stale cache: %v", err)
			return c.result(tag, url, checkedAt), nil
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
	defer resp.Body.Close()
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
	tag, err := c.settings.Get(keyTag)
	if err != nil || tag == "" {
		return "", "", time.Time{}, false
	}
	url, _ = c.settings.Get(keyURL)
	if url == "" {
		return "", "", time.Time{}, false
	}
	ts, err := c.settings.Get(keyCheckedAt)
	if err != nil {
		return "", "", time.Time{}, false
	}
	checkedAt, err = time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", "", time.Time{}, false
	}
	return tag, url, checkedAt, true
}

func (c *Checker) writeCache(tag, url string, at time.Time) {
	// Best-effort persistence: a failed write just means the next request refetches.
	_ = c.settings.Set(keyTag, tag)
	_ = c.settings.Set(keyURL, url)
	_ = c.settings.Set(keyCheckedAt, at.Format(time.RFC3339))
}
