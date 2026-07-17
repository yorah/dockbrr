package detect_test

import (
	"testing"

	"dockbrr/internal/detect"
)

func TestSeverity(t *testing.T) {
	cases := []struct {
		from, to, want string
	}{
		{"1.2.3", "2.0.0", "major"},
		{"1.2.3", "1.3.0", "minor"},
		{"1.2.3", "1.2.4", "patch"},
		{"v1.2.3", "v1.2.4", "patch"},   // v-prefix tolerant
		{"1.2.3", "1.2.3", "digest-only"}, // equal
		{"1.2", "1.3", "minor"},           // missing patch → 0
		{"1.2.3-rc1", "1.2.3-rc2", "digest-only"}, // same core
		{"1.2.3", "1.3.0-beta", "minor"}, // pre-release on to side, core differs
		{"latest", "1.2.3", "digest-only"},        // from unparseable
		{"1.2.3", "latest", "digest-only"},        // to unparseable
		{"", "", "digest-only"},
		{"2.0.0", "1.0.0", "major"}, // downgrade still classified by highest differing field
		{"1", "2", "major"},          // single component: missing minor/patch default to 0
	}
	for _, c := range cases {
		if got := detect.Severity(c.from, c.to); got != c.want {
			t.Errorf("Severity(%q, %q) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestNewerSemverTag(t *testing.T) {
	for _, tc := range []struct {
		current string
		tags    []string
		want    string
		ok      bool
	}{
		{"1.2.3", []string{"1.2.3", "1.2.4", "1.3.0", "latest", "1.3.0-rc1"}, "1.3.0", true},
		{"v1.2.3", []string{"v1.2.4", "v1.10.0"}, "v1.10.0", true}, // numeric, not lexical (1.10 > 1.9)
		{"1.2.3", []string{"1.2.3", "1.2.2"}, "", false},           // nothing newer
		{"latest", []string{"1.0.0"}, "", false},                   // moving tag: not semver-tracked
		{"1.2.3", []string{"2.0.0-beta1"}, "", false},              // pre-releases excluded from auto-suggest
		// Stream awareness: a flavored app-version tag (a "-alpine" build) must
		// only be compared against tags sharing the same flavor. Co-hosted
		// base-image tags (20.04.1, 18.04.1) sort higher numerically but belong
		// to a different stream and must be ignored.
		{"1.2.3-alpine", []string{"1.2.4-alpine", "20.04.1", "18.04.1", "1.2.3-alpine"}, "1.2.4-alpine", true},
		{"1.2.3-alpine", []string{"20.04.1", "18.04.1"}, "", false}, // only foreign-stream tags -> nothing newer
		{"1.2.3-alpine", []string{"1.3.0-glibc"}, "", false},        // different build flavor -> different stream
		{"20.04.1", []string{"20.04.1", "1.2.3-alpine"}, "", false}, // bare current never matches flavored tags
	} {
		got, ok := detect.NewerSemverTag(tc.current, tc.tags)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NewerSemverTag(%q, %v) = (%q,%v), want (%q,%v)", tc.current, tc.tags, got, ok, tc.want, tc.ok)
		}
	}
}
