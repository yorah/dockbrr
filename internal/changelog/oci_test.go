package changelog_test

import (
	"context"
	"testing"

	"dockbrr/internal/changelog"
	"dockbrr/internal/registry"
)

func TestParseSource(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   changelog.SourceInfo
	}{
		{
			name:   "github source",
			labels: map[string]string{"org.opencontainers.image.source": "https://github.com/acme/web"},
			want:   changelog.SourceInfo{URL: "https://github.com/acme/web", Host: "github.com", Owner: "acme", Name: "web"},
		},
		{
			name:   "trailing .git trimmed",
			labels: map[string]string{"org.opencontainers.image.source": "https://github.com/acme/web.git"},
			want:   changelog.SourceInfo{URL: "https://github.com/acme/web.git", Host: "github.com", Owner: "acme", Name: "web"},
		},
		{
			name:   "legacy label-schema vcs-url",
			labels: map[string]string{"org.label-schema.vcs-url": "https://github.com/acme/legacy"},
			want:   changelog.SourceInfo{URL: "https://github.com/acme/legacy", Host: "github.com", Owner: "acme", Name: "legacy"},
		},
		{
			name:   "gitlab host preserved",
			labels: map[string]string{"org.opencontainers.image.source": "https://gitlab.com/grp/proj"},
			want:   changelog.SourceInfo{URL: "https://gitlab.com/grp/proj", Host: "gitlab.com", Owner: "grp", Name: "proj"},
		},
		{
			name:   "non-url source kept as URL only",
			labels: map[string]string{"org.opencontainers.image.source": "not a url"},
			want:   changelog.SourceInfo{URL: "not a url"},
		},
		{
			name:   "no source label",
			labels: map[string]string{"org.opencontainers.image.version": "1.2.3"},
			want:   changelog.SourceInfo{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := changelog.ParseSourceForTest(c.labels)
			if got != c.want {
				t.Fatalf("parseSource = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestOCISourceFallbackLink(t *testing.T) {
	s := changelog.NewOCISource()
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"source wins", map[string]string{
			"org.opencontainers.image.source": "https://github.com/acme/web",
			"org.opencontainers.image.url":    "https://example.com",
		}, "https://github.com/acme/web"},
		{"url fallback", map[string]string{
			"org.opencontainers.image.url": "https://example.com/proj",
		}, "https://example.com/proj"},
		{"documentation fallback", map[string]string{
			"org.opencontainers.image.documentation": "https://docs.example.com",
		}, "https://docs.example.com"},
		{"legacy vcs-url", map[string]string{
			"org.label-schema.vcs-url": "https://github.com/acme/legacy",
		}, "https://github.com/acme/legacy"},
		{"legacy url", map[string]string{
			"org.label-schema.url": "https://legacy.example.com",
		}, "https://legacy.example.com"},
		{"no labels", map[string]string{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := s.Resolve(context.Background(), changelog.Input{
				Image: registry.RemoteImage{Labels: c.labels},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.URL != c.want {
				t.Fatalf("URL = %q, want %q", res.URL, c.want)
			}
			if res.Text != "" {
				t.Fatalf("OCI source must not return text, got %q", res.Text)
			}
		})
	}
}
