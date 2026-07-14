package discovery_test

import (
	"context"
	"errors"
	"testing"

	"dockbrr/internal/discovery"
	"dockbrr/internal/docker"
)

func TestLocatorFindsRecreatedContainers(t *testing.T) {
	fc := &fakeCollector{containers: []docker.Container{
		{ID: "new1", Project: "web", Service: "app", RepoDigest: "sha256:new"},
		{ID: "other", Project: "web", Service: "db", RepoDigest: "sha256:db"},
	}}
	loc := discovery.NewLocator(fc)
	ids, digest, err := loc.LocateService(context.Background(), "web", "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "new1" {
		t.Fatalf("ids = %v, want [new1]", ids)
	}
	if digest != "sha256:new" {
		t.Fatalf("digest = %q, want sha256:new", digest)
	}
}

func TestLocatorMissingServiceReturnsEmpty(t *testing.T) {
	loc := discovery.NewLocator(&fakeCollector{})
	ids, digest, err := loc.LocateService(context.Background(), "web", "app")
	if err != nil {
		t.Fatal(err)
	}
	if ids != nil || digest != "" {
		t.Fatalf("expected empty result, got ids=%v digest=%q", ids, digest)
	}
}

func TestLocatorPropagatesCollectError(t *testing.T) {
	loc := discovery.NewLocator(&fakeCollector{err: errors.New("boom")})
	if _, _, err := loc.LocateService(context.Background(), "web", "app"); err == nil {
		t.Fatal("expected Collect error to propagate")
	}
}
