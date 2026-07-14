package detect

import (
	"regexp"
	"strings"
)

// TagClass describes how tightly a user pinned an image tag. It drives whether
// apply rewrites the compose file (exact only) and whether the container is
// digest-pinned at runtime.
type TagClass int

const (
	TagFloating TagClass = iota // latest / named / partial semver (1, 1.31), tracks a stream
	TagExact                    // full semver (1.31.2, optionally -pre/+build)
	TagDigest                   // ref carries @sha256:, an explicit digest pin
)

// exactSemverRe matches a fully-specified semver tag: three numeric components,
// optional leading v, optional pre-release/build metadata.
var exactSemverRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[-+].*)?$`)

// ClassifyTag classifies an image ref by pin granularity. A ref carrying a
// digest is TagDigest regardless of tag; otherwise a full-semver tag is
// TagExact and everything else (latest, named tags, partial semver like "1" or
// "1.31") is TagFloating.
func ClassifyTag(ref string) TagClass {
	if strings.Contains(ref, "@sha256:") {
		return TagDigest
	}
	_, tag := SplitRef(ref)
	if exactSemverRe.MatchString(tag) {
		return TagExact
	}
	return TagFloating
}
