package changelog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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

// ErrRateLimited signals that a GitHub Releases request was rejected for primary
// rate-limit exhaustion (HTTP 403/429 with X-RateLimit-Remaining: 0). It is
// distinct from an auth failure (401) or a forbidden resource, which stay
// generic errors. The resolver surfaces it only when the whole source chain
// finds no changelog content.
var ErrRateLimited = errors.New("changelog: github rate limited")

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
		// Releases API failed (commonly an unauthenticated rate-limit). The raw
		// CHANGELOG.md probe hits raw.githubusercontent.com, which is not subject
		// to that limit and does not need the releases list, so try it before
		// giving up. A hit returns a link (dropping the error); a miss propagates
		// the original error so a genuine rate-limit still surfaces.
		if link, ok, lerr := s.changelogLink(ctx, owner, name, tgt.tags(in.Version)); lerr == nil && ok {
			return Result{URL: link}, nil
		}
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
	// Keep only releases matching the image's variant flavor (e.g. libtorrentv1),
	// so a dual-version image does not resolve to a co-published sibling variant
	// that shares its app-core. No-op when the image has no flavor.
	rels = filterByFlavor(rels, extractFlavor(in.Version))
	want := tgt.tags(in.Version)
	target, ok := findRelease(rels, want, in.Version)
	if !ok {
		// Rolling tag (non-semver version, e.g. "master-omnibus"): no release is
		// tagged with it, but the running digest is built from the repo tip, so the
		// latest stable release is the right proxy. Only for non-semver versions: a
		// semver miss must not resolve to an unrelated latest release.
		if _, parseable := detect.ParseCore(in.Version); !parseable {
			if latest, ok := latestStableRelease(rels); ok {
				note := fmt.Sprintf("_Latest release for rolling tag `%s`._\n\n", in.Version)
				return Result{Text: note + latest.Body, URL: latest.HTMLURL}, nil
			}
		}
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
	// releasesInSpan drops pre-release/build-suffixed tags ("-"/"+"). When every
	// release in the span carries such a suffix (LinuxServer.io's "-lsNNN" build
	// tags are the whole stream), the span is empty even though the target
	// release has real notes. Fall back to the target's own body rather than
	// returning nothing.
	if len(span) == 0 {
		return Result{Text: target.Body, URL: target.HTMLURL}, nil
	}
	link := spanLink(owner, name, fromTag, target.TagName, reachedFrom)
	return Result{Text: renderSpan(span, link, reachedFrom), URL: target.HTMLURL}, nil
}

// findRelease returns the release matching version: an exact tag match against
// want first, then, for a partial version like "8.8" which never equals a full
// semver release tag, the highest-numbered release whose normalized tag is that
// version's dotted prefix (8.8 -> the newest 8.8.x patch). Highest-numbered, not
// first-listed: GitHub orders releases by publish date, so a later backport can
// precede the newest patch. The "."-suffix guard makes "8.8" match "8.8.0" but
// not the "8.8-rc1" pre-release nor an unrelated "8.80.0". For a full (>=2-dot)
// or empty version, a third tier falls back to full-semver core-equality,
// matching name-prefixed / build-suffixed tags (LSIO-style "znc-1.10.2-ls183")
// by parsed core, where an exact normalized-tag match wins over the newest
// same-core build.
func findRelease(rels []ghRelease, want []string, version string) (ghRelease, bool) {
	for _, rel := range rels {
		for _, w := range want {
			if rel.TagName == w {
				return rel, true
			}
		}
	}
	v := strings.TrimPrefix(version, "v")
	if v != "" && strings.Count(v, ".") < 2 {
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
	// Full-semver core-equality fallback: LSIO-style tags ("znc-1.10.2-ls183")
	// carry a name prefix and/or build suffix, so neither the raw exact match nor
	// the partial (<2-dot) scan above finds them. Match by parsed core instead. An
	// exact normalized-tag match (a suffix-bearing version like "1.10.2-ls183") wins
	// outright; otherwise the first-listed same-core release wins, which is the
	// newest-published and thus the highest build for a given core (LSIO publishes
	// ascending lsNNN over time). detect.ParseCore silently truncates a 4th+
	// numeric component ("8.8.0.1" -> core [8,8,0]), so both sides are also
	// required to have at most 3 leading numeric components: without that guard
	// an unrelated 4-part tag ("8.8.0.1") would falsely core-match a full 3-part
	// version ("8.8.0").
	normVer := normalizeTag(version)
	if vCore, cok := detect.ParseCore(normVer); cok && coreComponents(normVer) <= 3 {
		var best ghRelease
		coreFound := false
		for _, rel := range rels {
			norm := normalizeTag(rel.TagName)
			c, ok := detect.ParseCore(norm)
			if !ok || c != vCore || isPrerelease(norm) || coreComponents(norm) > 3 {
				continue
			}
			if norm == normVer {
				return rel, true
			}
			if !coreFound {
				best, coreFound = rel, true
			}
		}
		if coreFound {
			return best, true
		}
	}
	return ghRelease{}, false
}

// latestStableRelease returns the highest-semver stable release in rels
// (pre-releases skipped), for rolling-tag images whose version matches no release
// tag. Mirrors findRelease's prefix-scan / CoreLess ranking: highest version wins,
// not first-listed, since GitHub orders releases by publish date and a backport can
// precede the newest release.
func latestStableRelease(rels []ghRelease) (ghRelease, bool) {
	var best ghRelease
	var bestCore [3]int
	found := false
	for _, rel := range rels {
		norm := normalizeTag(rel.TagName)
		if isPrerelease(norm) {
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
// instead of by version. Pre-releases are dropped (they are never what an image
// tag resolves to, and their notes duplicate the stable release that follows),
// but downstream build suffixes such as LinuxServer.io's "-lsNNN" are kept: they
// tag stable releases and are exactly what those images resolve to.
func releasesInSpan(rels []ghRelease, fromCore, toCore [3]int) []ghRelease {
	type scored struct {
		rel  ghRelease
		core [3]int
	}
	var keep []scored
	for _, rel := range rels {
		norm := normalizeTag(rel.TagName)
		if isPrerelease(norm) {
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
// (a leading "<name>-" package prefix, "release-", "v") and truncates at the
// first "_", which precedes a downstream second version in LinuxServer.io's
// dual-version tags ("libtorrentv1-5.2.3_v1.2.20-ls126" -> "5.2.3"), so the
// app-core can be parsed as semver. "znc-1.10.2-ls183" -> "1.10.2-ls183",
// "release-1.31.2" -> "1.31.2".
func normalizeTag(tag string) string {
	tag = detect.StripNamePrefix(tag)
	tag = strings.TrimPrefix(strings.TrimPrefix(tag, "release-"), "v")
	if i := strings.IndexByte(tag, '_'); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// buildSuffixRe matches the version suffixes that are build counters, not
// variant flavors: LinuxServer.io's "lsNNN". extractFlavor treats such a
// segment (and any pre-release marker) as "no flavor".
var buildSuffixRe = regexp.MustCompile(`(?i)^ls\d+$`)

// extractFlavor returns the variant flavor encoded in a version's suffix, or ""
// when there is none. The flavor is the first "-"-separated segment after a
// numeric core, unless that segment is a build counter (lsNNN) or a pre-release
// marker (rc/beta/...), which name a build rather than a variant. Examples:
// "5.2.3-libtorrentv1" -> "libtorrentv1", "5.2.3-alpine" -> "alpine",
// "1.10.2-ls183" -> "", "5.2.3" -> "", "master-omnibus" -> "".
func extractFlavor(version string) string {
	v := detect.StripNamePrefix(strings.TrimSpace(version))
	v = strings.TrimPrefix(v, "v")
	i := strings.IndexByte(v, '-')
	if i < 0 {
		return ""
	}
	if _, ok := detect.ParseCore(v[:i]); !ok {
		return "" // suffix does not follow a numeric core: not a flavor
	}
	seg := v[i+1:]
	if j := strings.IndexByte(seg, '-'); j >= 0 {
		seg = seg[:j]
	}
	if seg == "" || buildSuffixRe.MatchString(seg) || prereleaseRe.MatchString(seg) {
		return ""
	}
	return seg
}

// filterByFlavor narrows rels to the releases whose tag carries flavor, so a
// flavored image (e.g. "libtorrentv1") resolves to its own variant's notes and
// not a co-published sibling variant that shares the same app-core. It only
// narrows when flavor is non-empty AND at least one release matches, so an image
// with no flavor, or a flavor absent from every release, keeps today's behavior.
func filterByFlavor(rels []ghRelease, flavor string) []ghRelease {
	if flavor == "" {
		return rels
	}
	var kept []ghRelease
	for _, rel := range rels {
		if strings.Contains(rel.TagName, flavor) {
			kept = append(kept, rel)
		}
	}
	if len(kept) == 0 {
		return rels
	}
	return kept
}

// coreComponents counts the leading dot-separated numeric components of a
// normalized tag, ignoring any "-"/"+" suffix: "1.10.2-ls183" -> 3,
// "8.8.0.1" -> 4. detect.ParseCore silently truncates to the first 3
// components, so callers doing core-equality comparisons must reject operands
// with more than 3 to avoid a false match between an unrelated 4-part tag
// (e.g. an OS-patch-style "8.8.0.1") and a real 3-part version ("8.8.0").
func coreComponents(norm string) int {
	if i := strings.IndexAny(norm, "-+"); i >= 0 {
		norm = norm[:i]
	}
	return strings.Count(norm, ".") + 1
}

// prereleaseRe matches the common pre-release identifiers that follow a "-" right
// after a version's numeric core ("-rc1", "-beta.2", "-alpha", "-snapshot"). It
// deliberately does NOT match downstream build suffixes like LinuxServer.io's
// "-ls311", which tag stable releases and must survive span aggregation.
var prereleaseRe = regexp.MustCompile(`(?i)^(rc|alpha|beta|pre|preview|dev|snapshot|nightly|canary|milestone|ea|m\d|b\d)`)

// isPrerelease reports whether a normalized tag names a pre-release. Build
// metadata ("+meta") and downstream build suffixes ("-lsNNN") are stable and
// return false; only a "-" segment opening with a recognized pre-release marker
// returns true.
func isPrerelease(norm string) bool {
	i := strings.IndexByte(norm, '-')
	if i < 0 {
		return false
	}
	return prereleaseRe.MatchString(norm[i+1:])
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
	defer func() { _ = resp.Body.Close() }()
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
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
			resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, false, ErrRateLimited
		}
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
