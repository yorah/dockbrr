package changelog_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dockbrr/internal/changelog"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// failTransport / failClient are defined in github_test.go (same test package).

// fakeSource is a programmable Source for chain-runner unit tests.
type fakeSource struct {
	name   string
	res    changelog.Result
	err    error
	called *bool
}

func (f fakeSource) Name() string { return f.name }
func (f fakeSource) Resolve(_ context.Context, _ changelog.Input) (changelog.Result, error) {
	if f.called != nil {
		*f.called = true
	}
	return f.res, f.err
}

// captureSource records the Input the resolver built.
type captureSource struct{ in *changelog.Input }

func (captureSource) Name() string { return "capture" }
func (c captureSource) Resolve(_ context.Context, in changelog.Input) (changelog.Result, error) {
	*c.in = in
	return changelog.Result{URL: "https://x.example"}, nil
}

func TestResolvePassesBothVersions(t *testing.T) {
	// The range (from, to] is what sources need; the resolver must carry both.
	var got changelog.Input
	r := changelog.NewResolver([]changelog.Source{captureSource{in: &got}})
	u := store.Update{Tag: "8.8", FromVersion: "7.2", ToVersion: "8.8"}
	if _, _, err := r.Resolve(context.Background(), u, registry.RemoteImage{Ref: "redis:8.8"}); err != nil {
		t.Fatal(err)
	}
	if got.FromVersion != "7.2" || got.Version != "8.8" {
		t.Fatalf("Input from=%q to=%q, want 7.2 -> 8.8", got.FromVersion, got.Version)
	}
}

func TestResolveFirstHitWins(t *testing.T) {
	r := changelog.NewResolver([]changelog.Source{
		fakeSource{name: "a", res: changelog.Result{URL: "https://a.example"}},
		fakeSource{name: "b", res: changelog.Result{Text: "should not reach"}},
	})
	text, url, err := r.Resolve(context.Background(), store.Update{}, registry.RemoteImage{})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://a.example" || text != "" {
		t.Fatalf("got text=%q url=%q", text, url)
	}
}

func TestResolveSkipsMissAndSanitizes(t *testing.T) {
	r := changelog.NewResolver([]changelog.Source{
		fakeSource{name: "empty", res: changelog.Result{}},
		fakeSource{name: "html", res: changelog.Result{Text: "<script>evil()</script>hello <b>world</b>"}},
	})
	text, _, err := r.Resolve(context.Background(), store.Update{}, registry.RemoteImage{})
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello world" {
		t.Fatalf("sanitized text = %q, want %q", text, "hello world")
	}
}

func TestResolveErrorContinues(t *testing.T) {
	r := changelog.NewResolver([]changelog.Source{
		fakeSource{name: "boom", err: errors.New("down")},
		fakeSource{name: "ok", res: changelog.Result{URL: "https://ok.example"}},
	})
	_, url, err := r.Resolve(context.Background(), store.Update{}, registry.RemoteImage{})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://ok.example" {
		t.Fatalf("url = %q", url)
	}
}

func TestResolveUnavailable(t *testing.T) {
	r := changelog.NewResolver([]changelog.Source{
		fakeSource{name: "miss", res: changelog.Result{}},
		fakeSource{name: "bad-url", res: changelog.Result{URL: "javascript:alert(1)"}}, // sanitized away
	})
	text, url, err := r.Resolve(context.Background(), store.Update{}, registry.RemoteImage{})
	if err != nil {
		t.Fatal(err)
	}
	if text != "" || url != "" {
		t.Fatalf("expected unavailable, got text=%q url=%q", text, url)
	}
}

// Integration: the three real sources wired against fake GitHub/Hub hosts.
func TestResolveGitHubFromGHCRLabel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/web/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"v1.2.4","html_url":"https://github.com/acme/web/releases/tag/v1.2.4","body":"## 1.2.4\n- fixed a bug"}]`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sources := []changelog.Source{
		changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour),
		changelog.NewRegistrySource(srv.Client(), srv.URL),
		changelog.NewOCISource(),
	}
	r := changelog.NewResolver(sources)

	u := store.Update{Tag: "1.2.3", ToVersion: "1.2.4"}
	img := registry.RemoteImage{
		Ref:    "ghcr.io/acme/web:1.2.3",
		Labels: map[string]string{"org.opencontainers.image.source": "https://github.com/acme/web"},
	}
	text, url, err := r.Resolve(context.Background(), u, img)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/acme/web/releases/tag/v1.2.4" {
		t.Fatalf("url = %q", url)
	}
	if !strings.Contains(text, "fixed a bug") {
		t.Fatalf("text = %q", text)
	}
}

func TestResolveFallsThroughToLabelLink(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sources := []changelog.Source{
		changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour),
		changelog.NewRegistrySource(srv.Client(), srv.URL),
		changelog.NewOCISource(),
	}
	r := changelog.NewResolver(sources)
	img := registry.RemoteImage{
		Ref:    "ghcr.io/acme/web:1.2.3",
		Labels: map[string]string{"org.opencontainers.image.source": "https://github.com/acme/web"},
	}
	text, url, err := r.Resolve(context.Background(), store.Update{Tag: "1.2.3", ToVersion: "1.2.4"}, img)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/acme/web" {
		t.Fatalf("url = %q, want OCI label fallback link", url)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
}
