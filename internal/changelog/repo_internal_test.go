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
