package changelog

import (
	"reflect"
	"testing"
)

func TestGithubTarget(t *testing.T) {
	cases := []struct {
		name      string
		ref       string
		labels    map[string]string
		wantOK    bool
		wantOwner string
		wantName  string
	}{
		{
			name:   "oci source label wins over heuristic",
			ref:    "library/nginx",
			labels: map[string]string{"org.opencontainers.image.source": "https://github.com/acme/custom"},
			wantOK: true, wantOwner: "acme", wantName: "custom",
		},
		{
			name:   "legacy label-schema vcs-url",
			ref:    "someimage",
			labels: map[string]string{"org.label-schema.vcs-url": "https://github.com/acme/legacy.git"},
			wantOK: true, wantOwner: "acme", wantName: "legacy",
		},
		{
			name:   "ghcr host",
			ref:    "ghcr.io/immich-app/immich-server:v1.100.0",
			wantOK: true, wantOwner: "immich-app", wantName: "immich-server",
		},
		{
			name:   "curated remap node",
			ref:    "node:22",
			wantOK: true, wantOwner: "nodejs", wantName: "node",
		},
		{
			name:   "curated remap postgres",
			ref:    "library/postgres:16.1",
			wantOK: true, wantOwner: "postgres", wantName: "postgres",
		},
		{
			name:   "curated remap python",
			ref:    "python:3.12",
			wantOK: true, wantOwner: "python", wantName: "cpython",
		},
		{
			name:   "curated remap golang",
			ref:    "golang:1.22",
			wantOK: true, wantOwner: "golang", wantName: "go",
		},
		{
			name:   "namespaced vendor image",
			ref:    "grafana/grafana:11.0.0",
			wantOK: true, wantOwner: "grafana", wantName: "grafana",
		},
		{
			name:   "official library nginx",
			ref:    "nginx:1.25.0",
			wantOK: true, wantOwner: "nginx", wantName: "nginx",
		},
		{
			name:   "official library redis",
			ref:    "redis:7.2.0",
			wantOK: true, wantOwner: "redis", wantName: "redis",
		},
		{
			name:   "non-hub non-ghcr registry defers",
			ref:    "quay.io/prometheus/prometheus:v2.50.0",
			wantOK: false,
		},
		{
			name:   "non-github label defers to heuristic",
			ref:    "gitlab.com/x/y", // treated as a Hub ns "gitlab.com"? no: host has a dot
			labels: map[string]string{"org.opencontainers.image.source": "https://gitlab.com/g/p"},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := githubTarget(tc.ref, tc.labels)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Owner != tc.wantOwner || got.Name != tc.wantName {
				t.Fatalf("owner/name = %q/%q, want %q/%q", got.Owner, got.Name, tc.wantOwner, tc.wantName)
			}
			if got.tags == nil {
				t.Fatal("tags func is nil")
			}
		})
	}
}

func TestDefaultTags(t *testing.T) {
	if got := defaultTags("1.31.2"); !reflect.DeepEqual(got, []string{"1.31.2", "v1.31.2", "release-1.31.2"}) {
		t.Fatalf("defaultTags = %v", got)
	}
	if got := defaultTags("v2.0.0"); !reflect.DeepEqual(got, []string{"2.0.0", "v2.0.0", "release-2.0.0"}) {
		t.Fatalf("defaultTags(v-prefixed) = %v", got)
	}
}

func TestPostgresTags(t *testing.T) {
	got := postgresTags("16.1")
	if len(got) == 0 || got[0] != "REL_16_1" {
		t.Fatalf("postgresTags = %v, want first REL_16_1", got)
	}
}
