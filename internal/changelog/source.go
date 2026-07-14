// Package changelog enriches a detected update with changelog text and/or a
// link via an ordered fallback chain of Sources. It is read-only enrichment:
// it performs HTTP GETs against changelog hosts (GitHub, Docker Hub) and reads
// the Phase-3 registry labels. It never mutates Docker and never pulls.
package changelog

import (
	"context"

	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// Source is one provider in the ordered changelog fallback chain.
type Source interface {
	// Name is a short identifier used in logs.
	Name() string
	// Resolve attempts to produce changelog text and/or a URL for the update.
	// An empty Result (both fields "") means "miss, try the next source". A
	// returned error is logged and treated as a miss; it never aborts the chain.
	Resolve(ctx context.Context, in Input) (Result, error)
}

// Input is the resolved context handed to each Source. It is built once by the
// resolver from the update and its remote image. FromVersion+Version bound the
// range a source should report on: notes for every release in (FromVersion,
// Version]. FromVersion is empty for a digest-only update (nothing to span).
type Input struct {
	Update      store.Update
	Image       registry.RemoteImage
	Repo        string // bare repository from Image.Ref (no tag, no @digest)
	FromVersion string // running version: Update.FromVersion ("" when unknown)
	Version     string // target version: Update.ToVersion, else Update.Tag
}

// Result is a single source's output. Either field may be empty.
type Result struct {
	Text string
	URL  string
}

// SourceInfo is the VCS source for an image, parsed from its OCI labels.
type SourceInfo struct {
	URL   string // raw source/vcs URL from the labels
	Host  string // e.g. "github.com"
	Owner string // e.g. "acme"
	Name  string // e.g. "web"
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
