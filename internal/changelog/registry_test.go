package changelog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dockbrr/internal/changelog"
)

func hubServer(t *testing.T, descByPath map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/v2/repositories/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v2/repositories/"), "/")
		desc, ok := descByPath[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// json.Encode escapes the description safely (newlines, quotes).
		_ = json.NewEncoder(w).Encode(map[string]string{"full_description": desc})
	})
	return srv
}

func TestRegistryHubLibraryImage(t *testing.T) {
	srv := hubServer(t, map[string]string{"library/nginx": "# nginx\nthe web server"})
	s := changelog.NewRegistrySource(srv.Client(), srv.URL)
	res, err := s.Resolve(context.Background(), changelog.Input{Repo: "docker.io/library/nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "web server") {
		t.Fatalf("Text = %q", res.Text)
	}
	if res.URL != "https://hub.docker.com/_/nginx" {
		t.Fatalf("URL = %q, want library web url", res.URL)
	}
}

func TestRegistryHubBareName(t *testing.T) {
	srv := hubServer(t, map[string]string{"library/redis": "redis docs"})
	s := changelog.NewRegistrySource(srv.Client(), srv.URL)
	res, err := s.Resolve(context.Background(), changelog.Input{Repo: "redis"})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://hub.docker.com/_/redis" {
		t.Fatalf("URL = %q", res.URL)
	}
}

func TestRegistryHubNamespacedImage(t *testing.T) {
	srv := hubServer(t, map[string]string{"grafana/grafana": "grafana docs"})
	s := changelog.NewRegistrySource(srv.Client(), srv.URL)
	res, err := s.Resolve(context.Background(), changelog.Input{Repo: "docker.io/grafana/grafana"})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://hub.docker.com/r/grafana/grafana" {
		t.Fatalf("URL = %q", res.URL)
	}
}

func TestRegistryNonHubDefers(t *testing.T) {
	// ghcr.io and a registry with a port must defer without any HTTP call.
	s := changelog.NewRegistrySource(failClient(t), "http://127.0.0.1:0")
	for _, repo := range []string{"ghcr.io/acme/web", "quay.io/acme/web", "localhost:5000/app"} {
		res, err := s.Resolve(context.Background(), changelog.Input{Repo: repo})
		if err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
		if res != (changelog.Result{}) {
			t.Fatalf("%s should defer, got %+v", repo, res)
		}
	}
}
