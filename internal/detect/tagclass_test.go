package detect

import "testing"

func TestClassifyTag(t *testing.T) {
	cases := []struct {
		ref  string
		want TagClass
	}{
		{"nginx:latest", TagFloating},
		{"nginx", TagFloating},          // implicit :latest
		{"nginx:1", TagFloating},        // major-only
		{"nginx:1.31", TagFloating},     // major.minor
		{"nginx:1.31.2", TagExact},      // full semver
		{"nginx:v1.31.2", TagExact},     // v-prefixed
		{"nginx:1.31.2-alpine", TagExact},
		{"redis:8.8.0", TagExact},
		{"nginx:stable", TagFloating},   // named tag
		{"nginx:1.31.2@sha256:abc", TagDigest},
		{"nginx@sha256:abc", TagDigest},
		{"ghcr.io/org/app:1.2.3", TagExact},
		{"ghcr.io/org/app:main", TagFloating},
	}
	for _, c := range cases {
		if got := ClassifyTag(c.ref); got != c.want {
			t.Errorf("ClassifyTag(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}
