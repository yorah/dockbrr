# Variant-aware changelog matching Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve GitHub Release changelog notes for LinuxServer.io images whose release tags embed a second version and a variant flavor (e.g. `libtorrentv1-5.2.3_v1.2.20-ls126`), picking the notes matching the image's flavor.

**Architecture:** Two package-local additions in `internal/changelog`: teach `normalizeTag` to truncate at `_` so the app-core parses, and add a flavor pre-filter (`extractFlavor` + `filterByFlavor`) applied once in `Resolve` so both the single-release and span selection paths operate on a flavor-consistent subset. No changes to the `detect` package.

**Tech Stack:** Go 1.26, standard `testing`, `httptest`.

**Spec:** `docs/dev/specs/2026-07-23-changelog-variant-aware-matching-design.md`

## Global Constraints

- CGO_ENABLED=0, static-binary invariant: no new dependencies.
- Changes confined to `internal/changelog`; `internal/detect` untouched.
- Verify with `mise run check` (go vet + go test + web vitest) before completion.

## Task summary table

| # | Task | Deps | Model | Reviewer | Plan section |
|---|------|------|-------|----------|--------------|
| 1 | variant-aware matching | , | sonnet | sonnet | this file |

---

### Task 1: Variant-aware changelog matching

**Files:**
- Modify: `internal/changelog/github.go` (`normalizeTag`; add `buildSuffixRe`, `extractFlavor`, `filterByFlavor`; one call site in `Resolve`)
- Test: `internal/changelog/repo_internal_test.go` (unit: `normalizeTag` `_` case, `extractFlavor`, `filterByFlavor`)
- Test: `internal/changelog/github_test.go` (integration: single-target + range, variant isolation)

**Interfaces:**
- Produces (package-internal):
  - `extractFlavor(version string) string` — variant flavor from a version's suffix, or `""`.
  - `filterByFlavor(rels []ghRelease, flavor string) []ghRelease` — keeps only flavor-matching releases when the flavor matches at least one, else returns `rels` unchanged.
  - `normalizeTag(tag string) string` — now also truncates at the first `_`.

- [ ] **Step 1: Write the failing unit tests**

Add to `internal/changelog/repo_internal_test.go`:

```go
func TestExtractFlavor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"5.2.3-libtorrentv1", "libtorrentv1"},
		{"5.2.3-alpine", "alpine"},
		{"1.10.2-ls183", ""},
		{"6.3.0.10514-ls311", ""},
		{"5.2.3", ""},
		{"v1.2.3-rc1", ""},
		{"master-omnibus", ""},
	}
	for _, c := range cases {
		if got := extractFlavor(c.in); got != c.want {
			t.Errorf("extractFlavor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFilterByFlavor(t *testing.T) {
	rels := []ghRelease{
		{TagName: "libtorrentv1-5.2.3_v1.2.20-ls126"},
		{TagName: "5.2.3_v2.0.13-ls469"},
	}
	got := filterByFlavor(rels, "libtorrentv1")
	if len(got) != 1 || got[0].TagName != "libtorrentv1-5.2.3_v1.2.20-ls126" {
		t.Errorf("got %+v, want only the libtorrentv1 release", got)
	}
	if got := filterByFlavor(rels, ""); len(got) != 2 {
		t.Errorf("empty flavor must not filter, got %d", len(got))
	}
	if got := filterByFlavor(rels, "alpine"); len(got) != 2 {
		t.Errorf("non-matching flavor must not filter (monotonic safety), got %d", len(got))
	}
}
```

And add one case to the existing `TestNormalizeTag` table (in the same file):

```go
		{"libtorrentv1-5.2.3_v1.2.20-ls126", "5.2.3"},
```

- [ ] **Step 2: Run the unit tests to verify they fail**

Run: `cd /home/yorah/projects/dockbrr && go test ./internal/changelog/ -run 'TestExtractFlavor|TestFilterByFlavor|TestNormalizeTag' -v`
Expected: build/compile failure (`undefined: extractFlavor`, `undefined: filterByFlavor`), and once those compile, `TestNormalizeTag` fails on the new `_` case.

- [ ] **Step 3: Implement `normalizeTag` `_` truncation + `extractFlavor` + `filterByFlavor`**

In `internal/changelog/github.go`, replace `normalizeTag`:

```go
// normalizeTag strips the release-tag decorations dockbrr treats as noise
// (a leading "<name>-" package prefix, "release-", "v") and truncates at the
// first "_", which precedes a downstream second version in LinuxServer.io's
// dual-version tags ("libtorrentv1-5.2.3_v1.2.20-ls126" -> "5.2.3"), so the
// app-core can be parsed as semver. "znc-1.10.2-ls183" -> "1.10.2-ls183",
// "release-1.31.2" -> "1.31.2".
func normalizeTag(tag string) string {
	tag = detect.StripNamePrefix(tag)
	tag = strings.TrimPrefix(strings.TrimPrefix(tag, "release-"), "v")
	if i := strings.IndexByte(tag, '_'); i >= 0 {
		tag = tag[:i]
	}
	return tag
}
```

Add, near `normalizeTag` (e.g. just below it):

```go
// buildSuffixRe matches the version suffixes that are build counters, not
// variant flavors: LinuxServer.io's "lsNNN". extractFlavor treats such a
// segment (and any pre-release marker) as "no flavor".
var buildSuffixRe = regexp.MustCompile(`(?i)^ls\d+$`)

// extractFlavor returns the variant flavor encoded in a version's suffix, or ""
// when there is none. The flavor is the first "-"-separated segment after a
// numeric core, unless that segment is a build counter (lsNNN) or a pre-release
// marker (rc/beta/...), which name a build rather than a variant. Examples:
// "5.2.3-libtorrentv1" -> "libtorrentv1", "5.2.3-alpine" -> "alpine",
// "1.10.2-ls183" -> "", "5.2.3" -> "", "master-omnibus" -> "".
func extractFlavor(version string) string {
	v := detect.StripNamePrefix(strings.TrimSpace(version))
	v = strings.TrimPrefix(v, "v")
	i := strings.IndexByte(v, '-')
	if i < 0 {
		return ""
	}
	if _, ok := detect.ParseCore(v[:i]); !ok {
		return "" // suffix does not follow a numeric core: not a flavor
	}
	seg := v[i+1:]
	if j := strings.IndexByte(seg, '-'); j >= 0 {
		seg = seg[:j]
	}
	if seg == "" || buildSuffixRe.MatchString(seg) || prereleaseRe.MatchString(seg) {
		return ""
	}
	return seg
}

// filterByFlavor narrows rels to the releases whose tag carries flavor, so a
// flavored image (e.g. "libtorrentv1") resolves to its own variant's notes and
// not a co-published sibling variant that shares the same app-core. It only
// narrows when flavor is non-empty AND at least one release matches, so an image
// with no flavor, or a flavor absent from every release, keeps today's behavior.
func filterByFlavor(rels []ghRelease, flavor string) []ghRelease {
	if flavor == "" {
		return rels
	}
	var kept []ghRelease
	for _, rel := range rels {
		if strings.Contains(rel.TagName, flavor) {
			kept = append(kept, rel)
		}
	}
	if len(kept) == 0 {
		return rels
	}
	return kept
}
```

(`regexp`, `strings`, and `dockbrr/internal/detect` are already imported in this file.)

- [ ] **Step 4: Run the unit tests to verify they pass**

Run: `cd /home/yorah/projects/dockbrr && go test ./internal/changelog/ -run 'TestExtractFlavor|TestFilterByFlavor|TestNormalizeTag' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing integration tests**

Add to `internal/changelog/github_test.go`:

```go
func TestGitHubLinuxServerDualVersionFlavor(t *testing.T) {
	// qbittorrent ships two libtorrent variants sharing app-core 5.2.3; a
	// libtorrentv1 image must resolve to the v1 release notes, not the v2 sibling.
	srv := ghServer(t, map[string][]ghRel{
		"linuxserver/docker-qbittorrent": {
			{TagName: "5.2.3_v2.0.13-ls469", HTMLURL: "v2url", Body: "v2 notes"},
			{TagName: "libtorrentv1-5.2.3_v1.2.20-ls126", HTMLURL: "https://github.com/linuxserver/docker-qbittorrent/releases/tag/libtorrentv1-5.2.3_v1.2.20-ls126", Body: "libtorrent v1 notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	in := changelog.Input{
		Image: registry.RemoteImage{
			Ref:    "ghcr.io/linuxserver/qbittorrent:5.2.3-libtorrentv1",
			Labels: map[string]string{"org.opencontainers.image.source": "https://github.com/linuxserver/docker-qbittorrent"},
		},
		Version: "5.2.3-libtorrentv1",
	}
	res, err := s.Resolve(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "libtorrent v1 notes") {
		t.Errorf("Text = %q, want the v1 notes", res.Text)
	}
	if strings.Contains(res.Text, "v2 notes") {
		t.Errorf("Text = %q, must not contain the v2 sibling notes", res.Text)
	}
	if res.URL != "https://github.com/linuxserver/docker-qbittorrent/releases/tag/libtorrentv1-5.2.3_v1.2.20-ls126" {
		t.Errorf("URL = %q, want the v1 release url", res.URL)
	}
}

func TestGitHubLinuxServerDualVersionRange(t *testing.T) {
	// 5.2.2 -> 5.2.3 libtorrentv1: the span must stay within the v1 variant.
	srv := ghServer(t, map[string][]ghRel{
		"linuxserver/docker-qbittorrent": {
			{TagName: "5.2.3_v2.0.13-ls469", HTMLURL: "v2new", Body: "v2 latest notes"},
			{TagName: "libtorrentv1-5.2.3_v1.2.20-ls126", HTMLURL: "https://github.com/linuxserver/docker-qbittorrent/releases/tag/libtorrentv1-5.2.3_v1.2.20-ls126", Body: "v1 five two three notes"},
			{TagName: "5.2.2_v2.0.13-ls465", HTMLURL: "v2old", Body: "v2 old notes"},
			{TagName: "libtorrentv1-5.2.2_v1.2.20-ls123", HTMLURL: "v1old", Body: "v1 five two two notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	in := changelog.Input{
		Image: registry.RemoteImage{
			Ref:    "ghcr.io/linuxserver/qbittorrent:5.2.3-libtorrentv1",
			Labels: map[string]string{"org.opencontainers.image.source": "https://github.com/linuxserver/docker-qbittorrent"},
		},
		FromVersion: "5.2.2-libtorrentv1",
		Version:     "5.2.3-libtorrentv1",
	}
	res, err := s.Resolve(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "v1 five two three notes") {
		t.Errorf("Text = %q, want the v1 5.2.3 notes", res.Text)
	}
	if strings.Contains(res.Text, "v2 latest notes") || strings.Contains(res.Text, "v2 old notes") {
		t.Errorf("Text = %q, must not contain v2 notes", res.Text)
	}
}
```

- [ ] **Step 6: Run the integration tests to verify they fail**

Run: `cd /home/yorah/projects/dockbrr && go test ./internal/changelog/ -run 'TestGitHubLinuxServerDualVersion' -v`
Expected: FAIL (empty `Text`/`URL`: today no release matches, so the assertions on the v1 notes fail).

- [ ] **Step 7: Wire the flavor filter into `Resolve`**

In `internal/changelog/github.go`, in `Resolve`, immediately after the repo-exists guard:

```go
	if !exists {
		return Result{}, nil // repo does not exist: defer
	}
```

insert:

```go
	// Keep only releases matching the image's variant flavor (e.g. libtorrentv1),
	// so a dual-version image does not resolve to a co-published sibling variant
	// that shares its app-core. No-op when the image has no flavor.
	rels = filterByFlavor(rels, extractFlavor(in.Version))
```

- [ ] **Step 8: Run the integration tests to verify they pass**

Run: `cd /home/yorah/projects/dockbrr && go test ./internal/changelog/ -run 'TestGitHubLinuxServerDualVersion' -v`
Expected: PASS.

- [ ] **Step 9: Run the full changelog + detect suites (regression guard)**

Run: `cd /home/yorah/projects/dockbrr && go test ./internal/changelog/... ./internal/detect/...`
Expected: PASS (znc, radarr, postgres, rolling-tag, redis paths unchanged).

- [ ] **Step 10: Run the full check**

Run: `cd /home/yorah/projects/dockbrr && mise run check`
Expected: go vet + go test + web vitest all green.

- [ ] **Step 11: Commit**

```bash
cd /home/yorah/projects/dockbrr
git add internal/changelog/github.go internal/changelog/github_test.go internal/changelog/repo_internal_test.go
git commit -m "fix(changelog): resolve notes for dual-version variant tags

LinuxServer.io images whose release tags embed a second version and a
variant flavor (libtorrentv1-5.2.3_v1.2.20-ls126) showed no changelog:
the embedded _v1.2.20 made the app-core unparseable, so no release
matched. normalizeTag now truncates at _, and a flavor pre-filter keeps
the image's own variant so v1 images do not surface v2 sibling notes."
```

## Self-Review

- **Spec coverage:** `_`-aware `normalizeTag` (spec 1) -> Step 3; `extractFlavor`/`filterByFlavor` pre-filter in `Resolve` (spec 2) -> Steps 3, 7; monotonic safety -> `TestFilterByFlavor` non-matching case; single + range + variant isolation -> Steps 5-8; regression guard -> Step 9. No `detect` changes -> honored.
- **Placeholder scan:** none; all steps carry real code and exact commands.
- **Type consistency:** `extractFlavor`/`filterByFlavor`/`normalizeTag` signatures identical across the plan; `ghRel` (test) vs `ghRelease` (prod) used correctly per file.
