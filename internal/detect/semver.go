// Package detect compares running images against remote registry state to
// produce update records.
package detect

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Severity classifies the change between two version strings as
// major|minor|patch, or digest-only when either side is not parseable semver
// or the two share the same (major, minor, patch) core.
//
// Parsing is lenient: a leading "v" is dropped, missing minor/patch default to
// 0, and any pre-release ("-rc1") or build ("+meta") suffix is ignored so only
// the numeric core is compared.
func Severity(from, to string) string {
	fa, fok := parseCore(from)
	ta, tok := parseCore(to)
	if !fok || !tok {
		return "digest-only"
	}
	switch {
	case fa[0] != ta[0]:
		return "major"
	case fa[1] != ta[1]:
		return "minor"
	case fa[2] != ta[2]:
		return "patch"
	default:
		return "digest-only"
	}
}

// NewerSemverTag returns the highest semver tag in tags that is strictly newer
// than current, preserving the tag's original spelling (v-prefix kept). Only
// tags in the SAME stream as current are considered: a stream is the tag's
// numeric core plus its surrounding "flavor" (leading "v" and any suffix such
// as "-alpine"). Comparing only within a stream keeps an image's app-version
// tags from being "upgraded" to an unrelated tag that a registry happens to
// co-host and that merely sorts higher, e.g. some images ship legacy Ubuntu
// base-image tags (18.04.1, 20.04.1) alongside application tags like
// 1.2.3-alpine. A differing flavor also excludes true
// pre-releases (1.2.3-rc1) from a plain-core current, so stable pins keep only
// auto-suggesting stable releases. Returns ok=false when current is not semver
// or nothing newer shares its stream.
func NewerSemverTag(current string, tags []string) (string, bool) {
	curCore, curFlavor, ok := tagStream(current)
	if !ok {
		return "", false
	}
	best, bestCore := "", curCore
	for _, t := range tags {
		c, flavor, ok := tagStream(t)
		if !ok || flavor != curFlavor {
			continue // unparseable, or a different version stream
		}
		if coreLess(bestCore, c) {
			best, bestCore = t, c
		}
	}
	return best, best != ""
}

// streamRe splits a tag into a leading "v", its numeric core (1-3 dot-separated
// integer components), and everything after it.
var streamRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,2})(.*)$`)

// tagStream splits tag into its numeric core and its "flavor" (the leading "v"
// plus any trailing suffix). Two tags belong to the same version stream iff
// their flavors are identical. ok is false when tag has no leading numeric core.
func tagStream(tag string) (core [3]int, flavor string, ok bool) {
	m := streamRe.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return core, "", false
	}
	c, ok := parseCore(m[2])
	if !ok {
		return core, "", false
	}
	return c, m[1] + m[3], true
}

// semverTagsDesc returns the stable, fully-specified semver tags (X.Y.Z, with an
// optional leading v) from tags, sorted newest-first. Pre-release/build
// (1.2.3-rc1) and partial (1, 1.31) tags are excluded. It backs the floating-tag
// reverse version-naming scan, which walks tags newest-first.
func semverTagsDesc(tags []string) []string {
	type tc struct {
		tag  string
		core [3]int
	}
	var out []tc
	for _, t := range tags {
		if !exactSemverRe.MatchString(t) || strings.ContainsAny(t, "-+") {
			continue
		}
		c, ok := parseCore(t)
		if !ok {
			continue
		}
		out = append(out, tc{t, c})
	}
	sort.Slice(out, func(i, j int) bool { return coreLess(out[j].core, out[i].core) })
	res := make([]string, 0, len(out))
	for _, e := range out {
		res = append(res, e.tag)
	}
	return res
}

// namePrefixRe matches a leading alphanumeric package-name segment followed by
// "-" and a version core (optionally v-prefixed), e.g. "znc-1.10.2-ls183" ->
// "1.10.2-ls183". The remainder must begin with an optional "v" then a digit, so
// it never eats a bare or v-prefixed version or a non-versioned word.
var namePrefixRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-(v?\d.*)$`)

// StripNamePrefix removes a leading "<name>-" package prefix from a tag/version
// string when one precedes the version core, else returns s unchanged. It is
// deliberately narrow: only an alphanumeric word directly followed by "-" and a
// version digit is stripped, so "release-1.2.3" / "znc-1.10.2-ls183" are stripped
// while "1.2.3", "v1.2.3", "master-omnibus" (no digit) stay as-is.
func StripNamePrefix(s string) string {
	if m := namePrefixRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return m[1]
	}
	return s
}

// ParseCore extracts the lenient [major, minor, patch] core of a version string
// (leading "v" dropped, missing components 0, pre-release/build suffix ignored).
// ok=false when the version is not numeric semver. It is the single source of
// truth for version ordering across packages (see also CoreLess).
func ParseCore(v string) ([3]int, bool) { return parseCore(v) }

// CoreLess reports a < b in (major, minor, patch) order.
func CoreLess(a, b [3]int) bool { return coreLess(a, b) }

// coreLess reports a < b in (major, minor, patch) order.
func coreLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// parseCore extracts the [major, minor, patch] numeric core from a version
// string. Returns ok=false if any of the three integer components cannot be
// parsed (e.g. non-numeric segment). Missing minor/patch components default to 0.
func parseCore(v string) ([3]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return [3]int{}, false
	}
	// Strip build metadata then pre-release.
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	var core [3]int
	for i := 0; i < 3; i++ {
		if i >= len(parts) {
			core[i] = 0
			continue
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}, false
		}
		core[i] = n
	}
	return core, true
}
