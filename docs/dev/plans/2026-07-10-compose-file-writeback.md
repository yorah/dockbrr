# Compose File Write-Back Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make apply write updates back into the user's compose file, preserving
tag granularity, editing surgically, rolling back via the existing snapshot blob,
and surfacing file↔runtime divergence as a Drift badge.

**Architecture:** A tag-granularity classifier decides the apply path. Floating
tags (`latest`/`1`/`1.31`) get a plain `pull && up` (no digest-pin). Exact pins
(`1.31.2`) whose image is a rewritable literal get a surgical single-line YAML
edit + atomic write; the pre-edit file is stored in `state_snapshots.compose_blob`
for rollback. Non-rewritable exact pins and digest pins fall back to today's
runtime-pin override. Discovery derives a `drifted` flag by comparing the
compose-declared image to the running container ref.

**Tech Stack:** Go 1.26 (CGO-free), `gopkg.in/yaml.v3` (already a transitive dep
via compose-go: verify vendoring in Task 2), existing `compose.Parse`
(compose-go), SQLite migrations, React + TS + Tailwind + vitest.

> **⚠️ Spec refinement (needs reviewer awareness):** the design spec
> (`docs/dev/specs/2026-07-10-compose-file-writeback-design.md`) says drift
> is "derived at discovery time; no new persisted column." Persisting it is
> required for the dashboard read path without per-request compose parsing, so
> this plan adds a `services.drifted` column (migration 0004) computed during
> discovery. This is the only deviation from the spec.

## Global Constraints

- CGO_ENABLED=0 must stay green: `CGO_ENABLED=0 go build ./...`.
- No shell strings for compose, argv only (invariant 6). This feature adds no
  compose verbs; it edits files and reuses existing PullSpec/UpSpec.
- Snapshot precedes every mutation (invariant 3). The file edit is a mutation, so
  it happens AFTER `snapshot()` and the blob is captured IN that snapshot.
- Pull-before-up always (invariant 4). Unchanged: edit file → pull → up.
- TS typecheck via `./node_modules/.bin/tsc -b --noEmit` then `npm run build`
  (NOT `npx tsc`: rtk masks errors). After any web build, restore the tracked
  placeholder: `git checkout -- internal/httpapi/dist/index.html`.
- Preserve granularity: the apply path keys on the **tracked tag** (what the file
  declares), never the suggested update's tag. A floating tracked tag is NEVER
  rewritten to a more specific version, even when a cross-tag exact update exists.
- Exact rewrites are exact→exact: write `repo:newTag`, never append `@digest`.

---

### Task 1: Tag-granularity classifier

**Files:**
- Create: `internal/detect/tagclass.go`
- Test: `internal/detect/tagclass_test.go`

**Interfaces:**
- Consumes: `detect.SplitRef(ref string) (repo, tag string)` (existing in this package).
- Produces: `type TagClass int` with `TagFloating`, `TagExact`, `TagDigest`; and
  `func ClassifyTag(ref string) TagClass`.

- [ ] **Step 1: Write the failing test**

```go
package detect

import "testing"

func TestClassifyTag(t *testing.T) {
	cases := []struct {
		ref  string
		want TagClass
	}{
		{"nginx:latest", TagFloating},
		{"nginx", TagFloating},          // implicit :latest
		{"nginx:1", TagFloating},        // major-only
		{"nginx:1.31", TagFloating},     // major.minor
		{"nginx:1.31.2", TagExact},      // full semver
		{"nginx:v1.31.2", TagExact},     // v-prefixed
		{"nginx:1.31.2-alpine", TagExact},
		{"redis:8.8.0", TagExact},
		{"nginx:stable", TagFloating},   // named tag
		{"nginx:1.31.2@sha256:abc", TagDigest},
		{"nginx@sha256:abc", TagDigest},
		{"ghcr.io/org/app:1.2.3", TagExact},
		{"ghcr.io/org/app:main", TagFloating},
	}
	for _, c := range cases {
		if got := ClassifyTag(c.ref); got != c.want {
			t.Errorf("ClassifyTag(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/detect/ -run TestClassifyTag`
Expected: FAIL: `undefined: ClassifyTag`.

- [ ] **Step 3: Write minimal implementation**

```go
package detect

import "regexp"

// TagClass describes how tightly a user pinned an image tag. It drives whether
// apply rewrites the compose file (exact only) and whether the container is
// digest-pinned at runtime.
type TagClass int

const (
	TagFloating TagClass = iota // latest / named / partial semver (1, 1.31), tracks a stream
	TagExact                    // full semver (1.31.2, optionally -pre/+build)
	TagDigest                   // ref carries @sha256:: an explicit digest pin
)

// exactSemverRe matches a fully-specified semver tag: three numeric components,
// optional leading v, optional pre-release/build metadata.
var exactSemverRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[-+].*)?$`)

// ClassifyTag classifies an image ref by pin granularity. A ref carrying a
// digest is TagDigest regardless of tag; otherwise a full-semver tag is
// TagExact and everything else (latest, named tags, partial semver like "1" or
// "1.31") is TagFloating.
func ClassifyTag(ref string) TagClass {
	if strings.Contains(ref, "@sha256:") {
		return TagDigest
	}
	_, tag := SplitRef(ref)
	if exactSemverRe.MatchString(tag) {
		return TagExact
	}
	return TagFloating
}
```

> Implementer note: the digest check runs on the RAW ref before `SplitRef`.
> Verify `SplitRef`'s return for a `repo:tag` input (it must yield the bare tag,
> e.g. `1.31.2`, for the semver regex to match) and adjust only if its behavior
> differs. Add `"strings"` and `"regexp"` to imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/detect/ -run TestClassifyTag`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/detect/tagclass.go internal/detect/tagclass_test.go
git commit -m "feat(detect): tag-granularity classifier (floating/exact/digest)"
```

---

### Task 2: Compose image locate + surgical rewrite + atomic write

**Files:**
- Create: `internal/compose/rewrite.go`
- Test: `internal/compose/rewrite_test.go`

**Interfaces:**
- Produces:
  - `type LocatedImage struct { File string; Line int; OldRef string; Rewritable bool }`
  - `func LocateImageLine(configFiles []string, service string) (LocatedImage, error)`.
    Parses each file with yaml.v3, finds `services.<service>.image` scalar.
    `Rewritable` is true iff the value is a plain literal (no `${`), found in
    exactly one config file as a real scalar. When present in multiple files, the
    LAST file wins (compose precedence) and that one is returned. Returns
    `Rewritable:false` (no error) when not found or interpolated.
  - `func ReplaceImageLine(content, oldRef, newRef string, line int) (string, error)`.
    Replaces `oldRef` with `newRef` on the 1-based `line` only; errors if that
    line doesn't contain `oldRef`.
  - `func WriteFileAtomic(path, content string) error`: temp file in the same
    dir + `os.Rename`.

- [ ] **Step 1: Write the failing tests**

```go
package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const composeFixture = `services:
  web:
    image: nginx:1.31.2   # pinned
    ports:
      - "8080:80"
  cache:
    image: ${REDIS_IMAGE}
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocateImageLineLiteral(t *testing.T) {
	p := writeTemp(t, "compose.yml", composeFixture)
	loc, err := LocateImageLine([]string{p}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Rewritable || loc.OldRef != "nginx:1.31.2" || loc.File != p {
		t.Fatalf("got %+v", loc)
	}
	if loc.Line != 3 {
		t.Errorf("Line = %d, want 3", loc.Line)
	}
}

func TestLocateImageLineInterpolatedNotRewritable(t *testing.T) {
	p := writeTemp(t, "compose.yml", composeFixture)
	loc, err := LocateImageLine([]string{p}, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Rewritable {
		t.Fatalf("interpolated image must not be rewritable: %+v", loc)
	}
}

func TestLocateImageLineMissingService(t *testing.T) {
	p := writeTemp(t, "compose.yml", composeFixture)
	loc, err := LocateImageLine([]string{p}, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Rewritable {
		t.Fatalf("missing service must not be rewritable: %+v", loc)
	}
}

func TestLocateImageLineLastFileWins(t *testing.T) {
	base := writeTemp(t, "compose.yml", composeFixture)
	over := writeTemp(t, "override.yml", "services:\n  web:\n    image: nginx:1.31.2\n")
	loc, err := LocateImageLine([]string{base, over}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if loc.File != over {
		t.Errorf("last file should win: got %s", loc.File)
	}
}

func TestReplaceImageLinePreservesFormatting(t *testing.T) {
	out, err := ReplaceImageLine(composeFixture, "nginx:1.31.2", "nginx:1.32.0", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image: nginx:1.32.0   # pinned") {
		t.Errorf("comment/formatting not preserved:\n%s", out)
	}
	if strings.Contains(out, "nginx:1.31.2") {
		t.Errorf("old ref still present:\n%s", out)
	}
	if !strings.Contains(out, `- "8080:80"`) {
		t.Errorf("unrelated lines mangled:\n%s", out)
	}
}

func TestReplaceImageLineWrongLineErrors(t *testing.T) {
	if _, err := ReplaceImageLine(composeFixture, "nginx:1.31.2", "nginx:1.32.0", 1); err == nil {
		t.Fatal("expected error when line lacks oldRef")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, "new content"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "new content" {
		t.Errorf("got %q", string(b))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/compose/ -run 'TestLocateImageLine|TestReplaceImageLine|TestWriteFileAtomic'`
Expected: FAIL: undefined identifiers.

- [ ] **Step 3: Implement**

```go
package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LocatedImage is the result of resolving a service's image line in the raw
// config files: the file + 1-based line holding the literal, the current ref,
// and whether it is safe to rewrite in place.
type LocatedImage struct {
	File       string
	Line       int
	OldRef     string
	Rewritable bool
}

// LocateImageLine finds services.<service>.image across configFiles. The last
// file that declares it as a plain literal scalar wins (compose precedence).
// Rewritable is false (with nil error) when the image is absent or interpolated
// (contains "${"). A file that fails to parse is a hard error.
func LocateImageLine(configFiles []string, service string) (LocatedImage, error) {
	var found LocatedImage
	for _, f := range configFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			return LocatedImage{}, fmt.Errorf("read %s: %w", f, err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return LocatedImage{}, fmt.Errorf("parse %s: %w", f, err)
		}
		node := imageScalar(&doc, service)
		if node == nil {
			continue
		}
		val := node.Value
		loc := LocatedImage{File: f, Line: node.Line, OldRef: val, Rewritable: true}
		if strings.Contains(val, "${") || node.Style == yaml.AliasNode || val == "" {
			loc.Rewritable = false
		}
		found = loc // later file overrides earlier
	}
	return found, nil
}

// imageScalar walks a parsed document for services.<service>.image and returns
// its scalar node (nil if not present as a mapping value).
func imageScalar(doc *yaml.Node, service string) *yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	services := mapValue(root, "services")
	if services == nil {
		return nil
	}
	svc := mapValue(services, service)
	if svc == nil {
		return nil
	}
	return mapValue(svc, "image")
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ReplaceImageLine swaps oldRef for newRef on the 1-based line only, leaving
// every other byte untouched. It errors if that line does not contain oldRef.
func ReplaceImageLine(content, oldRef, newRef string, line int) (string, error) {
	lines := strings.Split(content, "\n")
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line %d out of range (%d lines)", line, len(lines))
	}
	i := line - 1
	if !strings.Contains(lines[i], oldRef) {
		return "", fmt.Errorf("line %d does not contain %q", line, oldRef)
	}
	lines[i] = strings.Replace(lines[i], oldRef, newRef, 1)
	return strings.Join(lines, "\n"), nil
}

// WriteFileAtomic writes content to a temp file in path's directory, then
// renames it over path, so a crash mid-write never leaves a truncated file.
func WriteFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dockbrr-write-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [ ] **Step 4: Verify yaml.v3 is available**

Run: `go test ./internal/compose/ -run 'TestLocateImageLine|TestReplaceImageLine|TestWriteFileAtomic'`
Expected: PASS. If `gopkg.in/yaml.v3` is not in `go.mod`, run
`go get gopkg.in/yaml.v3` first (it is already a compose-go transitive dep, so
this only promotes it to a direct require), then `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/compose/rewrite.go internal/compose/rewrite_test.go go.mod go.sum
git commit -m "feat(compose): surgical image-line locate + atomic rewrite"
```

---

### Task 3: Drift: declared-image resolver, migration, discovery compute, DTO

**Files:**
- Create: `internal/store/migrations/0004_service_drifted.sql`
- Modify: `internal/store/services.go` (add `Drifted bool` to struct + upsert columns + scans)
- Modify: `internal/discovery/discovery.go` (compute drift per service before Upsert)
- Modify: `internal/httpapi/projects.go` (expose `Drifted` in the service DTO)
- Test: `internal/discovery/discovery_test.go` (drift cases)

**Interfaces:**
- Consumes: `compose.Parse(ctx, workingDir, configFiles) (compose.Project, error)`
  where `Project.Services[i]` has `Name`, `Image`.
- Produces: `store.Service.Drifted bool`; discovery sets it; DTO carries it as
  `json:"drifted"`.

- [ ] **Step 1: Write the migration**

`internal/store/migrations/0004_service_drifted.sql`:
```sql
ALTER TABLE services ADD COLUMN drifted INTEGER NOT NULL DEFAULT 0;
```

- [ ] **Step 2: Write the failing discovery test**

Add to `internal/discovery/discovery_test.go` a test that drives `Reconcile`
with a stubbed compose parse + a running container whose ref diverges from the
declared image, asserting the upserted service has `Drifted == true`; and a
matching case asserting `false`. Match the existing test's fakes/harness in that
file (reuse its container source + store setup patterns, read the file first).

Drift rule to assert:
- declared `nginx:1.31` (parsed), running `nginx:1.31` → NOT drifted.
- declared `nginx:1.31.2` (parsed), running `nginx:1.31.2@sha256:x` → drifted.
- service with no compose declaration (standalone) → NOT drifted.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/discovery/ -run Drift`
Expected: FAIL.

- [ ] **Step 4: Implement**

In `internal/store/services.go`: add `Drifted bool` after `Pinned`. Add `drifted`
to the Upsert INSERT column list + `excluded.drifted` update + the `VALUES`
binding (as `boolToInt(sv.Drifted)` matching how `Pinned` is bound), and to every
`SELECT`/`Scan` that reads services (Get, ListByProject, List), add
`&drifted int` locals mirroring the existing `pinned int` handling, then
`sv.Drifted = drifted != 0`.

In `internal/discovery/discovery.go`: after building `groups` and BEFORE the
service Upsert loop, for each compose project parse once:
```go
// Drift: a service is drifted when the image it is actually running differs
// from what its compose file declares (e.g. a runtime-only digest pin the file
// doesn't carry, or an out-of-band edit). Reuses the read-only compose parser.
declared := map[string]string{} // service name -> declared image ref
if g.Kind == "compose" && len(g.ConfigFiles) > 0 {
	if pj, perr := compose.Parse(ctx, g.WorkingDir, g.ConfigFiles); perr == nil {
		for _, s := range pj.Services {
			declared[s.Name] = s.Image
		}
	}
	// parse error: leave declared empty -> nothing marked drifted this cycle.
}
```
Then in the per-service Upsert, set:
```go
Drifted: declaredDiffers(declared[s.Name], s.ImageRef),
```
with a helper:
```go
// declaredDiffers reports whether a service's running ref diverges from its
// compose-declared image. Empty declared (standalone / parse failure / service
// absent from file) is never drift.
func declaredDiffers(declared, running string) bool {
	if declared == "" {
		return false
	}
	return declared != running
}
```
Add `"dockbrr/internal/compose"` to discovery imports if not present.

In `internal/httpapi/projects.go`: add `Drifted bool \`json:"drifted"\`` to the
service DTO struct and populate it from `sv.Drifted` in the mapping (mirror the
`Pinned` field).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/discovery/ ./internal/store/ ./internal/httpapi/`
Expected: PASS. Then `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/0004_service_drifted.sql internal/store/services.go internal/discovery/discovery.go internal/httpapi/projects.go internal/discovery/discovery_test.go
git commit -m "feat(discovery): derive services.drifted from compose vs runtime"
```

---

### Task 4: write_back_compose setting: accessor, default, API exposure

**Files:**
- Modify: `internal/store/settings.go` (bool accessor with default)
- Modify: `internal/httpapi/settings.go` (include `write_back_compose` in GET response + accept in PUT)
- Test: `internal/store/settings_test.go`

**Interfaces:**
- Produces: `func (s *Settings) GetBoolDefault(key string, def bool) bool`,   returns `def` when the key is absent or unparseable; parses "true"/"false".
- Setting key: `write_back_compose`, default **true**.

- [ ] **Step 1: Write the failing test**

```go
func TestGetBoolDefault(t *testing.T) {
	s := newTestSettings(t) // reuse this file's existing harness
	if !s.GetBoolDefault("write_back_compose", true) {
		t.Error("absent key should return default true")
	}
	if err := s.Set("write_back_compose", "false"); err != nil {
		t.Fatal(err)
	}
	if s.GetBoolDefault("write_back_compose", true) {
		t.Error("set false should override default")
	}
	if err := s.Set("write_back_compose", "true"); err != nil {
		t.Fatal(err)
	}
	if !s.GetBoolDefault("write_back_compose", false) {
		t.Error("set true should override default")
	}
}
```
(If `newTestSettings` doesn't exist in the file, follow whatever setup the other
tests there use.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/store/ -run TestGetBoolDefault`
Expected: FAIL: `undefined: GetBoolDefault`.

- [ ] **Step 3: Implement**

In `internal/store/settings.go`:
```go
// GetBoolDefault returns the boolean value of key, or def when the key is
// absent or not a valid bool. Used for feature flags that must have a safe
// default before the user ever visits Settings.
func (s *Settings) GetBoolDefault(key string, def bool) bool {
	v, err := s.Get(key)
	if err != nil {
		return def
	}
	switch v {
	case "true":
		return true
	case "false":
		return false
	default:
		return def
	}
}
```

In `internal/httpapi/settings.go`: wherever the GET handler builds the settings
response map (the same place `air_gap_mode` is emitted), add:
```go
"write_back_compose": strconv.FormatBool(s.deps.Settings.GetBoolDefault("write_back_compose", true)),
```
and ensure the PUT handler's allowed-key set (if it whitelists keys) includes
`write_back_compose`. If PUT already accepts arbitrary keys (like
`air_gap_mode`), no PUT change is needed, verify by reading the handler.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/ ./internal/httpapi/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/settings.go internal/httpapi/settings.go internal/store/settings_test.go
git commit -m "feat(settings): write_back_compose flag (default on)"
```

---

### Task 5: Apply branching: floating / exact-rewrite / fallback

**Files:**
- Modify: `internal/job/worker.go` (the apply path: replace the
  `files := proj.ConfigFiles; if crossTag {...}` block; thread the pre-edit blob
  into `snapshot`)
- Test: `internal/job/worker_test.go` (or the existing apply test file, read it
  to match the fake runner/composer/store harness)

**Interfaces:**
- Consumes: `detect.ClassifyTag`, `compose.LocateImageLine`,
  `compose.ReplaceImageLine`, `compose.WriteFileAtomic`,
  `store.Settings.GetBoolDefault`, existing `writePinOverride`, existing
  `snapshot(ctx, job, svc, proj)`.
- The Applier must have access to settings. Verify the `Applier` struct's deps:
  if it lacks a settings reference, add one (constructor `NewApplier` + the
  `main.go` wiring): mirror how other deps (resolver, composer) are injected.

**Design (key on the TRACKED tag's class):**

```
repo, trackedTag := detect.SplitRef(svc.ImageRef)   // already computed above
class := detect.ClassifyTag(svc.ImageRef)
writeBack := a.settings.GetBoolDefault("write_back_compose", true)

newTag := trackedTag
if crossTag { newTag = upd.Tag }

files := proj.ConfigFiles
var preEditBlob *string   // -> snapshot.ComposeBlob

switch {
case writeBack && class == detect.TagExact && newTag != trackedTag:
    // Rewritable exact pin: try surgical file edit. On any miss, fall through
    // to the runtime-pin override (fallback path).
    loc, lerr := compose.LocateImageLine(proj.ConfigFiles, svc.Name)
    if lerr == nil && loc.Rewritable && loc.OldRef == svc.ImageRef {
        raw, rerr := os.ReadFile(loc.File)
        if rerr == nil {
            newContent, cerr := compose.ReplaceImageLine(string(raw), loc.OldRef, repo+":"+newTag, loc.Line)
            if cerr == nil {
                blob := blobJSON(loc.File, string(raw))   // {"path","content"}
                preEditBlob = &blob
                // snapshot BEFORE the edit (mutation). See snapshot threading below.
            }
        }
    }
    // if preEditBlob still nil -> not actually rewritable -> use override below.
}
```

Because the snapshot must precede the mutation, the ordering in the function
becomes:

1. Compute `class`, `writeBack`, `newTag`, and (if applicable) resolve `loc` +
   read `raw` + compute `newContent`: WITHOUT writing yet. Stash them.
2. Call `a.snapshot(...)`: pass `preEditBlob` so the snapshot row carries the
   pre-edit file (extend `snapshot` to accept `blob *string` and set
   `sn.ComposeBlob = blob`).
3. THEN perform the effect:
   - if we have a staged edit → `compose.WriteFileAtomic(loc.File, newContent)`;
     `files = proj.ConfigFiles` (no override).
   - else if `crossTag` (fallback: non-rewritable exact, digest pin, or
     writeBack off with a cross-tag) → existing `writePinOverride` +
     `files = append(..., overridePath)`.
   - else (floating tracked tag, same-tag digest drift) → `files =
     proj.ConfigFiles` (plain pull+up, no override).
4. pull → up → rediscover → health gate → success (unchanged).

**Add helper** in worker.go:
```go
// blobJSON encodes a pre-edit compose file for the snapshot's compose_blob so
// rollback can restore it verbatim.
func blobJSON(path, content string) string {
	b, _ := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{path, content})
	return string(b)
}
```
(Add `encoding/json` and `os` imports if missing.)

**Behavioral tests (write these; match the file's existing apply-test harness):**
- exact tracked `nginx:1.31.2`, cross-tag `1.32.0`, writeBack on, file has literal
  → asserts: `WriteFileAtomic` target contains `nginx:1.32.0`; the runner is
  invoked with NO `dockbrr-rollback-*.yml` in ConfigFiles; snapshot row has a
  non-nil `ComposeBlob` decoding to the pre-edit content.
- exact tracked, but image is `${VAR}` → asserts fallback: override file IS in the
  runner's ConfigFiles; compose file unchanged on disk.
- floating tracked `nginx:1.31`, cross-tag `1.32.0` → asserts NO file write, NO
  override; plain pull+up on ConfigFiles (granularity preserved, `1.31` not
  rewritten).
- writeBack off + exact cross-tag → asserts fallback override path (file
  untouched).

- [ ] **Step 1:** Write the four failing tests above.
- [ ] **Step 2:** Run `go test ./internal/job/ -run Apply` → FAIL.
- [ ] **Step 3:** Implement the branching + snapshot-blob threading + Applier
  settings dep.
- [ ] **Step 4:** Run `go test ./internal/job/` → PASS; then
  `CGO_ENABLED=0 go build ./...`.
- [ ] **Step 5:** Commit:
```bash
git add internal/job/worker.go internal/job/worker_test.go cmd/dockbrr/main.go
git commit -m "feat(job): apply writes back exact-pin compose edits, floating tags float"
```

---

### Task 6: Rollback restores compose_blob

**Files:**
- Modify: `internal/job/worker.go` (the rollback path, around the existing
  `writePinOverride(... snap.PrevRepo ...)` at ~line 367)
- Test: `internal/job/worker_test.go`

**Interfaces:**
- Consumes: `store.Snapshots.GetLatestForService` (returns `Snapshot` with
  `ComposeBlob *string`), `compose.WriteFileAtomic`.

**Design:** in the rollback function, before building the pin override, check the
snapshot's `ComposeBlob`:
```go
if snap.ComposeBlob != nil {
	var b struct{ Path, Content string }
	if err := json.Unmarshal([]byte(*snap.ComposeBlob), &b); err == nil && b.Path != "" {
		if werr := compose.WriteFileAtomic(b.Path, b.Content); werr != nil {
			a.fail(job, "rollback: restore compose file: "+werr.Error())
			return
		}
		// File restored to its pre-apply content; plain pull+up on the user's
		// files reverts the running container. No pin override needed.
		files := proj.ConfigFiles
		// ... pull + up on files (reuse existing pull/up + health-gate flow) ...
		return // after success handling
	}
}
// else: existing digest-repin override path (unchanged).
```
Structure this so the existing pull/up/health-gate/success tail is shared
between the blob-restore and override branches (extract a small helper if the
existing code doesn't already make that clean, keep the diff minimal).

**Tests:**
- rollback with a snapshot whose `ComposeBlob` is `{"path":P,"content":C}` →
  asserts file P on disk equals C afterward, runner invoked with NO override.
- rollback with nil `ComposeBlob` → asserts existing override path still runs
  (regression guard).

- [ ] **Step 1:** Write the two failing tests.
- [ ] **Step 2:** `go test ./internal/job/ -run Rollback` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test ./internal/job/` → PASS; `CGO_ENABLED=0 go build ./...`.
- [ ] **Step 5:** Commit:
```bash
git add internal/job/worker.go internal/job/worker_test.go
git commit -m "feat(job): rollback restores compose file from snapshot blob"
```

---

### Task 7: Web: Drifted badge

**Files:**
- Modify: `web/src/components/StatusBadge.tsx` (add `"drifted"` to union, LABEL,
  VARIANT, computeStatus)
- Modify: `web/src/api/types.ts` (add `drifted: boolean` to the service type)
- Modify: `web/src/components/DashboardTable.tsx` (pass `svc.drifted` through /
  ensure computeStatus sees it)
- Test: `web/src/components/StatusBadge.test.tsx` (or the existing computeStatus test)

**Interfaces:**
- Consumes: the API `drifted` boolean from Task 3.
- Produces: a `"drifted"` status, amber/warning variant, shown ABOVE `pinned`
  (a runtime-only fallback is both pinned and drifted; Drift is the actionable
  signal) but BELOW `gone`.

- [ ] **Step 1: Write the failing test**

```ts
import { computeStatus } from "./StatusBadge";

test("drifted takes precedence over pinned", () => {
  expect(
    computeStatus({ state: "running", pinned: true, drifted: true } as any, undefined),
  ).toBe("drifted");
});

test("pinned when not drifted", () => {
  expect(
    computeStatus({ state: "running", pinned: true, drifted: false } as any, undefined),
  ).toBe("pinned");
});
```

- [ ] **Step 2:** `cd web && npm test -- StatusBadge` → FAIL.
- [ ] **Step 3: Implement**

In `StatusBadge.tsx`:
- Add `| "drifted"` to `Status`.
- `LABEL.drifted = "Drifted"`.
- `VARIANT.drifted = "warning"`.
- In `computeStatus`, add after the `gone` check and BEFORE `pinned`:
  ```ts
  if (svc.drifted) return "drifted";
  ```
  (Widen the `svc` param type to include `drifted?: boolean`.)

In `web/src/api/types.ts`: add `drifted: boolean;` to the service interface
(next to `pinned`).

In `DashboardTable.tsx`: `computeStatus` already receives `r.service`; confirm
`r.service.drifted` flows (no change if it passes the whole service object).

- [ ] **Step 4:** `cd web && npm test -- StatusBadge` → PASS. Then
  `cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build`, then
  `git checkout -- internal/httpapi/dist/index.html`.
- [ ] **Step 5:** Commit:
```bash
git add web/src/components/StatusBadge.tsx web/src/api/types.ts web/src/components/DashboardTable.tsx web/src/components/StatusBadge.test.tsx
git commit -m "feat(web): Drifted status badge"
```

---

### Task 8: Web: write-back setting toggle

**Files:**
- Modify: `web/src/components/settings/GeneralSettings.tsx` (add
  `write_back_compose` to `EditableKey`, `EDITABLE_KEYS`, the form init, and a
  Switch)
- Modify: `web/src/api/types.ts` (add `write_back_compose: string` to Settings)
- Test: `web/src/components/settings/GeneralSettings.test.tsx`

**Interfaces:**
- Consumes: `write_back_compose` from `GET /api/settings` (Task 4).
- Reuses the existing dirty-indicator + batch-save machinery (a new EDITABLE_KEY
  participates automatically).

- [ ] **Step 1: Write the failing test**

```ts
test("write-back switch toggles and marks the form dirty", async () => {
  server.use(
    http.get("/api/settings", () =>
      HttpResponse.json({ ...fixture, write_back_compose: "true" })),
    http.put("/api/settings", () => HttpResponse.json({ ok: true })),
  );
  renderWithClient(<GeneralSettings />);
  const sw = await screen.findByRole("switch", { name: /write updates back/i });
  expect(sw).toHaveAttribute("data-state", "checked");
  await userEvent.click(sw);
  expect(sw).toHaveAttribute("data-state", "unchecked");
  expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument();
});
```
(Add `write_back_compose: "true"` to the test file's `fixture` object.)

- [ ] **Step 2:** `cd web && npm test -- GeneralSettings` → FAIL.
- [ ] **Step 3: Implement**

In `GeneralSettings.tsx`:
- Add `"write_back_compose"` to the `EditableKey` union and to `EDITABLE_KEYS`.
- Add `write_back_compose: data.write_back_compose` to the `setForm({...})` init.
- Add a Switch (mirror the air-gap one) labelled "Write updates back to compose
  files", `checked={form.write_back_compose === "true"}`,
  `onCheckedChange={(c) => setForm((f) => ({ ...f, write_back_compose: c ? "true" : "false" }))}`.

In `web/src/api/types.ts`: add `write_back_compose: string;` to `Settings`.

- [ ] **Step 4:** `cd web && npm test -- GeneralSettings` → PASS. Then
  `./node_modules/.bin/tsc -b --noEmit && npm run build`, then
  `git checkout -- internal/httpapi/dist/index.html`.
- [ ] **Step 5:** Commit:
```bash
git add web/src/components/settings/GeneralSettings.tsx web/src/api/types.ts web/src/components/settings/GeneralSettings.test.tsx
git commit -m "feat(web): write-back-to-compose settings toggle"
```

---

## Final verification (after all tasks)

- `CGO_ENABLED=0 go build ./...`: green.
- `go vet ./... && go test ./...`, green.
- `cd web && ./node_modules/.bin/tsc -b --noEmit && npm test && npm run build`, green.
- `git checkout -- internal/httpapi/dist/index.html`: restore placeholder.
- Manual smoke (live Docker): recreate `smoke-web` on `nginx:1.31.2`, let detect
  find a newer patch, Apply → the compose file's `image:` line is rewritten (tag
  only, comments preserved), container runs the new tag NOT digest-pinned, no
  Pinned/Drift badge. Then a `${VAR}` service → Apply → Drifted badge, file
  untouched. Rollback the rewritten one → file reverts to `1.31.2`.
