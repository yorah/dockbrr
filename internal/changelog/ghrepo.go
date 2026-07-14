package changelog

import "strings"

// target is a resolved GitHub repository plus the candidate release tags to try
// for a given image version.
type target struct {
	Owner string
	Name  string
	tags  func(version string) []string
}

// defaultTags is the broad, tolerant tag-candidate set for the authoritative and
// heuristic tiers: plain, v-prefixed, and release-prefixed (nginx-style). The
// leading "v" is stripped first so the three forms are canonical.
func defaultTags(v string) []string {
	v = strings.TrimPrefix(v, "v")
	return []string{v, "v" + v, "release-" + v}
}

// postgresTags matches PostgreSQL's REL_16_1 tag scheme (dots -> underscores),
// with plain / v-prefixed fallbacks. Beta/rc tags (REL_17_BETA1) are not
// covered, so those fall through to the Docker Hub source.
func postgresTags(v string) []string {
	v = strings.TrimPrefix(v, "v")
	return []string{"REL_" + strings.ReplaceAll(v, ".", "_"), v, "v" + v}
}

// curatedRepos overrides the heuristics for official images whose GitHub repo
// name differs from the image name, or whose release tags use an exotic scheme.
// Keyed by normalized Hub repo (see normalizeHubRepo). nginx and redis are
// intentionally absent: the library/X->X/X heuristic plus defaultTags' release-
// form resolves them.
var curatedRepos = map[string]target{
	"library/node":     {Owner: "nodejs", Name: "node", tags: defaultTags},
	"library/python":   {Owner: "python", Name: "cpython", tags: defaultTags},
	"library/golang":   {Owner: "golang", Name: "go", tags: defaultTags},
	"library/postgres": {Owner: "postgres", Name: "postgres", tags: postgresTags},
}

// githubTarget resolves an image reference plus its OCI labels to a GitHub
// target. ok=false means no repo could be determined (the caller defers). It
// makes no network calls; repo existence is confirmed later by the releases
// fetch. Tiers, first match wins:
//
//	1-2 a self-declared github.com VCS source label (OCI or legacy)
//	3   ghcr.io host (the registry is GitHub)
//	4   curated override map (name remaps, odd tag schemes)
//	5   namespaced Hub vendor image (ns/name -> ns/name)
//	6   official library image (library/X -> X/X)
func githubTarget(ref string, labels map[string]string) (target, bool) {
	if si := parseSource(labels); si.Host == "github.com" && si.Owner != "" && si.Name != "" {
		return target{Owner: si.Owner, Name: si.Name, tags: defaultTags}, true
	}
	host, path := splitHostPath(ref)
	if host == "ghcr.io" {
		if o, n, ok := firstTwo(path); ok {
			return target{Owner: o, Name: n, tags: defaultTags}, true
		}
		return target{}, false
	}
	hubRepo, ok := normalizeHubRepo(host, path)
	if !ok {
		return target{}, false
	}
	if t, ok := curatedRepos[hubRepo]; ok {
		return t, true
	}
	ns, name, ok := firstTwo(hubRepo)
	if !ok {
		return target{}, false
	}
	if ns != "library" {
		return target{Owner: ns, Name: name, tags: defaultTags}, true
	}
	return target{Owner: name, Name: name, tags: defaultTags}, true
}

// splitHostPath strips any :tag and @digest from ref, then separates a registry
// host (a first path segment containing "." or ":", or "localhost") from the
// remaining repository path.
func splitHostPath(ref string) (host, path string) {
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		if colon := strings.LastIndex(ref[slash:], ":"); colon >= 0 {
			ref = ref[:slash+colon]
		}
	} else if colon := strings.LastIndex(ref, ":"); colon >= 0 {
		ref = ref[:colon]
	}
	if i := strings.Index(ref, "/"); i >= 0 {
		first := ref[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			return first, ref[i+1:]
		}
	}
	return "", ref
}

// normalizeHubRepo maps a (host, path) to a normalized Docker Hub repo key
// ("<ns>/<name>"). A non-Hub host yields ok=false (tiers 4-6 do not apply). A
// bare name normalizes to library/<name>.
func normalizeHubRepo(host, path string) (string, bool) {
	switch host {
	case "", "docker.io", "index.docker.io":
	default:
		return "", false
	}
	if path == "" {
		return "", false
	}
	if !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return path, true
}

// firstTwo returns the first two "/"-separated segments of path, trimming a
// trailing ".git" from the second. ok=false if either is missing/empty.
func firstTwo(path string) (a, b string, ok bool) {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}
