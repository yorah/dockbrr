package registry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"dockbrr/internal/registry"
)

// stubCredentials is a CredentialStore for testing.
type stubCredentials struct{ host, user, pass string }

func (s *stubCredentials) Lookup(h string) (string, string, bool) {
	if h == s.host {
		return s.user, s.pass, true
	}
	return "", "", false
}

// pushFixture serves an in-process registry and pushes a 1-layer image tagged
// host/repo:tag carrying the given OCI labels. It returns the pushed ref string
// and the image's digest.
func pushFixture(t *testing.T, repo, tag string, labels map[string]string) (string, string) {
	t.Helper()
	srv := httptest.NewServer(ggcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf = cf.DeepCopy()
	if cf.Config.Labels == nil {
		cf.Config.Labels = map[string]string{}
	}
	for k, v := range labels {
		cf.Config.Labels[k] = v
	}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}

	ref, err := name.ParseReference(host + "/" + repo + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	dig, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return ref.String(), dig.String()
}

func TestResolvePublicImageRoundTrip(t *testing.T) {
	ref, digest := pushFixture(t, "acme/web", "1.2.3", map[string]string{
		"org.opencontainers.image.source":  "https://github.com/acme/web",
		"org.opencontainers.image.version": "1.2.3",
	})

	r := registry.NewResolver(nil) // anonymous-only
	got, err := r.Resolve(context.Background(), ref, registry.HostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != digest {
		t.Fatalf("Digest = %q, want %q", got.Digest, digest)
	}
	if got.Labels["org.opencontainers.image.source"] != "https://github.com/acme/web" {
		t.Fatalf("source label = %q", got.Labels["org.opencontainers.image.source"])
	}
	if got.Labels["org.opencontainers.image.version"] != "1.2.3" {
		t.Fatalf("version label = %q", got.Labels["org.opencontainers.image.version"])
	}
}

func TestListTagsReturnsPushedTags(t *testing.T) {
	ref, _ := pushFixture(t, "acme/web", "1.2.3", nil)
	repo := strings.TrimSuffix(ref, ":1.2.3")

	// Push a second tag into the same in-process repository.
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	tagRef, err := name.ParseReference(repo + ":1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatal(err)
	}

	r := registry.NewResolver(nil)
	tags, err := r.ListTags(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tg := range tags {
		got[tg] = true
	}
	if !got["1.2.3"] || !got["1.3.0"] {
		t.Fatalf("ListTags = %v, want both 1.2.3 and 1.3.0", tags)
	}
}

// TestListTagsUnknownRepoErrors verifies a non-existent repository surfaces an
// error rather than an empty, misleadingly-successful tag list.
func TestListTagsUnknownRepoErrors(t *testing.T) {
	ref, _ := pushFixture(t, "acme/web", "1.2.3", nil)
	repo := strings.TrimSuffix(ref, ":1.2.3")
	missingRepo := strings.Replace(repo, "acme/web", "acme/does-not-exist", 1)

	r := registry.NewResolver(nil)
	if _, err := r.ListTags(context.Background(), missingRepo); err == nil {
		t.Fatal("expected error listing tags for a non-existent repository")
	}
}

func TestResolveUnknownTagErrors(t *testing.T) {
	ref, _ := pushFixture(t, "acme/web", "1.2.3", nil)
	missing := strings.Replace(ref, ":1.2.3", ":9.9.9", 1)
	r := registry.NewResolver(nil)
	if _, err := r.Resolve(context.Background(), missing, registry.HostPlatform()); err == nil {
		t.Fatal("expected error resolving a non-existent tag")
	}
}

// TestResolveCredsOn401Retry verifies the anonymous-first→401→retry-with-creds
// path in registry.get(). An in-process registry rejects requests without an
// Authorization header so only a credentialed resolver succeeds.
func TestResolveCredsOn401Retry(t *testing.T) {
	// Build an auth-protected in-process registry.
	// Requests without Authorization → 401 Basic challenge.
	// Requests with any Authorization header → delegate to ggcr registry.
	// (go-containerregistry's basicTransport never sends an Authorization
	// header when the authenticator is authn.Anonymous, so the anonymous path
	// always receives 401 from this handler.)
	inner := ggcrregistry.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	// Push a fixture image with credentials (u:p).
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(host + "/acme/web:1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img, remote.WithAuth(authn.FromConfig(authn.AuthConfig{Username: "u", Password: "p"}))); err != nil {
		t.Fatalf("push fixture with creds: %v", err)
	}
	dig, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}

	creds := &stubCredentials{host: host, user: "u", pass: "p"}

	// Path 1: resolver with credentials, must succeed after 401 retry.
	r := registry.NewResolver(creds)
	got, err := r.Resolve(context.Background(), ref.String(), registry.HostPlatform())
	if err != nil {
		t.Fatalf("Resolve with creds: %v", err)
	}
	if got.Digest != dig.String() {
		t.Fatalf("Digest = %q, want %q", got.Digest, dig.String())
	}

	// Path 2: resolver with no credentials, must fail and be marked unauthorized.
	_, err = registry.NewResolver(nil).Resolve(context.Background(), ref.String(), registry.HostPlatform())
	if err == nil {
		t.Fatal("expected error resolving against auth-only registry with no creds")
	}
	if !registry.IsUnauthorized(err) {
		t.Fatalf("IsUnauthorized(%v) = false, want true", err)
	}
}
