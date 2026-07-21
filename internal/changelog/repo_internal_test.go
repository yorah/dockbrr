package changelog

import "testing"

func TestRepoFromRef(t *testing.T) {
	cases := []struct{ ref, want string }{
		{"ghcr.io/acme/web:1.2.3", "ghcr.io/acme/web"},
		{"ghcr.io/acme/web", "ghcr.io/acme/web"},
		{"docker.io/library/nginx:latest", "docker.io/library/nginx"},
		{"img@sha256:abc", "img"},
		{"ghcr.io/acme/web:1.2.3@sha256:abc", "ghcr.io/acme/web"},
		{"localhost:5000/app:dev", "localhost:5000/app"},
		{"localhost:5000/app", "localhost:5000/app"},
	}
	for _, c := range cases {
		if got := repoFromRef(c.ref); got != c.want {
			t.Errorf("repoFromRef(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestLatestStableRelease(t *testing.T) {
	t.Run("picks highest stable, skips prerelease", func(t *testing.T) {
		rels := []ghRelease{
			{TagName: "v0.9.0-rc1", Body: "rc"},
			{TagName: "v0.9.2", HTMLURL: "https://github.com/o/r/releases/tag/v0.9.2", Body: "notes 0.9.2"},
			{TagName: "v0.9.1", Body: "notes 0.9.1"},
		}
		got, ok := latestStableRelease(rels)
		if !ok {
			t.Fatal("want ok=true, got false")
		}
		if got.TagName != "v0.9.2" {
			t.Fatalf("want v0.9.2, got %q", got.TagName)
		}
	})

	t.Run("highest by semver, not list order", func(t *testing.T) {
		// GitHub lists by publish date; a backport (0.8.6) can precede the newest.
		rels := []ghRelease{
			{TagName: "v0.8.6"},
			{TagName: "v0.9.2"},
			{TagName: "v0.9.0"},
		}
		got, ok := latestStableRelease(rels)
		if !ok || got.TagName != "v0.9.2" {
			t.Fatalf("want v0.9.2 ok, got %q ok=%v", got.TagName, ok)
		}
	})

	t.Run("empty when prerelease-only", func(t *testing.T) {
		rels := []ghRelease{{TagName: "v1.0.0-beta.1"}, {TagName: "v1.0.0-rc2"}}
		if _, ok := latestStableRelease(rels); ok {
			t.Fatal("want ok=false for prerelease-only list")
		}
	})

	t.Run("empty when no releases", func(t *testing.T) {
		if _, ok := latestStableRelease(nil); ok {
			t.Fatal("want ok=false for empty list")
		}
	})
}
