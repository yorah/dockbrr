# LSIO-style tag changelog + version display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve changelog notes for LinuxServer.io images whose release tags are `<name>-<semver>-lsNNN`, and stop the version-display reverse-match from downgrading a suffix-bearing OCI label to a bare `X.Y.Z`.

**Architecture:** One shared helper `detect.StripNamePrefix` strips a leading package-name prefix so name-prefixed tags parse as semver. The changelog source consumes it in `normalizeTag` and adds a core-equality release match; the version detector consumes it in a `preferDigestTag` gate that keeps the richer label when it names the same version as the digest-matched tag.

**Tech Stack:** Go 1.26 (CGO_ENABLED=0), standard library only. Tests: `go test`.

## Global Constraints

- CGO_ENABLED=0; standard library only, no new dependencies.
- `go vet ./... && go test ./...` must pass.
- The canonical `detect.parseCore` / `ParseCore` is NOT modified (broad blast radius: `Severity`, `tagStream`, `semverTagsDesc`, `NewerSemverTag`).
- Spec: `docs/dev/specs/2026-07-21-lsio-tag-changelog-and-version-display-design.md`.

---

### Task 1: `detect.StripNamePrefix` shared helper

**Files:**
- Modify: `internal/detect/semver.go` (add helper + `namePrefixRe`; `regexp` already imported)
- Test: `internal/detect/semver_test.go` (add `TestStripNamePrefix`)

**Interfaces:**
- Consumes: nothing.
- Produces: `func StripNamePrefix(s string) string` — strips a leading `<alphanumeric-word>-` when it directly precedes an optional `v` then a version digit; returns the input unchanged otherwise.

- [ ] **Step 1: Write the failing test**

Add to `internal/detect/semver_test.go`:

```go
func TestStripNamePrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"znc-1.10.2-ls183", "1.10.2-ls183"},
		{"release-1.2.3", "1.2.3"},
		{"radarr-v6.3.0.10514-ls311", "v6.3.0.10514-ls311"},
		{"1.2.3", "1.2.3"},
		{"v1.2.3", "v1.2.3"},
		{"6.3.0.10514-ls311", "6.3.0.10514-ls311"},
		{"master-omnibus", "master-omnibus"},
		{"", ""},
	}
	for _, c := range cases {
		if got := StripNamePrefix(c.in); got != c.want {
			t.Errorf("StripNamePrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/detect/ -run TestStripNamePrefix -v`
Expected: FAIL — `undefined: StripNamePrefix`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/detect/semver.go` (near `ParseCore`, after the `streamRe` block):

```go
// namePrefixRe matches a leading alphanumeric package-name segment followed by
// "-" and a version core (optionally v-prefixed), e.g. "znc-1.10.2-ls183" ->
// "1.10.2-ls183". The remainder must begin with an optional "v" then a digit, so
// it never eats a bare or v-prefixed version or a non-versioned word.
var namePrefixRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-(v?\d.*)$`)

// StripNamePrefix removes a leading "<name>-" package prefix from a tag/version
// string when one precedes the version core, else returns s unchanged. It is
// deliberately narrow: only an alphanumeric word directly followed by "-" and a
// version digit is stripped, so "release-1.2.3" / "znc-1.10.2-ls183" are stripped
// while "1.2.3", "v1.2.3", "master-omnibus" (no digit) stay as-is.
func StripNamePrefix(s string) string {
	if m := namePrefixRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return m[1]
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/detect/ -run TestStripNamePrefix -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/detect/semver.go internal/detect/semver_test.go
git commit -m "feat(detect): add StripNamePrefix for name-prefixed version tags"
```

---

### Task 2: changelog `normalizeTag` strips the name prefix (B1)

**Files:**
- Modify: `internal/changelog/github.go:355-357` (`normalizeTag`; `detect` already imported)
- Test: `internal/changelog/repo_internal_test.go` (add `TestNormalizeTag`)

**Interfaces:**
- Consumes: `detect.StripNamePrefix` (Task 1).
- Produces: `normalizeTag("znc-1.10.2-ls183") == "1.10.2-ls183"` (unexported; relied on by Task 3 and existing core-based helpers).

- [ ] **Step 1: Write the failing test**

Add to `internal/changelog/repo_internal_test.go`:

```go
func TestNormalizeTag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"znc-1.10.2-ls183", "1.10.2-ls183"},
		{"release-1.31.2", "1.31.2"},
		{"v1.31.2", "1.31.2"},
		{"1.31.2", "1.31.2"},
		{"6.3.0.10514-ls311", "6.3.0.10514-ls311"},
	}
	for _, c := range cases {
		if got := normalizeTag(c.in); got != c.want {
			t.Errorf("normalizeTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/changelog/ -run TestNormalizeTag -v`
Expected: FAIL — `normalizeTag("znc-1.10.2-ls183")` returns `"znc-1.10.2-ls183"`.

- [ ] **Step 3: Write minimal implementation**

Replace `normalizeTag` in `internal/changelog/github.go`:

```go
// normalizeTag strips the release-tag decorations dockbrr treats as noise
// (a leading "<name>-" package prefix, "release-", "v") so a tag can be parsed as
// semver: "znc-1.10.2-ls183" -> "1.10.2-ls183", "release-1.31.2" -> "1.31.2".
func normalizeTag(tag string) string {
	tag = detect.StripNamePrefix(tag)
	return strings.TrimPrefix(strings.TrimPrefix(tag, "release-"), "v")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/changelog/ -run TestNormalizeTag -v`
Expected: PASS.
Run: `go test ./internal/changelog/`
Expected: PASS (existing normalizeTag consumers — `isPrerelease`, `releasesInSpan`, `latestStableRelease` — unaffected for non-prefixed tags).

- [ ] **Step 5: Commit**

```bash
git add internal/changelog/github.go internal/changelog/repo_internal_test.go
git commit -m "feat(changelog): strip package-name prefix in normalizeTag"
```

---

### Task 3: `findRelease` core-equality fallback (B2)

**Files:**
- Modify: `internal/changelog/github.go:207-236` (`findRelease` — replace the final `return ghRelease{}, false`)
- Test: `internal/changelog/repo_internal_test.go` (add `TestFindReleaseCoreEquality`)
- Test: `internal/changelog/github_test.go` (add `TestGitHubLinuxServerNamePrefixedTag` — end-to-end `Resolve`)

**Interfaces:**
- Consumes: `normalizeTag` (Task 2), `detect.ParseCore`, `isPrerelease` (existing).
- Produces: `findRelease` returns a same-core release for a full-semver version that no raw tag matches; an exact normalized-tag match wins over the newest same-core build.

- [ ] **Step 1: Write the failing internal test**

Add to `internal/changelog/repo_internal_test.go`:

```go
func TestFindReleaseCoreEquality(t *testing.T) {
	rels := []ghRelease{
		{TagName: "znc-1.10.2-ls183"},
		{TagName: "znc-1.10.2-ls182"},
		{TagName: "znc-1.10.1-ls179"},
	}
	// Suffixed version: exact normalized match wins.
	if got, ok := findRelease(rels, defaultTags("1.10.2-ls182"), "1.10.2-ls182"); !ok || got.TagName != "znc-1.10.2-ls182" {
		t.Errorf("suffixed: got %q ok=%v, want znc-1.10.2-ls182", got.TagName, ok)
	}
	// Bare full-semver version: newest same-core build (first-listed) wins.
	if got, ok := findRelease(rels, defaultTags("1.10.2"), "1.10.2"); !ok || got.TagName != "znc-1.10.2-ls183" {
		t.Errorf("bare: got %q ok=%v, want znc-1.10.2-ls183", got.TagName, ok)
	}
	// No core match: miss.
	if _, ok := findRelease(rels, defaultTags("2.0.0"), "2.0.0"); ok {
		t.Error("2.0.0: want miss")
	}
}
```

Note: `defaultTags` and `findRelease` are unexported and in-package; this test file is `package changelog`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/changelog/ -run TestFindReleaseCoreEquality -v`
Expected: FAIL — bare and suffixed cases return `ok=false` (the raw exact match misses and the partial scan bails for 2-dot versions).

- [ ] **Step 3: Write minimal implementation**

In `internal/changelog/github.go`, `findRelease`, replace the final line `return ghRelease{}, false` (currently line 235) with:

```go
	// Full-semver core-equality fallback: LSIO-style tags ("znc-1.10.2-ls183")
	// carry a name prefix and/or build suffix, so neither the raw exact match nor
	// the partial (<2-dot) scan above finds them. Match by parsed core instead. An
	// exact normalized-tag match (a suffix-bearing version like "1.10.2-ls183") wins
	// outright; otherwise the first-listed same-core release wins, which is the
	// newest-published and thus the highest build for a given core (LSIO publishes
	// ascending lsNNN over time).
	if vCore, cok := detect.ParseCore(version); cok {
		normVer := normalizeTag(version)
		var best ghRelease
		coreFound := false
		for _, rel := range rels {
			norm := normalizeTag(rel.TagName)
			c, ok := detect.ParseCore(norm)
			if !ok || c != vCore || isPrerelease(norm) {
				continue
			}
			if norm == normVer {
				return rel, true
			}
			if !coreFound {
				best, coreFound = rel, true
			}
		}
		if coreFound {
			return best, true
		}
	}
	return ghRelease{}, false
```

- [ ] **Step 4: Run internal test to verify it passes**

Run: `go test ./internal/changelog/ -run TestFindReleaseCoreEquality -v`
Expected: PASS.

- [ ] **Step 5: Write the end-to-end `Resolve` test**

Add to `internal/changelog/github_test.go`:

```go
func TestGitHubLinuxServerNamePrefixedTag(t *testing.T) {
	// znc releases are tagged "znc-<semver>-lsNNN"; the repo is docker-znc.
	srv := ghServer(t, map[string][]ghRel{
		"linuxserver/docker-znc": {
			{TagName: "znc-1.10.2-ls183", HTMLURL: "https://github.com/linuxserver/docker-znc/releases/tag/znc-1.10.2-ls183", Body: "ls183 notes"},
			{TagName: "znc-1.10.2-ls182", HTMLURL: "u182", Body: "ls182 notes"},
		},
	}, nil, "")
	s := changelog.NewGitHubSource(srv.Client(), srv.URL, srv.URL, func() string { return "" }, nil, time.Hour)
	res, err := s.Resolve(context.Background(), ghRangeInput("linuxserver/docker-znc:1.10.2-ls183", "1.10.2-ls181", "1.10.2-ls183"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "ls183 notes") {
		t.Errorf("Text = %q, want it to contain the ls183 body", res.Text)
	}
	if res.URL != "https://github.com/linuxserver/docker-znc/releases/tag/znc-1.10.2-ls183" {
		t.Errorf("URL = %q, want the ls183 release url", res.URL)
	}
}
```

- [ ] **Step 6: Run the end-to-end test + full package**

Run: `go test ./internal/changelog/ -run TestGitHubLinuxServerNamePrefixedTag -v`
Expected: PASS.
Run: `go test ./internal/changelog/`
Expected: PASS (radarr / nginx / redis regression tests still green — their tags match at the exact/partial steps before the fallback).

- [ ] **Step 7: Commit**

```bash
git add internal/changelog/github.go internal/changelog/repo_internal_test.go internal/changelog/github_test.go
git commit -m "feat(changelog): match LSIO release tags by version core"
```

---

### Task 4: `preferDigestTag` gate on the version reverse-match (C)

**Files:**
- Modify: `internal/detect/detect.go` (add `preferDigestTag`; gate the three reverse-match overrides at ~211-218 and ~427-430)
- Test: `internal/detect/detect_test.go` (add `TestPreferDigestTag`)

**Interfaces:**
- Consumes: `parseCore`, `StripNamePrefix` (Task 1) — same package.
- Produces: `func preferDigestTag(digestTag, label string) bool` — true when the bare digest-matched tag should replace the OCI-label-derived version (unparseable label, or different core); false when the label names the same version (keep the richer label).

- [ ] **Step 1: Write the failing test**

Add to `internal/detect/detect_test.go`:

```go
func TestPreferDigestTag(t *testing.T) {
	cases := []struct {
		digestTag, label string
		want             bool
	}{
		{"1.10.2", "znc-1.10.2-ls181", false}, // same core, name-prefixed label -> keep label
		{"1.10.2", "1.10.2-ls183", false},     // same core, suffixed label -> keep label
		{"15.1.2", "24.04", true},             // base-OS mislabel, different core -> override
		{"1.10.2", "", true},                  // empty label -> override
		{"1.10.2", "master-omnibus", true},    // unparseable label -> override
		{"1.10.2", "1.10.2", false},           // identical core -> keep label (no downgrade)
	}
	for _, c := range cases {
		if got := preferDigestTag(c.digestTag, c.label); got != c.want {
			t.Errorf("preferDigestTag(%q, %q) = %v, want %v", c.digestTag, c.label, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/detect/ -run TestPreferDigestTag -v`
Expected: FAIL — `undefined: preferDigestTag`.

- [ ] **Step 3: Write the helper**

Add to `internal/detect/detect.go` (near the other version helpers, e.g. after `semverTagPref`):

```go
// preferDigestTag reports whether a bare, digest-reverse-matched tag should
// replace the current OCI-label-derived version string. It replaces the label
// only when the label does not already name the same version: an unparseable
// label (a base-OS value, a rolling word, or empty) yields the tag; a label whose
// lenient core differs from the tag's is wrong for this image and yields the tag;
// a label whose core matches the tag is the same version but potentially more
// precise (carries "-lsNNN"), so it is kept.
func preferDigestTag(digestTag, label string) bool {
	lc, lok := parseCore(StripNamePrefix(label))
	if !lok {
		return true // label not a version: use the digest-matched tag
	}
	dc, _ := parseCore(digestTag) // digestTag is always bare semver
	return lc != dc               // differ: correct the label; equal: keep it
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/detect/ -run TestPreferDigestTag -v`
Expected: PASS.

- [ ] **Step 5: Gate the update-path reverse-match**

In `internal/detect/detect.go`, section 5b, replace the two override blocks (currently ~lines 212-217):

```go
		if v, _ := d.matchVersionByDigest(ctx, repo, svc.CurrentImageID, semverTagPref(svc.ImageVersion)); v != "" {
			fromVer = v
		}
		if v, _ := d.matchVersionByDigest(ctx, repo, targetRemote.ConfigDigest, semverTagPref(toLabel)); v != "" {
			toVer = v
		}
```

with:

```go
		if v, _ := d.matchVersionByDigest(ctx, repo, svc.CurrentImageID, semverTagPref(svc.ImageVersion)); v != "" && preferDigestTag(v, fromVer) {
			fromVer = v
		}
		if v, _ := d.matchVersionByDigest(ctx, repo, targetRemote.ConfigDigest, semverTagPref(toLabel)); v != "" && preferDigestTag(v, toVer) {
			toVer = v
		}
```

- [ ] **Step 6: Gate the current-row reverse-match**

In `internal/detect/detect.go`, `resolveCurrentVersion`, replace (currently ~lines 427-430):

```go
	ver := tagName
	if ver == "" {
		ver = label // conclusive no-match: fall back to the label (may be "")
	}
```

with:

```go
	ver := tagName
	if ver == "" || !preferDigestTag(tagName, label) {
		ver = label // no match, or the label already names this version (keep it)
	}
```

- [ ] **Step 7: Run the detect package tests**

Run: `go test ./internal/detect/`
Expected: PASS. If an existing reverse-match test asserted a bare tag overriding a same-core label, update its expectation to the kept label (this is the intended behavior change) and note it in the commit.

- [ ] **Step 8: Commit**

```bash
git add internal/detect/detect.go internal/detect/detect_test.go
git commit -m "fix(detect): keep suffix-bearing label over bare digest-matched tag"
```

---

### Task 5: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Vet + full test suite**

Run: `go vet ./... && CGO_ENABLED=0 go test ./...`
Expected: PASS across all packages.

- [ ] **Step 2: Static-binary invariant**

Run: `CGO_ENABLED=0 go build ./...`
Expected: builds clean (no CGO, no new deps).

- [ ] **Step 3: Commit (only if any incidental fixups were needed)**

```bash
git add -A
git commit -m "test(detect,changelog): LSIO tag matching + version display green"
```

---

## Self-Review

**Spec coverage:**
- Shared `StripNamePrefix` → Task 1. ✓
- B1 `normalizeTag` → Task 2. ✓
- B2 `findRelease` core-equality → Task 3 (+ end-to-end Resolve). ✓
- C `preferDigestTag` + both reverse-match sites (update path 5b, `resolveCurrentVersion`) → Task 4. ✓
- Regression guards (thelounge/radarr/nginx/redis) → Task 3 Step 6, Task 2 Step 4. ✓
- Canonical `parseCore` untouched → honored (StripNamePrefix is separate; helpers call it explicitly). ✓

**Placeholder scan:** none — every code step shows full code, every run step shows an expected result.

**Type consistency:** `StripNamePrefix(string) string`, `preferDigestTag(digestTag, label string) bool`, `normalizeTag(string) string`, `findRelease([]ghRelease, []string, string) (ghRelease, bool)` — names and signatures match across tasks and match the existing code.
