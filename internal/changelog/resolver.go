package changelog

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// maxChangelogBytes caps cached changelog text so a pathological description
// cannot bloat the updates row.
const (
	maxChangelogBytes = 64 * 1024
	ellipsis          = "…"
)

var (
	scriptRe = regexp.MustCompile(`(?is)<script.*?>.*?</script>`)
	tagRe    = regexp.MustCompile(`<[^>]+>`)
)

// Resolver runs an ordered chain of Sources, returning the first non-empty
// (sanitized) text/url pair. The OCI label source sits last as the always-keep-
// a-link fallback.
type Resolver struct {
	sources []Source
}

// NewResolver builds a resolver over the given ordered sources.
func NewResolver(sources []Source) *Resolver {
	return &Resolver{sources: sources}
}

// Resolve enriches an update with changelog text and/or a link. It returns
// ("","",nil) when no source hits (the caller marks the update unavailable). A
// source error is logged and treated as a miss; the chain never aborts.
func (r *Resolver) Resolve(ctx context.Context, u store.Update, img registry.RemoteImage) (text, url string, err error) {
	in := buildInput(u, img)
	// First non-empty (sanitized) pair wins. "Always keep a link" is structural:
	// each text-bearing source pairs its text with its own URL, and the offline
	// OCI label source runs last to supply a link when network sources miss.
	for _, s := range r.sources {
		res, rerr := s.Resolve(ctx, in)
		if rerr != nil {
			logger.Errorf("changelog: source %s: %v", s.Name(), rerr)
			continue
		}
		t := sanitizeText(res.Text)
		l := sanitizeURL(res.URL)
		if t != "" || l != "" {
			return t, l, nil
		}
	}
	return "", "", nil
}

// buildInput derives the per-update source context once.
func buildInput(u store.Update, img registry.RemoteImage) Input {
	return Input{
		Update:      u,
		Image:       img,
		Repo:        repoFromRef(img.Ref),
		FromVersion: u.FromVersion,
		Version:     firstNonEmpty(u.ToVersion, u.Tag),
	}
}

// repoFromRef strips the tag and @digest from an image reference, yielding the
// bare repository. Mirrors detect.SplitRef's repo half (kept local to avoid a
// cross-package dependency).
func repoFromRef(ref string) string {
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon >= 0 && !strings.Contains(ref[colon+1:], "/") {
		ref = ref[:colon]
	}
	return ref
}

// sanitizeText defangs changelog prose before caching: <script> blocks and raw
// HTML tags removed, ASCII control characters dropped, size capped on a rune
// boundary. (Render-time markdown→HTML sanitization is Phase 7.)
func sanitizeText(s string) string {
	if s == "" {
		return ""
	}
	s = scriptRe.ReplaceAllString(s, "")
	s = tagRe.ReplaceAllString(s, "")
	s = stripControl(s)
	s = strings.TrimSpace(s)
	if len(s) > maxChangelogBytes {
		cut := maxChangelogBytes - len(ellipsis)
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + ellipsis
	}
	return s
}

// stripControl removes ASCII control characters except newline and tab.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// sanitizeURL keeps only well-formed http/https URLs (drops javascript:, data:,
// relative, and malformed URLs).
func sanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}
