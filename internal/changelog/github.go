package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"dockbrr/internal/detect"
	"dockbrr/internal/logger"
)

const (
	defaultGitHubAPIBase = "https://api.github.com"
	defaultGitHubRawBase = "https://raw.githubusercontent.com"

	// releasesPerPage is the GitHub API page size for the releases list.
	releasesPerPage = 100
	// maxReleasePages bounds how far back a range walk pages. A span wider than
	// 300 releases is reported as truncated rather than costing more API calls.
	maxReleasePages = 3
	// noteReserve is the byte budget kept free under maxChangelogBytes so the
	// "releases omitted" note always survives the cap.
	noteReserve = 512
)

// repoCache is GitHubSource's optional image->repo resolution cache. nil
// disables caching (every Resolve re-resolves live).
type repoCache interface {
	Get(repo string, ttl time.Duration) (owner, name string, positive, found bool, err error)
	Put(repo, owner, name string) error
}

// GitHubSource resolves changelog notes from the GitHub Releases API, with a
// best-effort CHANGELOG.md link fallback when no release matches. tokenFn,
// when it returns a non-empty value, is sent as a Bearer credential and is
// used for changelog reads only. tokenFn is called at each use site so a
// runtime-configurable token (e.g. read from settings) is honored without
// rebuilding the source.
type GitHubSource struct {
	client  *http.Client
	apiBase string
	rawBase string
	tokenFn func() string
	cache   repoCache
	ttl     time.Duration
}

// NewGitHubSource builds the source. A nil client gets a 10s-timeout default;
// empty apiBase/rawBase default to the public GitHub hosts. A nil tokenFn is
// treated as "no token". A nil cache disables the image->repo resolution
// cache (every Resolve re-resolves live). ttl<=0 defaults to 24h.
func NewGitHubSource(client *http.Client, apiBase, rawBase string, tokenFn func() string, cache repoCache, ttl time.Duration) *GitHubSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	if rawBase == "" {
		rawBase = defaultGitHubRawBase
	}
	if tokenFn == nil {
		tokenFn = func() string { return "" }
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &GitHubSource{client: client, apiBase: apiBase, rawBase: rawBase, tokenFn: tokenFn, cache: cache, ttl: ttl}
}

func (s *GitHubSource) Name() string { return "github-releases" }

// Resolve resolves the image to a GitHub repo (via githubTarget), fetches that
// repo's releases, and returns the notes for every release in the update's
// (FromVersion, Version] span, not just the target release, so a 7.2 -> 8.8
// jump shows what changed across all the versions in between. It falls back to
// a CHANGELOG.md blob link. It defers (empty Result) when no repo can be
// resolved or no version is known.
func (s *GitHubSource) Resolve(ctx context.Context, in Input) (Result, error) {
	tgt, ok := githubTarget(in.Image.Ref, in.Image.Labels)
	if !ok || in.Version == "" {
		return Result{}, nil
	}
	owner, name := tgt.Owner, tgt.Name
	// Key the cache on the resolved owner/name, not the raw image ref, so the
	// mapping (which depends on labels + tier rules) is what's remembered, so two
	// differently-written refs that resolve to the same repo share one entry.
	repoKey := owner + "/" + name
	var cached bool
	if s.cache != nil {
		if _, _, positive, found, err := s.cache.Get(repoKey, s.ttl); err != nil {
			logger.Warnf("changelog: cache get %s: %v", repoKey, err)
		} else if found {
			cached = true
			if !positive {
				return Result{}, nil // cached negative: repo does not exist, no network
			}
		}
	}
	// A parseable from-version means a span to walk: page back (bounded) until a
	// release at or below it is in hand. Without one (digest-only update), the
	// first page is all a single target release needs.
	fromCore, hasFrom := detect.ParseCore(in.FromVersion)
	pages := 1
	if hasFrom {
		pages = maxReleasePages
	}
	rels, reachedFrom, exists, err := s.fetchReleases(ctx, owner, name, pages, func(oldest ghRelease) bool {
		c, ok := detect.ParseCore(normalizeTag(oldest.TagName))
		return ok && !detect.CoreLess(fromCore, c) // oldest <= from: span covered
	})
	if err != nil {
		return Result{}, err
	}
	// Persist the resolution only on a cache miss: a positive hit already fetched
	// (the changelog itself is never cached, only the repo mapping), so re-Putting
	// would just re-stamp resolved_at with no benefit.
	if s.cache != nil && !cached {
		if exists {
			if perr := s.cache.Put(repoKey, owner, name); perr != nil {
				logger.Warnf("changelog: cache put %s: %v", repoKey, perr)
			}
		} else if perr := s.cache.Put(repoKey, "", ""); perr != nil {
			logger.Warnf("changelog: cache put %s: %v", repoKey, perr)
		}
	}
	if !exists {
		return Result{}, nil // repo does not exist: defer
	}
	want := tgt.tags(in.Version)
	target, ok := findRelease(rels, want, in.Version)
	if !ok {
		link, ok, err := s.changelogLink(ctx, owner, name, want)
		if err != nil {
			return Result{}, err
		}
		if ok {
			return Result{URL: link}, nil
		}
		return Result{}, nil
	}
	toCore, hasTo := detect.ParseCore(normalizeTag(target.TagName))
	// No span to report (digest-only update, unparseable versions, or a target at
	// or below the running version): the target release's own notes are the answer.
	if !hasFrom || !hasTo || !detect.CoreLess(fromCore, toCore) {
		return Result{Text: target.Body, URL: target.HTMLURL}, nil
	}
	// The compare view is the escape hatch when the span does not fit: it needs
	// the running version's real tag, which only exists if we paged back to it.
	fromTag := ""
	if fromRel, ok := findRelease(rels, tgt.tags(in.FromVersion), in.FromVersion); ok {
		fromTag = fromRel.TagName
	}
	span := releasesInSpan(rels, fromCore, toCore)
	link := spanLink(owner, name, fromTag, target.TagName, reachedFrom)
	return Result{Text: renderSpan(span, link, reachedFrom), URL: target.HTMLURL}, nil
}

// findRelease returns the release matching version: an exact tag match against
// want first, then, for a partial version like "8.8" which never equals a full
// semver release tag, the highest-numbered release whose normalized tag is that
// version's dotted prefix (8.8 -> the newest 8.8.x patch). Highest-numbered, not
// first-listed: GitHub orders releases by publish date, so a later backport can
// precede the newest patch. The "."-suffix guard makes "8.8" match "8.8.0" but
// not the "8.8-rc1" pre-release nor an unrelated "8.80.0".
func findRelease(rels []ghRelease, want []string, version string) (ghRelease, bool) {
	for _, rel := range rels {
		for _, w := range want {
			if rel.TagName == w {
				return rel, true
			}
		}
	}
	v := strings.TrimPrefix(version, "v")
	if v == "" || strings.Count(v, ".") >= 2 {
		return ghRelease{}, false
	}
	var best ghRelease
	var bestCore [3]int
	found := false
	for _, rel := range rels {
		norm := normalizeTag(rel.TagName)
		if !strings.HasPrefix(norm, v+".") {
			continue
		}
		c, ok := detect.ParseCore(norm)
		if !ok {
			continue
		}
		if !found || detect.CoreLess(bestCore, c) {
			best, bestCore, found = rel, c, true
		}
	}
	return best, found
}

// releasesInSpan keeps the stable releases in (fromCore, toCore], ordered by
// version, highest first. GitHub lists releases by publish date, which is NOT
// version order: a project maintaining several lines at once (redis backporting
// 8.6.4 after shipping 8.8.0) publishes older versions later, which would bury
// the target release mid-page and make the size cap drop sections by recency
// instead of by version. Pre-release and build-metadata tags are dropped: they
// are never what an image tag resolves to, and their notes duplicate the stable
// release that follows.
func releasesInSpan(rels []ghRelease, fromCore, toCore [3]int) []ghRelease {
	type scored struct {
		rel  ghRelease
		core [3]int
	}
	var keep []scored
	for _, rel := range rels {
		norm := normalizeTag(rel.TagName)
		if strings.ContainsAny(norm, "-+") {
			continue
		}
		c, ok := detect.ParseCore(norm)
		if !ok || !detect.CoreLess(fromCore, c) || detect.CoreLess(toCore, c) {
			continue
		}
		keep = append(keep, scored{rel, c})
	}
	sort.SliceStable(keep, func(i, j int) bool { return detect.CoreLess(keep[j].core, keep[i].core) })
	out := make([]ghRelease, len(keep))
	for i, s := range keep {
		out[i] = s.rel
	}
	return out
}

// renderSpan concatenates the span's notes newest-first under "## <tag>"
// headings, stopping before maxChangelogBytes and appending an "omitted" note
// (linking to compareURL) for whatever did not fit. complete=false means the
// span itself is short of the running version (the page cap cut the walk), which
// gets the same note. A single-release complete span is returned verbatim,
// with no heading and nothing to compare.
func renderSpan(span []ghRelease, compareURL string, complete bool) string {
	if len(span) == 0 {
		return ""
	}
	if len(span) == 1 && complete {
		return span[0].Body
	}
	var b strings.Builder
	budget := maxChangelogBytes - noteReserve
	for i, rel := range span {
		chunk := "## " + rel.TagName + "\n\n" + strings.TrimSpace(rel.Body)
		if i > 0 {
			chunk = "\n\n---\n\n" + chunk
		}
		if b.Len() > 0 && b.Len()+len(chunk) > budget {
			b.WriteString(omittedNote(len(span)-i, compareURL))
			return b.String()
		}
		b.WriteString(chunk)
	}
	if !complete {
		b.WriteString(omittedNote(0, compareURL))
	}
	return b.String()
}

// omittedNote renders the trailing "n releases omitted" line. n<=0 means the
// span itself was cut short by the page cap (an unknown number of older
// releases is missing).
func omittedNote(n int, compareURL string) string {
	if n <= 0 {
		return fmt.Sprintf("\n\n---\n\n_Older releases omitted, see the [full comparison](%s)._", compareURL)
	}
	plural := "releases"
	if n == 1 {
		plural = "release"
	}
	return fmt.Sprintf("\n\n---\n\n_%d earlier %s omitted, see the [full comparison](%s)._", n, plural, compareURL)
}

// spanLink is the URL the omitted note points at: GitHub's compare view when
// both endpoints are known real tags, else the repo's releases page.
func spanLink(owner, name, fromTag, toTag string, reachedFrom bool) string {
	base := fmt.Sprintf("https://github.com/%s/%s", url.PathEscape(owner), url.PathEscape(name))
	if !reachedFrom || fromTag == "" {
		return base + "/releases"
	}
	return fmt.Sprintf("%s/compare/%s...%s", base, url.PathEscape(fromTag), url.PathEscape(toTag))
}

// normalizeTag strips the release-tag decorations dockbrr treats as noise
// ("release-1.31.2", "v1.31.2" -> "1.31.2") so a tag can be parsed as semver.
func normalizeTag(tag string) string {
	return strings.TrimPrefix(strings.TrimPrefix(tag, "release-"), "v")
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
}

// fetchReleases GETs a repo's releases newest-first, up to maxPages pages of
// releasesPerPage. It stops early on a short page (the list is exhausted) or
// once covered reports the oldest release in hand is at or below the version the
// caller is walking back to. reached=false means the walk hit the page cap with
// older releases still unseen. exists=false on 404 (no such repo). A non-2xx/404
// status is an error.
func (s *GitHubSource) fetchReleases(ctx context.Context, owner, repo string, maxPages int, covered func(oldest ghRelease) bool) (rels []ghRelease, reached, exists bool, err error) {
	for page := 1; page <= maxPages; page++ {
		batch, ok, err := s.fetchReleasesPage(ctx, owner, repo, page)
		if err != nil {
			return nil, false, false, err
		}
		if !ok {
			return nil, false, false, nil
		}
		rels = append(rels, batch...)
		if len(rels) > 0 && covered != nil && covered(rels[len(rels)-1]) {
			return rels, true, true, nil
		}
		if len(batch) < releasesPerPage {
			return rels, true, true, nil // list exhausted: nothing older exists
		}
	}
	return rels, false, true, nil
}

// fetchReleasesPage GETs one page of a repo's releases. exists=false on 404.
func (s *GitHubSource) fetchReleasesPage(ctx context.Context, owner, repo string, page int) ([]ghRelease, bool, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d&page=%d",
		s.apiBase, url.PathEscape(owner), url.PathEscape(repo), releasesPerPage, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := s.tokenFn(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var rels []ghRelease
		if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
			return nil, false, err
		}
		return rels, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("github releases: status %d", resp.StatusCode)
	}
}

// changelogLink probes raw CHANGELOG.md at each candidate tag and returns the
// human blob link when the file exists. Best-effort, link-only.
func (s *GitHubSource) changelogLink(ctx context.Context, owner, repo string, tags []string) (string, bool, error) {
	for _, tag := range tags {
		rawURL := fmt.Sprintf("%s/%s/%s/%s/CHANGELOG.md",
			s.rawBase, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", false, err
		}
		if tok := s.tokenFn(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return "", false, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return fmt.Sprintf("https://github.com/%s/%s/blob/%s/CHANGELOG.md",
				url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag)), true, nil
		}
	}
	return "", false, nil
}
