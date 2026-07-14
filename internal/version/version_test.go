package version_test

import (
	"regexp"
	"testing"

	"dockbrr/internal/version"
)

func TestVersionIsSemverish(t *testing.T) {
	re := regexp.MustCompile(`^\d+\.\d+\.\d+`)
	if !re.MatchString(version.Version) {
		t.Fatalf("version %q is not semver-ish", version.Version)
	}
}
