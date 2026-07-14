package changelog

import (
	"context"
	"net/url"
	"strings"
)

// OCISource derives a fallback changelog link from the image's OCI labels (and
// legacy label-schema labels). It is offline, the lowest-priority source,
// used only when the network sources miss.
type OCISource struct{}

// NewOCISource constructs the OCI label fallback-link source.
func NewOCISource() *OCISource { return &OCISource{} }

func (s *OCISource) Name() string { return "oci-labels" }

// Resolve returns the best label-derived link (source repo, project url, or
// documentation), preferring OCI labels over legacy label-schema. It never
// returns text: labels alone are not changelog prose.
func (s *OCISource) Resolve(_ context.Context, in Input) (Result, error) {
	labels := in.Image.Labels
	link := firstNonEmpty(
		labels["org.opencontainers.image.source"],
		labels["org.opencontainers.image.url"],
		labels["org.opencontainers.image.documentation"],
		labels["org.label-schema.vcs-url"],
		labels["org.label-schema.url"],
	)
	return Result{URL: link}, nil
}

// parseSource extracts the VCS source URL from the labels and, when it is a
// well-formed host/owner/repo URL, splits out the host, owner, and repo name
// (trailing ".git" trimmed). A non-URL source is returned as URL-only.
func parseSource(labels map[string]string) SourceInfo {
	raw := firstNonEmpty(
		labels["org.opencontainers.image.source"],
		labels["org.label-schema.vcs-url"],
	)
	si := SourceInfo{URL: raw}
	if raw == "" {
		return si
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return si
	}
	si.Host = u.Host
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		si.Owner = parts[0]
		si.Name = strings.TrimSuffix(parts[1], ".git")
	}
	return si
}

// ParseSourceForTest exposes parseSource to black-box tests in this package.
func ParseSourceForTest(labels map[string]string) SourceInfo { return parseSource(labels) }
