# Settings Revamp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the six-tab settings screen with a left sub-nav of card-based, deep-linkable pages; add a read-only Application page backed by a new `/api/system/info`; move "Add project" out of settings into a shared dialog.

**Architecture:** Backend gains one read-only endpoint (`GET /api/system/info`) that reports build/runtime/docker/storage/auth facts: build stamps come from `debug.ReadBuildInfo()` VCS metadata (no ldflags, no build-task change), uptime is derived client-side from `started_at`. Frontend gains a `SettingsLayout` with nested routes, two card primitives (`SettingsCard`, `InfoRow`) that every settings page is built from, a `useSettingsForm` hook holding the dirty/save-diff logic shared by the pages split out of `GeneralSettings`, and an `AddProjectDialog` reachable from the sidebar and the dashboard.

**Tech Stack:** Go 1.26 (chi, Docker SDK, SQLite), React + TypeScript + Vite, Tailwind v4, Radix/shadcn, TanStack Router + Query, vitest + Testing Library, Go stdlib testing.

Spec: `docs/dev/specs/2026-07-13-settings-revamp-design.md`

## Task summary

| # | Task | Deps | Model | Reviewer |
|---|------|------|-------|----------|
| 1 | `GET /api/system/info` (backend) |, | opus | opus |
| 2 | `SystemInfo` DTO + `useSystemInfo` query | 1 | haiku | sonnet |
| 3 | `SettingsCard` + `InfoRow` primitives |, | haiku | sonnet |
| 4 | Application settings page | 2, 3 | sonnet | sonnet |
| 5 | `useSettingsForm` + split General → Scanning/Updates | 3, 4 | sonnet | sonnet |
| 6 | GitHub token card on Registries | 3, 5 | opus | opus |
| 7 | `AddProjectDialog` + sidebar/dashboard triggers |, | sonnet | sonnet |
| 8 | `SettingsLayout` + nested settings routes | 3–7 | sonnet | opus |

Task 1 and Task 6 are opus/opus: a new authenticated endpoint (must leak no secrets) and the write-only GitHub token invariant. Task 8's review is opus: it is the whole-surface wiring.

## Global Constraints

- `CGO_ENABLED=0 go build ./...` must stay green, single static binary, no cgo. No new Go dependency may be added for this feature.
- No change to `mise.toml` build tasks and no ldflags: build stamps come from `debug.ReadBuildInfo()` VCS settings only.
- UI/API never touches Docker mutation. `/api/system/info` is read-only (`Ping` + `ServerVersion` only).
- GitHub token stays **write-only**: never echoed by the API, never prefilled in the UI, only sent in a PUT when a new value was typed.
- `/api/system/info` exposes no secrets: no token values, no password hash, no session token.
- Frontend: CSRF header on mutations only, `credentials: "include"`, 401 → auth gate. No CDN assets. All fetches go through `apiFetch`.
- TS typecheck via `cd web && ./node_modules/.bin/tsc -b --noEmit` (NOT `npx tsc`. The rtk proxy reports a false "No errors found").
- Every task ends green: `go vet ./... && go test ./...` and `cd web && npm test` and `cd web && npm run build`.

---

### Task 1: `GET /api/system/info` (backend)

**Files:**
- Create: `internal/httpapi/system.go`
- Create: `internal/httpapi/system_test.go`
- Modify: `internal/docker/client.go` (add `ServerVersion`)
- Modify: `internal/httpapi/server.go` (`Deps` gains `StartedAt` + `DockerVersion`; `routes()` gains the route)
- Modify: `cmd/dockbrr/main.go:~300-316` (wire `StartedAt` + `DockerVersion`)

**Interfaces:**
- Consumes: existing `Deps.DockerPinger`, `s.dockerReachable(r)` (`internal/httpapi/status.go`), `s.userByID(uid)`, `userIDFromCtx(ctx)`, `writeJSON`, `s.cfg config.Config` (fields `DataDir`, `BindAddr`).
- Produces:
  - `func (cl *Client) ServerVersion(ctx context.Context) (version, apiVersion string, err error)` in `internal/docker`.
  - `Deps.StartedAt time.Time` and `Deps.DockerVersion func(ctx context.Context) (version, apiVersion string, err error)` (optional; nil in tests, mirroring the existing `NextScan` idiom).
  - Route `GET /api/system/info` inside the `requireAuth` group, returning the JSON shape asserted in Step 1.

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/system_test.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"dockbrr/internal/config"
	"dockbrr/internal/version"
)

// systemInfoBody drives the endpoint and decodes its JSON body.
func systemInfoBody(t *testing.T, srv *Server, tok, csrf string) map[string]any {
	t.Helper()
	rec := authedGet(t, srv, "/api/system/info", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/system/info = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestSystemInfoReportsBuildRuntimeAndStorage(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	srv.cfg = config.Config{DataDir: "/config", BindAddr: "0.0.0.0:3625"}
	started := time.Date(2026, 7, 11, 9, 14, 2, 0, time.UTC)
	srv.deps.StartedAt = started
	srv.deps.DockerPinger = fakePinger{}
	srv.deps.DockerVersion = func(context.Context) (string, string, error) {
		return "27.3.1", "1.47", nil
	}

	out := systemInfoBody(t, srv, tok, csrf)

	if out["version"] != version.Version {
		t.Errorf("version = %v, want %q", out["version"], version.Version)
	}
	if out["go_version"] != runtime.Version() {
		t.Errorf("go_version = %v, want %q", out["go_version"], runtime.Version())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; out["platform"] != want {
		t.Errorf("platform = %v, want %q", out["platform"], want)
	}
	if out["db_path"] != "/config/dockbrr.db" {
		t.Errorf("db_path = %v, want /config/dockbrr.db", out["db_path"])
	}
	if out["bind_addr"] != "0.0.0.0:3625" {
		t.Errorf("bind_addr = %v", out["bind_addr"])
	}
	if out["data_dir"] != "/config" {
		t.Errorf("data_dir = %v", out["data_dir"])
	}
	if out["started_at"] != started.Format(time.RFC3339) {
		t.Errorf("started_at = %v, want %s", out["started_at"], started.Format(time.RFC3339))
	}

	docker, _ := out["docker"].(map[string]any)
	if docker["reachable"] != true || docker["version"] != "27.3.1" || docker["api_version"] != "1.47" {
		t.Errorf("docker = %v", docker)
	}

	auth, _ := out["auth"].(map[string]any)
	if auth["username"] != "admin" || auth["method"] != "password" {
		t.Errorf("auth = %v", auth)
	}

	// Never leak secrets, even by key name.
	for _, k := range []string{"github_token", "password", "password_hash", "session"} {
		if _, ok := out[k]; ok {
			t.Errorf("system info leaks %q", k)
		}
	}
}

// Missing VCS stamps (go run, -buildvcs=false) must degrade to empty strings,
// never a 500. The UI renders them as the DASH placeholder.
func TestSystemInfoBuildStampsOptional(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	out := systemInfoBody(t, srv, tok, csrf)
	for _, k := range []string{"commit", "build_date"} {
		if _, ok := out[k]; !ok {
			t.Errorf("field %q missing entirely; want present (possibly empty)", k)
		}
		if _, ok := out[k].(string); !ok {
			t.Errorf("field %q = %v, want string", k, out[k])
		}
	}
	if _, ok := out["commit_dirty"].(bool); !ok {
		t.Errorf("commit_dirty = %v, want bool", out["commit_dirty"])
	}
}

// A nil DockerVersion func (tests), a nil pinger (daemon never came up), and a
// failing ServerVersion must all degrade, never error the request.
func TestSystemInfoDockerDegrades(t *testing.T) {
	cases := map[string]struct {
		pinger  DockerPinger
		version func(context.Context) (string, string, error)
		want    bool
	}{
		"nil pinger":        {nil, nil, false},
		"ping fails":        {fakePinger{err: errors.New("connection refused")}, nil, false},
		"nil version func":  {fakePinger{}, nil, true},
		"version func errs": {fakePinger{}, func(context.Context) (string, string, error) { return "", "", errors.New("boom") }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _, tok, csrf := authedServer(t, Deps{})
			srv.deps.DockerPinger = tc.pinger
			srv.deps.DockerVersion = tc.version

			out := systemInfoBody(t, srv, tok, csrf)
			docker, _ := out["docker"].(map[string]any)
			if docker["reachable"] != tc.want {
				t.Errorf("reachable = %v, want %v", docker["reachable"], tc.want)
			}
			if _, ok := docker["version"]; ok {
				t.Errorf("version present on degraded docker: %v", docker)
			}
		})
	}
}

func TestSystemInfoRequiresAuth(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/info", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d, want 401", rec.Code)
	}
}
```

`fakePinger`, `authedServer`, and `authedGet` already exist in `internal/httpapi/status_test.go` / `testutil_test.go` / `updates_test.go`: same package, no import needed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/ -run TestSystemInfo -v`
Expected: compile failure: `srv.deps.StartedAt` / `srv.deps.DockerVersion` undefined.

- [ ] **Step 3: Add `ServerVersion` to the docker wrapper**

Append to `internal/docker/client.go`:

```go
// ServerVersion reports the daemon's version and the negotiated API version.
// Read-only: it queries the daemon, never mutates it.
func (cl *Client) ServerVersion(ctx context.Context) (version, apiVersion string, err error) {
	v, err := cl.c.ServerVersion(ctx)
	if err != nil {
		return "", "", err
	}
	return v.Version, v.APIVersion, nil
}
```

- [ ] **Step 4: Extend `Deps` and register the route**

In `internal/httpapi/server.go`, add to the `Deps` struct (right after `NextScan`):

```go
	// StartedAt is the process start time, reported by /api/system/info so the
	// UI can tick uptime client-side. Zero (as in tests) omits the field.
	StartedAt time.Time
	// DockerVersion reports the daemon + API version for /api/system/info.
	// Optional: nil (tests, Docker never came up) degrades to no version, not
	// an error: mirrors the NextScan idiom.
	DockerVersion func(ctx context.Context) (version, apiVersion string, err error)
```

Add `"context"` to the imports if not already present.

In `routes()`, inside the `requireAuth` group next to `r.Get("/api/status", s.handleStatus)`:

```go
		r.Get("/api/system/info", s.handleSystemInfo)
```

- [ ] **Step 5: Write the handler**

Create `internal/httpapi/system.go`:

```go
package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"dockbrr/internal/version"
)

type dockerInfoDTO struct {
	Reachable  bool   `json:"reachable"`
	Version    string `json:"version,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
}

type authInfoDTO struct {
	Username string `json:"username"`
	Method   string `json:"method"`
}

type systemInfoDTO struct {
	Version     string        `json:"version"`
	Commit      string        `json:"commit"`
	CommitDirty bool          `json:"commit_dirty"`
	BuildDate   string        `json:"build_date"`
	GoVersion   string        `json:"go_version"`
	Platform    string        `json:"platform"`
	StartedAt   string        `json:"started_at,omitempty"`
	Docker      dockerInfoDTO `json:"docker"`
	DBPath      string        `json:"db_path"`
	BindAddr    string        `json:"bind_addr"`
	DataDir     string        `json:"data_dir"`
	Auth        authInfoDTO   `json:"auth"`
}

// buildStamps reads the VCS metadata Go embeds automatically when building from
// a git checkout (no ldflags needed). Absent under `go run` or -buildvcs=false,
// in which case the zero values travel to the UI, which renders the DASH placeholder.
func buildStamps() (commit string, dirty bool, buildDate string) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, ""
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		case "vcs.time":
			buildDate = s.Value
		}
	}
	return commit, dirty, buildDate
}

// handleSystemInfo reports build, runtime, docker, storage and auth facts for
// the Settings → Application page. Read-only: it never mutates Docker or the
// store, and it deliberately carries no secrets (no tokens, no password hash).
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	commit, dirty, buildDate := buildStamps()
	out := systemInfoDTO{
		Version:     version.Version,
		Commit:      commit,
		CommitDirty: dirty,
		BuildDate:   buildDate,
		GoVersion:   runtime.Version(),
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		DBPath:      filepath.Join(s.cfg.DataDir, "dockbrr.db"),
		BindAddr:    s.cfg.BindAddr,
		DataDir:     s.cfg.DataDir,
		Auth:        authInfoDTO{Method: "password"},
	}
	if !s.deps.StartedAt.IsZero() {
		out.StartedAt = s.deps.StartedAt.UTC().Format(time.RFC3339)
	}
	if uid, ok := userIDFromCtx(r.Context()); ok {
		if u, err := s.userByID(uid); err == nil {
			out.Auth.Username = u.Username
		}
	}
	out.Docker.Reachable = s.dockerReachable(r)
	if out.Docker.Reachable && s.deps.DockerVersion != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if v, api, err := s.deps.DockerVersion(ctx); err == nil {
			out.Docker.Version, out.Docker.APIVersion = v, api
		}
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ ./internal/docker/ -run 'TestSystemInfo|TestStatus' -v`
Expected: PASS (all four `TestSystemInfo*` tests, existing status tests still green).

- [ ] **Step 7: Wire it in main.go**

In `cmd/dockbrr/main.go`, in the `deps := httpapi.Deps{...}` literal (around line 300), add:

```go
		StartedAt: time.Now(),
```

and extend the existing probe block (around line 314):

```go
	if dockerProbe != nil {
		deps.DockerPinger = dockerProbe
		deps.DockerVersion = dockerProbe.ServerVersion
	}
```

- [ ] **Step 8: Verify the binary builds and the suite is green**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: exit 0, all packages PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/httpapi/system.go internal/httpapi/system_test.go internal/httpapi/server.go internal/docker/client.go cmd/dockbrr/main.go
git commit -m "feat(api): read-only /api/system/info (build, runtime, docker, storage, auth)"
```

---

### Task 2: `SystemInfo` DTO + `useSystemInfo` query (frontend data layer)

**Files:**
- Modify: `web/src/api/types.ts` (append `SystemInfo` + `DockerInfo` + `AuthInfo`)
- Modify: `web/src/api/keys.ts` (add `systemInfo`)
- Modify: `web/src/hooks/queries.ts` (add `useSystemInfo`)

**Interfaces:**
- Consumes: `apiFetch<T>(path)` from `@/api/client`, `keys` from `@/api/keys`.
- Produces:
  - `interface SystemInfo`: the exact field names Task 4 renders.
  - `keys.systemInfo = ["system-info"] as const`
  - `useSystemInfo()`: `useQuery<SystemInfo>`, no polling (the page has a Refresh button).

- [ ] **Step 1: Add the DTO**

Append to `web/src/api/types.ts` (mirrors `systemInfoDTO` in `internal/httpapi/system.go` byte-for-byte):

```ts
export interface DockerInfo {
  reachable: boolean;
  /** Absent when the daemon is unreachable or the version probe failed. */
  version?: string;
  api_version?: string;
}
export interface AuthInfo {
  username: string;
  method: string;
}
export interface SystemInfo {
  version: string;
  /** Empty when built without VCS stamps (go run, -buildvcs=false). */
  commit: string;
  commit_dirty: boolean;
  /** RFC3339, or empty when built without VCS stamps. */
  build_date: string;
  go_version: string;
  platform: string;
  /** RFC3339 process start; uptime is derived client-side from this. */
  started_at?: string;
  docker: DockerInfo;
  db_path: string;
  bind_addr: string;
  data_dir: string;
  auth: AuthInfo;
}
```

- [ ] **Step 2: Add the query key**

In `web/src/api/keys.ts`, inside the `keys` object after `status`:

```ts
  systemInfo: ["system-info"] as const,
```

- [ ] **Step 3: Add the hook**

In `web/src/hooks/queries.ts`, after `useStatus`:

```ts
export const useSystemInfo = () =>
  useQuery({ queryKey: keys.systemInfo, queryFn: () => apiFetch<SystemInfo>("/api/system/info") });
```

Add `SystemInfo` to the existing `import type { ... } from "@/api/types";` list at the top of the file.

- [ ] **Step 4: Typecheck**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: exit 0, no output.

- [ ] **Step 5: Commit**

```bash
git add web/src/api/types.ts web/src/api/keys.ts web/src/hooks/queries.ts
git commit -m "feat(web): SystemInfo DTO + useSystemInfo query"
```

---

### Task 3: `SettingsCard` + `InfoRow` primitives

**Files:**
- Create: `web/src/components/settings/SettingsCard.tsx`
- Create: `web/src/components/settings/InfoRow.tsx`
- Create: `web/src/components/settings/SettingsCard.test.tsx`

**Interfaces:**
- Consumes: `cn` from `@/lib/cn`.
- Produces:
  - `SettingsCard({ title, description, action?, children }: { title: string; description?: string; action?: ReactNode; children: ReactNode })`
  - `DefaultHint()`: exported from `SettingsCard.tsx`; the "default" badge marking a field still holding the server-side default. Tasks 5 shares this one copy (both settings form pages need it).
  - `InfoRow({ label, value, sub? }: { label: string; value: ReactNode; sub?: ReactNode })`: renders `<dt>`/`<dd>`; caller wraps rows in `<dl className="divide-y divide-border">`.

Every settings page in Tasks 4–7 is built from these two.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/settings/SettingsCard.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SettingsCard, DefaultHint } from "@/components/settings/SettingsCard";
import { InfoRow } from "@/components/settings/InfoRow";

describe("SettingsCard", () => {
  it("renders title, description, action slot and body", () => {
    render(
      <SettingsCard title="Build" description="Version and build details." action={<button>Refresh</button>}>
        <p>body</p>
      </SettingsCard>,
    );
    expect(screen.getByRole("heading", { name: "Build" })).toBeInTheDocument();
    expect(screen.getByText("Version and build details.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
    expect(screen.getByText("body")).toBeInTheDocument();
  });

  it("renders without a description or action", () => {
    render(
      <SettingsCard title="Runtime">
        <p>body</p>
      </SettingsCard>,
    );
    expect(screen.getByRole("heading", { name: "Runtime" })).toBeInTheDocument();
  });
});

describe("DefaultHint", () => {
  it("renders the default badge", () => {
    render(<DefaultHint />);
    expect(screen.getByText("default")).toBeInTheDocument();
  });
});

describe("InfoRow", () => {
  it("renders label, value and optional sub-line", () => {
    render(
      <dl>
        <InfoRow label="Build date" value="29/06/2026 21:48:15" sub="13d 14h ago" />
      </dl>,
    );
    expect(screen.getByText("Build date")).toBeInTheDocument();
    expect(screen.getByText("29/06/2026 21:48:15")).toBeInTheDocument();
    expect(screen.getByText("13d 14h ago")).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- SettingsCard`
Expected: FAIL: cannot resolve `@/components/settings/SettingsCard`.

- [ ] **Step 3: Implement the primitives**

Create `web/src/components/settings/SettingsCard.tsx`:

```tsx
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface SettingsCardProps {
  title: string;
  description?: string;
  /** Right-aligned header slot (e.g. a Refresh or Save button). */
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}

export function SettingsCard({ title, description, action, className, children }: SettingsCardProps) {
  return (
    <section className={cn("rounded-lg border border-border bg-card", className)}>
      <div className="flex items-start justify-between gap-4 p-4">
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        {action && <div className="shrink-0">{action}</div>}
      </div>
      <div className="px-4 pb-4">{children}</div>
    </section>
  );
}

// Marks a field whose value still matches the server-side default, so an
// untouched setting reads differently from one deliberately set to that value.
export function DefaultHint() {
  return <span className="text-xs font-normal text-muted-foreground/70">default</span>;
}
```

Create `web/src/components/settings/InfoRow.tsx`:

```tsx
import type { ReactNode } from "react";

export interface InfoRowProps {
  label: string;
  value: ReactNode;
  /** Muted second line under the value (e.g. "13d 14h ago"). */
  sub?: ReactNode;
}

// A read-only key/value line. Callers wrap a run of rows in
// <dl className="divide-y divide-border"> so the dividers land between rows.
export function InfoRow({ label, value, sub }: InfoRowProps) {
  return (
    <div className="grid grid-cols-[10rem_1fr] gap-4 py-3 max-sm:grid-cols-1 max-sm:gap-1">
      <dt className="text-xs font-medium tracking-wider text-muted-foreground uppercase">{label}</dt>
      <dd className="space-y-0.5">
        <div className="font-mono text-sm break-all">{value}</div>
        {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
      </dd>
    </div>
  );
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm test -- SettingsCard`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/components/settings/SettingsCard.tsx web/src/components/settings/InfoRow.tsx web/src/components/settings/SettingsCard.test.tsx
git commit -m "feat(web): SettingsCard + InfoRow settings primitives"
```

---

### Task 4: Application settings page (Build · Runtime · Docker · Storage · Authentication · Backup)

**Files:**
- Create: `web/src/components/settings/ApplicationSettings.tsx`
- Create: `web/src/components/settings/ApplicationSettings.test.tsx`
- Modify: `web/src/components/settings/GeneralSettings.tsx` (remove the export/import block, it moves here)
- Modify: `web/src/routes/settings.tsx` (add an "Application" tab; the tabs strip is replaced wholesale in Task 8)

**Interfaces:**
- Consumes: `useSystemInfo()` (Task 2), `SettingsCard` + `InfoRow` (Task 3), `useNow()` from `@/hooks/useNow`, `apiFetch`, `keys`, `Button`, `toast` from `sonner`, `useQueryClient`.
- Produces: `ApplicationSettings()`: default export-free named component, rendered by Task 8's `/settings/application` route.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/settings/ApplicationSettings.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { ApplicationSettings } from "@/components/settings/ApplicationSettings";
import type { SystemInfo } from "@/api/types";

const FULL: SystemInfo = {
  version: "0.1.0-dev",
  commit: "b563d4be1234",
  commit_dirty: false,
  build_date: "2026-06-29T21:48:15Z",
  go_version: "go1.26.4",
  platform: "linux/amd64",
  started_at: "2026-07-12T12:00:00Z",
  docker: { reachable: true, version: "27.3.1", api_version: "1.47" },
  db_path: "/config/dockbrr.db",
  bind_addr: "0.0.0.0:3625",
  data_dir: "/config",
  auth: { username: "admin", method: "password" },
};

function renderPage(info: SystemInfo) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(info), { status: 200, headers: { "content-type": "application/json" } })),
  );
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <ApplicationSettings />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  // Freeze the clock so the uptime assertion is exact.
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-07-13T14:30:00Z"));
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("ApplicationSettings", () => {
  it("renders build, runtime, docker, storage and auth facts", async () => {
    renderPage(FULL);
    expect(await screen.findByText("0.1.0-dev")).toBeInTheDocument();
    expect(screen.getByText("b563d4b")).toBeInTheDocument(); // short commit
    expect(screen.getByText("go1.26.4")).toBeInTheDocument();
    expect(screen.getByText("linux/amd64")).toBeInTheDocument();
    expect(screen.getByText("/config/dockbrr.db")).toBeInTheDocument();
    expect(screen.getByText("0.0.0.0:3625")).toBeInTheDocument();
    expect(screen.getByText("27.3.1")).toBeInTheDocument();
    expect(screen.getByText("admin")).toBeInTheDocument();
  });

  it("derives uptime from started_at", async () => {
    renderPage(FULL); // started 2026-07-12T12:00:00Z, now 2026-07-13T14:30:00Z
    expect(await screen.findByText("1d 2h 30m")).toBeInTheDocument();
  });

  it("shows the dash placeholder when build stamps are absent", async () => {
    renderPage({ ...FULL, commit: "", build_date: "", started_at: undefined });
    // COMMIT, BUILD DATE and UPTIME all degrade to the DASH placeholder.
    expect(await screen.findAllByText(DASH)).toHaveLength(3);
  });

  it("reports an unreachable daemon without a version row", async () => {
    renderPage({ ...FULL, docker: { reachable: false } });
    expect(await screen.findByText("Unreachable")).toBeInTheDocument();
    expect(screen.queryByText("27.3.1")).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- ApplicationSettings`
Expected: FAIL: cannot resolve `@/components/settings/ApplicationSettings`.

- [ ] **Step 3: Implement the page**

Create `web/src/components/settings/ApplicationSettings.tsx`:

```tsx
import { useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { apiFetch } from "@/api/client";
import { keys } from "@/api/keys";
import { useSystemInfo } from "@/hooks/queries";
import { useNow } from "@/hooks/useNow";
import { Button } from "@/components/ui/button";
import { SettingsCard } from "@/components/settings/SettingsCard";
import { InfoRow } from "@/components/settings/InfoRow";

const DASH = "-";

// Uptime as "1d 2h 30m" / "2h 30m" / "45s", derived from started_at against a
// ticking clock, so it ages on screen without refetching.
function uptime(startedAt: string | undefined, now: Date): string {
  if (!startedAt) return DASH;
  const start = new Date(startedAt).getTime();
  if (Number.isNaN(start)) return DASH;
  let s = Math.max(0, Math.floor((now.getTime() - start) / 1000));
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (d > 0) return `${d}d ${h}h ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

function formatDate(iso: string): string {
  if (!iso) return DASH;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? DASH : d.toLocaleString();
}

function Rows({ children }: { children: React.ReactNode }) {
  return <dl className="divide-y divide-border border-t border-border">{children}</dl>;
}

export function ApplicationSettings() {
  const { data, isLoading, refetch, isFetching } = useSystemInfo();
  const now = useNow(1_000);
  const qc = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);

  if (isLoading || !data) {
    return <div className="h-40 animate-pulse rounded-lg bg-muted" role="status" aria-label="Loading system info" />;
  }

  const commit = data.commit ? data.commit.slice(0, 7) : DASH;

  return (
    <div className="space-y-4">
      <SettingsCard
        title="Build"
        description="Version and build details."
        action={
          <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>
            <RefreshCw className="mr-2 h-4 w-4" />
            Refresh
          </Button>
        }
      >
        <Rows>
          <InfoRow label="Version" value={data.version} />
          <InfoRow label="Commit" value={commit} sub={data.commit_dirty ? "working tree was dirty at build time" : undefined} />
          <InfoRow label="Build date" value={formatDate(data.build_date)} />
        </Rows>
      </SettingsCard>

      <SettingsCard title="Runtime" description="Server runtime information.">
        <Rows>
          <InfoRow label="Uptime" value={uptime(data.started_at, now)} />
          <InfoRow label="Runtime" value={`${data.go_version} • ${data.platform}`} />
        </Rows>
      </SettingsCard>

      <SettingsCard title="Docker" description="Daemon connection.">
        <Rows>
          <InfoRow label="Status" value={data.docker.reachable ? "Reachable" : "Unreachable"} />
          {data.docker.reachable && data.docker.version && (
            <InfoRow label="Daemon" value={data.docker.version} sub={data.docker.api_version ? `API ${data.docker.api_version}` : undefined} />
          )}
        </Rows>
      </SettingsCard>

      <SettingsCard title="Storage" description="Database and file system paths.">
        <Rows>
          <InfoRow label="Database" value={data.db_path} />
          <InfoRow label="Bind address" value={data.bind_addr} />
          <InfoRow label="Data directory" value={data.data_dir} />
        </Rows>
      </SettingsCard>

      <SettingsCard title="Authentication" description="Authentication configuration.">
        <Rows>
          <InfoRow label="Current session" value={data.auth.username} sub={data.auth.method} />
        </Rows>
      </SettingsCard>

      <SettingsCard title="Backup" description="Export or restore dockbrr's settings and registry credentials.">
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={() => {
              window.location.href = "/api/settings/export";
            }}
          >
            Export settings
          </Button>
          <Button variant="outline" onClick={() => fileRef.current?.click()}>
            Import settings
          </Button>
          <input
            ref={fileRef}
            type="file"
            accept="application/json"
            className="hidden"
            onChange={async (e) => {
              const file = e.target.files?.[0];
              if (!file) return;
              try {
                const body = JSON.parse(await file.text());
                await apiFetch("/api/settings/import", { method: "POST", body });
                toast.success("Settings imported");
                qc.invalidateQueries({ queryKey: keys.settings });
                qc.invalidateQueries({ queryKey: keys.registries });
              } catch {
                toast.error("Import failed: invalid file?");
              } finally {
                e.target.value = "";
              }
            }}
          />
        </div>
      </SettingsCard>
    </div>
  );
}
```

- [ ] **Step 4: Remove export/import from `GeneralSettings`**

In `web/src/components/settings/GeneralSettings.tsx`, delete the trailing `<div className="flex gap-2 border-t border-border pt-4">…</div>` block (the Export/Import buttons and the hidden file input), plus the now-unused `useRef`, `fileRef`, `useQueryClient`, `qc`, `apiFetch`, and `keys` imports/locals. Leave everything else untouched, Task 5 splits the rest.

- [ ] **Step 5: Add the Application tab (interim)**

In `web/src/routes/settings.tsx`, add as the FIRST tab so the page is reachable before Task 8 replaces the strip:

```tsx
<TabsTrigger value="application">Application</TabsTrigger>
```
```tsx
<TabsContent value="application">
  <ApplicationSettings />
</TabsContent>
```
Import `ApplicationSettings`, and change `<Tabs defaultValue="general">` to `<Tabs defaultValue="application">`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd web && npm test -- ApplicationSettings GeneralSettings`
Expected: PASS. If a `GeneralSettings` test asserted the Export/Import buttons, move that assertion into `ApplicationSettings.test.tsx` rather than deleting it.

- [ ] **Step 7: Full web check**

Run: `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: all suites PASS, typecheck exit 0, build succeeds.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/settings/ApplicationSettings.tsx web/src/components/settings/ApplicationSettings.test.tsx web/src/components/settings/GeneralSettings.tsx web/src/components/settings/GeneralSettings.test.tsx web/src/routes/settings.tsx
git commit -m "feat(web): Application settings page (build/runtime/docker/storage/auth/backup)"
```

---

### Task 5: `useSettingsForm` + split `GeneralSettings` into Scanning and Updates

**Files:**
- Create: `web/src/hooks/useSettingsForm.ts`
- Create: `web/src/hooks/useSettingsForm.test.tsx`
- Create: `web/src/components/settings/ScanningSettings.tsx`
- Create: `web/src/components/settings/UpdatesSettings.tsx`
- Create: `web/src/components/settings/ScanningSettings.test.tsx`
- Create: `web/src/components/settings/UpdatesSettings.test.tsx`
- Delete: `web/src/components/settings/GeneralSettings.tsx`, `web/src/components/settings/GeneralSettings.test.tsx`
- Modify: `web/src/routes/settings.tsx` (General tab → Scanning + Updates tabs)

**Interfaces:**
- Consumes: `useSettings()` + `useSaveSettings()` (existing), `Settings` DTO, `SettingsCard` (Task 3), `HelpTooltip`, `Label`, `Input`, `Switch`, `Button`.
- Produces:
  - `useSettingsForm(editableKeys: SettingKey[])` → `{ data: Settings | undefined, form, setField(key, value), isDefault(key), dirty, isSaving, save(extra?: Record<string, string>) }` where `SettingKey = keyof Omit<Settings, "github_token_set" | "restart_required" | "defaults">`. `save` PUTs only changed keys (plus any `extra`), toasts "Settings saved", and toasts "Concurrency applies after restart" when `concurrency` changed. Task 6 reuses it with `extra` for the GitHub token.
  - `ScanningSettings()`: poll interval, scan on launch, concurrency, registry cache TTL.
  - `UpdatesSettings()`: health timeout, health poll, write-back to compose, auto-remove gone, gone grace, job retention.

- [ ] **Step 1: Write the failing hook test**

Create `web/src/hooks/useSettingsForm.test.tsx`:

```tsx
import { renderHook, act, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import type { Settings } from "@/api/types";

const SETTINGS: Settings = {
  poll_interval_seconds: "900",
  scan_on_start: "true",
  concurrency: "4",
  health_timeout_seconds: "60",
  health_poll_seconds: "3",
  cache_ttl_seconds: "300",
  write_back_compose: "false",
  auto_remove_gone: "false",
  gone_grace_seconds: "86400",
  job_retention_days: "30",
  github_token_set: false,
  restart_required: [],
  defaults: { poll_interval_seconds: "900", concurrency: "4" },
};

const fetchMock = vi.fn();
function wrapper({ children }: { children: ReactNode }) {
  const client = makeQueryClient();
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function stubFetch() {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return new Response(null, { status: 204 });
    return new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
}

afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
});

describe("useSettingsForm", () => {
  it("is not dirty until a field changes, then sends only changed keys", async () => {
    stubFetch();
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds", "concurrency"]), { wrapper });

    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.dirty).toBe(false);

    act(() => result.current.setField("poll_interval_seconds", "600"));
    expect(result.current.dirty).toBe(true);

    act(() => result.current.save());
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ poll_interval_seconds: "600" });
    });
  });

  it("flags a field still matching the server-side default", async () => {
    stubFetch();
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds"]), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.isDefault("poll_interval_seconds")).toBe(true);

    act(() => result.current.setField("poll_interval_seconds", "600"));
    expect(result.current.isDefault("poll_interval_seconds")).toBe(false);
  });

  it("merges `extra` keys into the PUT (used for the write-only GitHub token)", async () => {
    stubFetch();
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds"]), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());

    act(() => result.current.save({ github_token: "ghp_x" }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ github_token: "ghp_x" });
    });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- useSettingsForm`
Expected: FAIL: cannot resolve `@/hooks/useSettingsForm`.

- [ ] **Step 3: Implement the hook**

Create `web/src/hooks/useSettingsForm.ts`:

```ts
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { useSettings } from "@/hooks/queries";
import { useSaveSettings } from "@/hooks/mutations";
import type { Settings } from "@/api/types";

/** The string-valued, user-editable settings keys (everything but the metadata fields). */
export type SettingKey = Exclude<keyof Settings, "github_token_set" | "restart_required" | "defaults">;

/**
 * Form state for a slice of the settings object: seeds from the server value,
 * tracks which fields diverge from it (the unsaved-changes indicator), and PUTs
 * only the changed keys so two settings pages editing different slices can never
 * clobber each other's fields.
 */
export function useSettingsForm(editableKeys: SettingKey[]) {
  const { data } = useSettings();
  const save = useSaveSettings();
  const [form, setForm] = useState<Partial<Record<SettingKey, string>>>({});

  useEffect(() => {
    if (!data) return;
    setForm(Object.fromEntries(editableKeys.map((k) => [k, data[k]])) as Partial<Record<SettingKey, string>>);
    // editableKeys is a stable literal per page; keyed on its contents, not identity.
  }, [data, editableKeys.join(",")]);

  const changed = (): Record<string, string> => {
    const patch: Record<string, string> = {};
    if (!data) return patch;
    for (const key of editableKeys) {
      const value = form[key];
      if (value !== undefined && value !== data[key]) patch[key] = value;
    }
    return patch;
  };

  const dirty = Object.keys(changed()).length > 0;

  return {
    data,
    form,
    dirty,
    isSaving: save.isPending,
    setField: (key: SettingKey, value: string) => setForm((f) => ({ ...f, [key]: value })),
    // A field still holding the server-side default reads differently from one
    // deliberately set to that same number.
    isDefault: (key: SettingKey) => {
      const value = form[key];
      return value !== undefined && value === data?.defaults?.[key];
    },
    save: (extra?: Record<string, string>, onSaved?: () => void) => {
      const patch = { ...changed(), ...(extra ?? {}) };
      if (Object.keys(patch).length === 0) return;
      save.mutate(patch, {
        onSuccess: () => {
          toast.success("Settings saved");
          if ("concurrency" in patch) toast.info("Concurrency applies after restart");
          onSaved?.();
        },
      });
    },
  };
}
```

- [ ] **Step 4: Run the hook test to verify it passes**

Run: `cd web && npm test -- useSettingsForm`
Expected: PASS (3 tests).

- [ ] **Step 5: Write the failing page tests**

Create `web/src/components/settings/ScanningSettings.test.tsx`:

```tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { ScanningSettings } from "@/components/settings/ScanningSettings";
import type { Settings } from "@/api/types";

const SETTINGS: Settings = {
  poll_interval_seconds: "900",
  scan_on_start: "true",
  concurrency: "4",
  health_timeout_seconds: "60",
  health_poll_seconds: "3",
  cache_ttl_seconds: "300",
  write_back_compose: "false",
  auto_remove_gone: "false",
  gone_grace_seconds: "86400",
  job_retention_days: "30",
  github_token_set: false,
  restart_required: [],
  defaults: { poll_interval_seconds: "900" },
};

const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
  if (init?.method === "PUT") return new Response(null, { status: 204 });
  return new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } });
});

afterEach(() => {
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderPage() {
  vi.stubGlobal("fetch", fetchMock);
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <ScanningSettings />
    </QueryClientProvider>,
  );
}

describe("ScanningSettings", () => {
  it("renders only the scanning fields", async () => {
    renderPage();
    expect(await screen.findByLabelText(/poll interval/i)).toHaveValue(900);
    expect(screen.getByLabelText(/scan on launch/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/concurrency/i)).toHaveValue(4);
    expect(screen.getByLabelText(/registry cache ttl/i)).toHaveValue(300);
    // Updates-page fields must not leak onto this page.
    expect(screen.queryByLabelText(/health timeout/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/job history retention/i)).not.toBeInTheDocument();
  });

  it("saves only the changed key and shows an unsaved-changes hint", async () => {
    const user = userEvent.setup();
    renderPage();
    const poll = await screen.findByLabelText(/poll interval/i);
    await user.clear(poll);
    await user.type(poll, "600");
    expect(screen.getByRole("status")).toHaveTextContent(/unsaved changes/i);

    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ poll_interval_seconds: "600" });
    });
  });
});
```

Create `web/src/components/settings/UpdatesSettings.test.tsx`: identical scaffolding (copy the `SETTINGS` fixture, `fetchMock`, `afterEach`, and `renderPage` above verbatim, importing `UpdatesSettings` instead), with these assertions:

```tsx
describe("UpdatesSettings", () => {
  it("renders only the update/apply fields", async () => {
    renderPage();
    expect(await screen.findByLabelText(/health timeout/i)).toHaveValue(60);
    expect(screen.getByLabelText(/health poll interval/i)).toHaveValue(3);
    expect(screen.getByLabelText(/write updates back to compose/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/auto-remove gone/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/gone-removal grace/i)).toHaveValue(86400);
    expect(screen.getByLabelText(/job history retention/i)).toHaveValue(30);
    // Scanning-page fields must not leak onto this page.
    expect(screen.queryByLabelText(/poll interval/i)).not.toBeInTheDocument();
  });

  it("saves a toggled switch as a string boolean", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByLabelText(/write updates back to compose/i));
    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ write_back_compose: "true" });
    });
  });
});
```

- [ ] **Step 6: Run the page tests to verify they fail**

Run: `cd web && npm test -- ScanningSettings UpdatesSettings`
Expected: FAIL: cannot resolve the two components.

- [ ] **Step 7: Implement the two pages**

Create `web/src/components/settings/ScanningSettings.tsx`:

```tsx
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, DefaultHint } from "@/components/settings/SettingsCard";
import { HelpTooltip } from "@/components/settings/HelpTooltip";
import { useSettingsForm, type SettingKey } from "@/hooks/useSettingsForm";

const KEYS: SettingKey[] = ["poll_interval_seconds", "scan_on_start", "concurrency", "cache_ttl_seconds"];

const NUMBER_FIELDS: Array<{ key: SettingKey; label: string; help: string }> = [
  {
    key: "poll_interval_seconds",
    label: "Poll interval (seconds)",
    help: "How often dockbrr scans your stacks for available image updates. Auto-update (when enabled) also applies updates on this interval.",
  },
  {
    key: "concurrency",
    label: "Concurrency",
    help: "Maximum number of registry checks run at once. Takes effect after a restart.",
  },
  {
    key: "cache_ttl_seconds",
    label: "Registry cache TTL (seconds)",
    help: "How long a registry digest lookup is cached before dockbrr re-queries the registry.",
  },
];

export function ScanningSettings() {
  const { data, form, dirty, isSaving, setField, isDefault, save } = useSettingsForm(KEYS);
  if (!data) return null;

  return (
    <SettingsCard title="Scanning" description="How often dockbrr looks for new images, and how hard it hits registries.">
      <div className="max-w-lg space-y-4">
        {NUMBER_FIELDS.map(({ key, label, help }) => (
          <div key={key} className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor={key}>{label}</Label>
              <HelpTooltip text={help} />
              {isDefault(key) && <DefaultHint />}
            </div>
            <Input
              id={key}
              type="number"
              className={isDefault(key) ? "text-muted-foreground" : undefined}
              value={form[key] ?? ""}
              onChange={(e) => setField(key, e.target.value)}
            />
          </div>
        ))}

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="scan_on_start">Scan on launch</Label>
            <HelpTooltip text="Run one scan as soon as dockbrr starts, instead of waiting a full poll interval for the first one. Detection only, auto-update still applies on the poll interval, so a restart never applies an update by itself." />
            {isDefault("scan_on_start") && <DefaultHint />}
          </div>
          <Switch
            id="scan_on_start"
            checked={form.scan_on_start === "true"}
            onCheckedChange={(checked) => setField("scan_on_start", checked ? "true" : "false")}
          />
        </div>

        <div className="flex items-center gap-3">
          <Button onClick={() => save()} disabled={isSaving || !dirty}>Save</Button>
          {dirty && (
            <span role="status" className="text-sm text-warning">
              Unsaved changes
            </span>
          )}
        </div>
      </div>
    </SettingsCard>
  );
}
```

Create `web/src/components/settings/UpdatesSettings.tsx`:

```tsx
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { SettingsCard, DefaultHint } from "@/components/settings/SettingsCard";
import { HelpTooltip } from "@/components/settings/HelpTooltip";
import { useSettingsForm, type SettingKey } from "@/hooks/useSettingsForm";

const KEYS: SettingKey[] = [
  "health_timeout_seconds",
  "health_poll_seconds",
  "write_back_compose",
  "auto_remove_gone",
  "gone_grace_seconds",
  "job_retention_days",
];

const NUMBER_FIELDS: Array<{ key: SettingKey; label: string; help: string }> = [
  {
    key: "health_timeout_seconds",
    label: "Health timeout (seconds)",
    help: "How long to wait for a recreated container to become healthy after an apply before rolling back.",
  },
  {
    key: "health_poll_seconds",
    label: "Health poll interval (seconds)",
    help: "How frequently the health check polls the recreated container during the timeout window.",
  },
  {
    key: "gone_grace_seconds",
    label: "Gone-removal grace (seconds)",
    help: "When auto-remove is on, how long a disappeared (gone) service is kept before it is deleted.",
  },
  {
    key: "job_retention_days",
    label: "Job history retention (days)",
    help: "Finished jobs (and their logs) older than this are deleted daily. Queued and running jobs are never removed. Set to 0 to keep job history forever.",
  },
];

export function UpdatesSettings() {
  const { data, form, dirty, isSaving, setField, isDefault, save } = useSettingsForm(KEYS);
  if (!data) return null;

  return (
    <SettingsCard title="Updates" description="How updates are applied, health-gated, and cleaned up.">
      <div className="max-w-lg space-y-4">
        {NUMBER_FIELDS.map(({ key, label, help }) => (
          <div key={key} className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor={key}>{label}</Label>
              <HelpTooltip text={help} />
              {isDefault(key) && <DefaultHint />}
            </div>
            <Input
              id={key}
              type="number"
              className={isDefault(key) ? "text-muted-foreground" : undefined}
              value={form[key] ?? ""}
              onChange={(e) => setField(key, e.target.value)}
            />
          </div>
        ))}

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="write_back_compose">Write updates back to compose files</Label>
            <HelpTooltip text="On apply, rewrite the image tag in your compose file so the update survives a manual recreate." />
            {isDefault("write_back_compose") && <DefaultHint />}
          </div>
          <Switch
            id="write_back_compose"
            checked={form.write_back_compose === "true"}
            onCheckedChange={(checked) => setField("write_back_compose", checked ? "true" : "false")}
          />
        </div>

        <div className="flex items-center justify-between">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="auto_remove_gone">Auto-remove gone services &amp; empty projects</Label>
            <HelpTooltip text="Automatically delete services whose containers have disappeared past the grace period, plus any project left empty." />
            {isDefault("auto_remove_gone") && <DefaultHint />}
          </div>
          <Switch
            id="auto_remove_gone"
            checked={form.auto_remove_gone === "true"}
            onCheckedChange={(checked) => setField("auto_remove_gone", checked ? "true" : "false")}
          />
        </div>

        <div className="flex items-center gap-3">
          <Button onClick={() => save()} disabled={isSaving || !dirty}>Save</Button>
          {dirty && (
            <span role="status" className="text-sm text-warning">
              Unsaved changes
            </span>
          )}
        </div>
      </div>
    </SettingsCard>
  );
}
```

- [ ] **Step 8: Delete `GeneralSettings` and repoint the tabs**

```bash
git rm web/src/components/settings/GeneralSettings.tsx web/src/components/settings/GeneralSettings.test.tsx
```

In `web/src/routes/settings.tsx`, replace the `general` trigger/content with two:

```tsx
<TabsTrigger value="scanning">Scanning</TabsTrigger>
<TabsTrigger value="updates">Updates</TabsTrigger>
```
```tsx
<TabsContent value="scanning">
  <ScanningSettings />
</TabsContent>
<TabsContent value="updates">
  <UpdatesSettings />
</TabsContent>
```
Swap the `GeneralSettings` import for `ScanningSettings` + `UpdatesSettings`. (The whole strip is replaced in Task 8; this keeps the app working in between.)

- [ ] **Step 9: Run tests to verify they pass**

Run: `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: all suites PASS (including the new Scanning/Updates tests), typecheck exit 0, build succeeds.

- [ ] **Step 10: Commit**

```bash
git add -A web/src
git commit -m "refactor(web): split GeneralSettings into Scanning + Updates behind useSettingsForm"
```

---

### Task 6: GitHub token card on the Registries page

**Files:**
- Modify: `web/src/components/settings/RegistriesSettings.tsx`
- Create: `web/src/components/settings/RegistriesSettings.test.tsx`

**Interfaces:**
- Consumes: `useSettingsForm([])` (Task 5) for the write-only token PUT, `useSettings()` for `github_token_set`, `SettingsCard` (Task 3).
- Produces: no new exports: `RegistriesSettings()` keeps its name and gains a second card.

**Write-only invariant (must hold):** the token value is never read back from the API, never prefilled into the input, and `github_token` appears in the PUT body only when the user typed a non-empty value. The placeholder shows `Set` / `Not set` from the boolean `github_token_set`.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/settings/RegistriesSettings.test.tsx`:

```tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { RegistriesSettings } from "@/components/settings/RegistriesSettings";
import type { Settings } from "@/api/types";

const SETTINGS: Settings = {
  poll_interval_seconds: "900",
  scan_on_start: "true",
  concurrency: "4",
  health_timeout_seconds: "60",
  health_poll_seconds: "3",
  cache_ttl_seconds: "300",
  write_back_compose: "false",
  auto_remove_gone: "false",
  gone_grace_seconds: "86400",
  job_retention_days: "30",
  github_token_set: true,
  restart_required: [],
  defaults: {},
};

const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
  if (init?.method === "PUT") return new Response(null, { status: 204 });
  if (String(url).includes("/api/registries")) {
    return new Response("[]", { status: 200, headers: { "content-type": "application/json" } });
  }
  return new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } });
});

afterEach(() => {
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderPage() {
  vi.stubGlobal("fetch", fetchMock);
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <RegistriesSettings />
    </QueryClientProvider>,
  );
}

describe("RegistriesSettings: GitHub token", () => {
  it("never prefills the token, only reports whether one is set", async () => {
    renderPage();
    const input = await screen.findByLabelText(/github token/i);
    expect(input).toHaveValue("");
    expect(input).toHaveAttribute("placeholder", "Set");
    expect(input).toHaveAttribute("type", "password");
  });

  it("sends github_token only when a value is typed, then clears the field", async () => {
    const user = userEvent.setup();
    renderPage();
    const input = await screen.findByLabelText(/github token/i);
    const saveToken = screen.getByRole("button", { name: /save token/i });

    // Untouched: nothing to send.
    expect(saveToken).toBeDisabled();

    await user.type(input, "ghp_secret");
    await user.click(saveToken);

    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ github_token: "ghp_secret" });
    });
    await waitFor(() => expect(input).toHaveValue(""));
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- RegistriesSettings`
Expected: FAIL: no "GitHub token" field on the page.

- [ ] **Step 3: Add the card**

In `web/src/components/settings/RegistriesSettings.tsx`: wrap the existing registry-credentials UI in `<SettingsCard title="Registry credentials" description="Credentials dockbrr uses to query private registries.">…</SettingsCard>`, wrap the whole return in `<div className="space-y-4">`, and append a second card:

```tsx
      <SettingsCard title="GitHub token" description="Used to fetch changelogs and release notes at a higher rate limit.">
        <div className="max-w-lg space-y-1">
          <div className="flex items-center gap-1.5">
            <Label htmlFor="github_token">GitHub token</Label>
            <HelpTooltip text="Personal access token used to fetch changelog and release notes from GitHub at a higher rate limit. Write-only, never displayed." />
          </div>
          <Input
            id="github_token"
            type="password"
            autoComplete="off"
            placeholder={settings.data?.github_token_set ? "Set" : "Not set"}
            value={githubToken}
            onChange={(e) => setGithubToken(e.target.value)}
          />
          <div className="pt-2">
            <Button
              onClick={() => form.save({ github_token: githubToken.trim() }, () => setGithubToken(""))}
              disabled={form.isSaving || githubToken.trim().length === 0}
            >
              Save token
            </Button>
          </div>
        </div>
      </SettingsCard>
```

with these additions at the top of the component:

```tsx
  const settings = useSettings();
  const form = useSettingsForm([]); // no editable fields here, only the write-only token extra
  const [githubToken, setGithubToken] = useState("");
```

and the imports `useSettings` (`@/hooks/queries`), `useSettingsForm` (`@/hooks/useSettingsForm`), `SettingsCard`, `HelpTooltip`, `Label`, `Input`, `Button`, `useState`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- RegistriesSettings`
Expected: PASS (2 tests).

- [ ] **Step 5: Full web check**

Run: `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/settings/RegistriesSettings.tsx web/src/components/settings/RegistriesSettings.test.tsx
git commit -m "feat(web): write-only GitHub token card on the Registries page"
```

---

### Task 7: `AddProjectDialog` + sidebar / dashboard triggers

**Files:**
- Create: `web/src/components/AddProjectDialog.tsx`
- Create: `web/src/components/AddProjectDialog.test.tsx`
- Delete: `web/src/components/settings/ManualProject.tsx`
- Modify: `web/src/components/layout/SidebarProjects.tsx` (`+` on the Projects header)
- Modify: `web/src/routes/dashboard.tsx` (action-row button + empty-state CTA)
- Modify: `web/src/routes/settings.tsx` (drop the Add project tab)

**Interfaces:**
- Consumes: `useCreateProject()` (existing: `mutate({ name, working_dir, config_files: string[] })` → `{ id, name }`), `Dialog`/`DialogContent`/`DialogHeader`/`DialogTitle`/`DialogDescription` from `@/components/ui/dialog`, `Button`, `Input`, `Label`, `HelpTooltip`, `toast`.
- Produces: `AddProjectDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void })`: controlled; closes itself on success.

**Note on `SidebarProjects`:** it currently early-returns `null` when there are no projects. The `+` header button must render regardless, so move the early return to cover only the project list, not the header.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/AddProjectDialog.test.tsx`:

```tsx
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { AddProjectDialog } from "@/components/AddProjectDialog";

const fetchMock = vi.fn(async () =>
  new Response(JSON.stringify({ id: 7, name: "media" }), { status: 200, headers: { "content-type": "application/json" } }),
);

afterEach(() => {
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderDialog(onOpenChange = vi.fn()) {
  vi.stubGlobal("fetch", fetchMock);
  const client = makeQueryClient();
  render(
    <QueryClientProvider client={client}>
      <AddProjectDialog open onOpenChange={onOpenChange} />
    </QueryClientProvider>,
  );
  return { onOpenChange };
}

describe("AddProjectDialog", () => {
  it("posts the project and closes on success", async () => {
    const user = userEvent.setup();
    const { onOpenChange } = renderDialog();

    await user.type(screen.getByLabelText(/^name$/i), "media");
    await user.type(screen.getByLabelText(/working directory/i), "/srv/media");
    await user.type(screen.getByLabelText(/compose files/i), "docker-compose.yml, override.yml");
    await user.click(screen.getByRole("button", { name: /add project/i }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "POST");
      expect(JSON.parse((post![1] as RequestInit).body as string)).toEqual({
        name: "media",
        working_dir: "/srv/media",
        config_files: ["docker-compose.yml", "override.yml"],
      });
    });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("does not submit without a name and working directory", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: /add project/i }));
    expect(fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "POST")).toBeUndefined();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- AddProjectDialog`
Expected: FAIL: cannot resolve `@/components/AddProjectDialog`.

- [ ] **Step 3: Implement the dialog**

Create `web/src/components/AddProjectDialog.tsx`:

```tsx
import { useState } from "react";
import { toast } from "sonner";
import { useCreateProject } from "@/hooks/mutations";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { HelpTooltip } from "@/components/settings/HelpTooltip";

function parseConfigFiles(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export interface AddProjectDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * Registers a compose project dockbrr could not auto-discover. Shared by the
 * sidebar "+" and the dashboard's Add project button, so both entry points get
 * the same form and the same invalidation on success (via useCreateProject).
 */
export function AddProjectDialog({ open, onOpenChange }: AddProjectDialogProps) {
  const createProject = useCreateProject();
  const [name, setName] = useState("");
  const [workingDir, setWorkingDir] = useState("");
  const [configFiles, setConfigFiles] = useState("");

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add project</DialogTitle>
          <DialogDescription>
            Register a compose project dockbrr did not discover from running containers.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!name || !workingDir) return;
            createProject.mutate(
              { name, working_dir: workingDir, config_files: parseConfigFiles(configFiles) },
              {
                onSuccess: (created) => {
                  toast.success(`Project "${created.name}" added`);
                  setName("");
                  setWorkingDir("");
                  setConfigFiles("");
                  onOpenChange(false);
                },
              },
            );
          }}
        >
          <div className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="project-name">Name</Label>
              <HelpTooltip text="Display name for this project in dockbrr. Any label you choose; it does not need to match the compose project name." />
            </div>
            <Input id="project-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="project-working-dir">Working directory</Label>
              <HelpTooltip text="Absolute path on the host where the compose files live. dockbrr runs all compose commands from here." />
            </div>
            <Input
              id="project-working-dir"
              value={workingDir}
              onChange={(e) => setWorkingDir(e.target.value)}
              placeholder="/srv/app"
            />
          </div>
          <div className="space-y-1">
            <div className="flex items-center gap-1.5">
              <Label htmlFor="project-config-files">Compose files</Label>
              <HelpTooltip text="Compose file names relative to the working directory. Separate multiple files with a comma or newline; order is preserved." />
            </div>
            <textarea
              id="project-config-files"
              className="flex min-h-16 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              value={configFiles}
              onChange={(e) => setConfigFiles(e.target.value)}
              placeholder="docker-compose.yml, docker-compose.override.yml"
            />
          </div>
          <Button type="submit" disabled={createProject.isPending}>Add project</Button>
        </form>
      </DialogContent>
    </Dialog>
  );
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm test -- AddProjectDialog`
Expected: PASS (2 tests).

- [ ] **Step 5: Add the sidebar trigger**

In `web/src/components/layout/SidebarProjects.tsx`:

- Add imports: `import { useState } from "react";`, `import { Plus } from "lucide-react";`, `import { AddProjectDialog } from "@/components/AddProjectDialog";`.
- Replace `if (projects.length === 0) return null;` with `const [addOpen, setAddOpen] = useState(false);` (the hook must run unconditionally. The `+` renders even with zero projects).
- Change the header block so the `+` sits beside the label, and gate only the *list* on `projects.length`:

```tsx
  return (
    <div className="flex min-h-0 flex-col">
      {!collapsed && (
        <div className="flex items-center justify-between px-3 py-2">
          <span className="text-xs font-medium tracking-wider text-muted-foreground uppercase">Projects</span>
          <button
            type="button"
            aria-label="Add project"
            onClick={() => setAddOpen(true)}
            className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
          >
            <Plus className="h-4 w-4" />
          </button>
        </div>
      )}
      <nav className="flex flex-col gap-1 overflow-y-auto px-2" aria-label="Projects">
        {projects.map((p) => { /* …unchanged… */ })}
      </nav>
      <AddProjectDialog open={addOpen} onOpenChange={setAddOpen} />
    </div>
  );
```

- [ ] **Step 6: Add the dashboard triggers**

In `web/src/routes/dashboard.tsx`:

```tsx
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { AddProjectDialog } from "@/components/AddProjectDialog";
```

Add `const [addOpen, setAddOpen] = useState(false);` beside the other state, extend the `Filters` `actions` slot:

```tsx
        actions={
          <>
            <ScanAllButton ariaLabel="Check all services" />
            <ApplyAllButton
              updates={updatesData}
              onApplied={setAppliedJobId}
              scopeNoun="across all projects"
              ariaLabel="Apply all available updates"
            />
            <Button variant="outline" onClick={() => setAddOpen(true)}>
              <Plus className="mr-2 h-4 w-4" />
              Add project
            </Button>
          </>
        }
```

give the empty state a CTA:

```tsx
      {!isLoading && !isError && rows.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          {projects.length === 0 ? (
            <div className="space-y-3">
              <p>No workloads discovered. Is the Docker socket mounted and reachable?</p>
              <Button variant="outline" onClick={() => setAddOpen(true)}>
                <Plus className="mr-2 h-4 w-4" />
                Add project
              </Button>
            </div>
          ) : (
            "No services match the current filters."
          )}
        </div>
      )}
```

and render the dialog once, next to `ApplyPanel`:

```tsx
      <AddProjectDialog open={addOpen} onOpenChange={setAddOpen} />
```

- [ ] **Step 7: Drop Add project from settings**

```bash
git rm web/src/components/settings/ManualProject.tsx
```
In `web/src/routes/settings.tsx`, remove the `add-project` `TabsTrigger` / `TabsContent` and the `ManualProject` import.

- [ ] **Step 8: Verify**

Run: `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: all suites PASS (existing `Sidebar.test.tsx` still green), typecheck exit 0, build succeeds.

- [ ] **Step 9: Commit**

```bash
git add -A web/src
git commit -m "feat(web): AddProjectDialog with sidebar + dashboard triggers, out of settings"
```

---

### Task 8: `SettingsLayout` + nested settings routes

**Files:**
- Create: `web/src/components/settings/SettingsLayout.tsx`
- Create: `web/src/routes/settings.test.tsx`
- Rewrite: `web/src/routes/settings.tsx` (Tabs → layout host)
- Modify: `web/src/router.tsx` (nested routes + index redirect)
- Modify: `web/src/components/settings/AutoUpdateToggles.tsx`, `web/src/components/settings/LogsSettings.tsx`, `web/src/components/settings/PasswordSettings.tsx` (wrap each in a `SettingsCard`)

**Interfaces:**
- Consumes: `SettingsCard` (Task 3), `ApplicationSettings` (Task 4), `ScanningSettings` + `UpdatesSettings` (Task 5), `RegistriesSettings` (Task 6), existing `AutoUpdateToggles` / `PasswordSettings` / `LogsSettings`, `rowClass` + `rowActiveClass` from `@/components/layout/SidebarNav`, `Link` + `Outlet` from `@tanstack/react-router`.
- Produces: `SettingsScreen()` (unchanged export name, `router.tsx` already imports it) rendering the heading + `SettingsLayout`; the seven child routes below.

Route table (spec §1):

| Path | Component |
|---|---|
| `/settings` | redirect → `/settings/application` |
| `/settings/application` | `ApplicationSettings` |
| `/settings/scanning` | `ScanningSettings` |
| `/settings/updates` | `UpdatesSettings` |
| `/settings/auto-update` | `AutoUpdateToggles` |
| `/settings/registries` | `RegistriesSettings` |
| `/settings/security` | `PasswordSettings` |
| `/settings/logs` | `LogsSettings` |

- [ ] **Step 1: Write the failing routing test**

Create `web/src/routes/settings.test.tsx`:

```tsx
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderApp } from "@/test/utils";

// Every settings page fetches; answer each endpoint with a benign payload so the
// test asserts routing, not data.
const fetchMock = vi.fn(async (url: string) => {
  const u = String(url);
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
  if (u.includes("/api/setup/status")) return json({ needs_setup: false });
  if (u.includes("/api/auth/me")) return json({ username: "admin" });
  if (u.includes("/api/system/info")) {
    return json({
      version: "0.1.0-dev", commit: "", commit_dirty: false, build_date: "",
      go_version: "go1.26.4", platform: "linux/amd64",
      docker: { reachable: true }, db_path: "/data/dockbrr.db", bind_addr: ":3625", data_dir: "/data",
      auth: { username: "admin", method: "password" },
    });
  }
  if (u.includes("/api/settings")) {
    return json({
      poll_interval_seconds: "900", scan_on_start: "true", concurrency: "4",
      health_timeout_seconds: "60", health_poll_seconds: "3", cache_ttl_seconds: "300",
      write_back_compose: "false", auto_remove_gone: "false", gone_grace_seconds: "86400",
      job_retention_days: "30", github_token_set: false, restart_required: [], defaults: {},
    });
  }
  if (u.includes("/api/logs/config")) return json({ path: "/data/dockbrr.log", level: "info", maxSizeMB: 10, maxBackups: 3 });
  return json([]); // projects, updates, registries, log files
});

afterEach(() => {
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderSettings(path = "/settings") {
  vi.stubGlobal("fetch", fetchMock);
  return renderApp(path);
}

describe("settings routing", () => {
  it("redirects /settings to the Application page", async () => {
    renderSettings("/settings");
    expect(await screen.findByRole("heading", { name: "Build" })).toBeInTheDocument();
  });

  it("deep-links straight to a sub-page", async () => {
    renderSettings("/settings/scanning");
    expect(await screen.findByLabelText(/poll interval/i)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Build" })).not.toBeInTheDocument();
  });

  it("navigates between sections from the sub-nav", async () => {
    const user = userEvent.setup();
    renderSettings("/settings/application");
    await screen.findByRole("heading", { name: "Build" });

    await user.click(screen.getByRole("link", { name: "Security" }));
    await waitFor(() => expect(screen.getByLabelText(/current password/i)).toBeInTheDocument());
  });

  it("does not offer an Add project section", async () => {
    renderSettings("/settings");
    await screen.findByRole("heading", { name: "Build" });
    expect(screen.queryByRole("link", { name: /add project/i })).not.toBeInTheDocument();
  });
});
```

If `PasswordSettings` labels its field something other than "Current password", match that label instead, check the component before running.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- settings`
Expected: FAIL: `/settings/scanning` does not resolve; the Tabs screen renders instead.

- [ ] **Step 3: Implement `SettingsLayout`**

Create `web/src/components/settings/SettingsLayout.tsx`:

```tsx
import { Link, Outlet } from "@tanstack/react-router";
import {
  Database,
  FileText,
  Info,
  Radar,
  RefreshCw,
  Shield,
  Zap,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/cn";
import { rowActiveClass, rowClass } from "@/components/layout/SidebarNav";

const SECTIONS: Array<{ to: string; label: string; icon: LucideIcon }> = [
  { to: "/settings/application", label: "Application", icon: Info },
  { to: "/settings/scanning", label: "Scanning", icon: Radar },
  { to: "/settings/updates", label: "Updates", icon: RefreshCw },
  { to: "/settings/auto-update", label: "Auto-update", icon: Zap },
  { to: "/settings/registries", label: "Registries", icon: Database },
  { to: "/settings/security", label: "Security", icon: Shield },
  { to: "/settings/logs", label: "Logs", icon: FileText },
];

/**
 * Settings shell: a section nav plus the active section. The nav reuses the app
 * sidebar's row classes so an active settings section reads identically to an
 * active app nav item. Below `md` it becomes a horizontal scroller, a second
 * vertical rail is unusable on a phone.
 */
export function SettingsLayout() {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 md:flex-row">
      <nav
        aria-label="Settings sections"
        className="flex shrink-0 gap-1 overflow-x-auto md:w-56 md:flex-col md:overflow-x-visible"
      >
        {SECTIONS.map(({ to, label, icon: Icon }) => (
          <Link
            key={to}
            to={to}
            className={cn(rowClass, "w-auto whitespace-nowrap md:w-full")}
            activeProps={{ className: cn(rowActiveClass, "w-auto whitespace-nowrap md:w-full") }}
          >
            <Icon className="h-4 w-4 shrink-0" />
            <span>{label}</span>
          </Link>
        ))}
      </nav>
      <div className="min-w-0 flex-1">
        <Outlet />
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Rewrite the settings route host**

Replace `web/src/routes/settings.tsx` entirely:

```tsx
import { SettingsLayout } from "@/components/settings/SettingsLayout";

export function SettingsScreen() {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <h1 className="text-xl font-semibold">Settings</h1>
      <SettingsLayout />
    </div>
  );
}
```

- [ ] **Step 5: Wire the nested routes**

In `web/src/router.tsx`, replace the single `settingsRoute` with a parent + children (the parent keeps the `SettingsScreen` component, which renders the `Outlet` via `SettingsLayout`):

```tsx
import { createRoute, createRouter, redirect } from "@tanstack/react-router";
import { ApplicationSettings } from "@/components/settings/ApplicationSettings";
import { ScanningSettings } from "@/components/settings/ScanningSettings";
import { UpdatesSettings } from "@/components/settings/UpdatesSettings";
import { AutoUpdateToggles } from "@/components/settings/AutoUpdateToggles";
import { RegistriesSettings } from "@/components/settings/RegistriesSettings";
import { PasswordSettings } from "@/components/settings/PasswordSettings";
import { LogsSettings } from "@/components/settings/LogsSettings";

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: SettingsScreen,
});

// /settings itself has no content, land on Application.
const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/settings/application" });
  },
});

const settingsSectionRoutes = [
  { path: "application", component: ApplicationSettings },
  { path: "scanning", component: ScanningSettings },
  { path: "updates", component: UpdatesSettings },
  { path: "auto-update", component: AutoUpdateToggles },
  { path: "registries", component: RegistriesSettings },
  { path: "security", component: PasswordSettings },
  { path: "logs", component: LogsSettings },
].map(({ path, component }) =>
  createRoute({ getParentRoute: () => settingsRoute, path, component }),
);

settingsRoute.addChildren([settingsIndexRoute, ...settingsSectionRoutes]);
```

Keep the `routeTree` line as-is: `settingsRoute` already appears in it, and its children now hang off it.

- [ ] **Step 6: Wrap the three untouched pages in cards**

`AutoUpdateToggles`, `PasswordSettings`, and `LogsSettings` each become a page, so each returns a `SettingsCard` around its existing body (no logic changes):

- `AutoUpdateToggles` → `<SettingsCard title="Auto-update" description="Apply updates automatically, per project or per service.">…</SettingsCard>`
- `PasswordSettings` → `<SettingsCard title="Password" description="Change the password for this account.">…</SettingsCard>`
- `LogsSettings` → `<SettingsCard title="Logs" description="Log level, rotation, and downloadable log files.">…</SettingsCard>`

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd web && npm test -- settings`
Expected: PASS (4 routing tests).

- [ ] **Step 8: Full verification**

Run: `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Then: `cd .. && CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: every suite green, both builds exit 0.

- [ ] **Step 9: Manual smoke check**

Run: `mise run dev` and open http://localhost:5173/settings
Confirm: it redirects to `/settings/application`; Build/Runtime/Docker/Storage/Authentication/Backup cards render with real values; uptime ticks; each nav row deep-links and survives a reload; the sidebar `+` and the dashboard "Add project" button both open the dialog; there is no Add project section in settings.

- [ ] **Step 10: Commit**

```bash
git add -A web/src
git commit -m "feat(web): settings left sub-nav with deep-linkable nested routes"
```

---

## Verification checklist (whole feature)

- [ ] `CGO_ENABLED=0 go build ./...` exits 0 (static-binary invariant).
- [ ] `go vet ./... && go test ./...` green.
- [ ] `cd web && npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build` green.
- [ ] `/settings` redirects to `/settings/application`; every section deep-links and survives a reload.
- [ ] `/api/system/info` returns 401 unauthenticated, and carries no token/password/session field.
- [ ] GitHub token is never prefilled and only PUT when typed.
- [ ] Add project is reachable from the sidebar `+`, the dashboard action row, and the empty state, and is gone from settings.
