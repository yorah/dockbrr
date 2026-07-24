# Multi-job Apply Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a live per-job panel when "Apply all" enqueues 2+ jobs, listing every job with expandable logs and per-row rollback, instead of collapsing to a single arbitrary job.

**Architecture:** Extract today's single-job panel body into a reusable `JobLogView`. `ApplyPanel` becomes a thin wrapper around it (single-job path unchanged). A new `BulkApplyPanel` renders a list of `JobRow`s over `JobLogView`, computing aggregate progress + auto-close from the original apply job ids via `useQueries`. `ApplyAllButton` stops collapsing N->first and reports the full `{jobId, serviceId}[]` set; routes branch single vs bulk.

**Tech Stack:** React 19, TypeScript, TanStack Query v5 (`useQuery`, `useQueries`), Vitest + Testing Library + MSW, Tailwind v4, lucide-react icons.

## Global Constraints

- Frontend only. No Go/backend changes. Job engine, `/api/jobs/{id}`, `/api/jobs/{id}/logs` SSE reused as-is.
- TS typecheck via `npm run typecheck` (NOT `npx tsc`); `npm run build` is the backstop. Run from `web/`.
- Tests: `cd web && npm test` (vitest). MSW `onUnhandledRequest: "error"` — every network call a mounted component makes must have a handler.
- No `dangerouslySetInnerHTML`, no CDN, follow existing component/import conventions (`@/` alias).
- Job terminal statuses: `success | failed | canceled`. Failure statuses: `failed | canceled`. Source of truth: `store/jobs.go`, mirrored in `useJob`.

---

### Task 1: Shared `AppliedJob` type + `jobQueryOptions` factory

**Files:**
- Modify: `web/src/api/types.ts` (append interface)
- Modify: `web/src/hooks/queries.ts:50-60` (extract factory, refactor `useJob`)
- Test: `web/src/hooks/queries.test.ts` (create)

**Interfaces:**
- Produces: `AppliedJob = { jobId: number; serviceId: number }`
- Produces: `jobQueryOptions(id: number)` returning `{ queryKey, queryFn, refetchInterval }` consumable by both `useQuery` and `useQueries`.

- [ ] **Step 1: Write the failing test**

Create `web/src/hooks/queries.test.ts`:

```ts
import { expect, test } from "vitest";
import { jobQueryOptions } from "./queries";

test("jobQueryOptions builds the per-job query key", () => {
  expect(jobQueryOptions(5).queryKey).toEqual(["job", 5]);
});

test("jobQueryOptions stops polling on a terminal status and polls otherwise", () => {
  const opts = jobQueryOptions(5);
  const at = (status: string | undefined) =>
    opts.refetchInterval({ state: { data: status ? { status } : undefined } } as never);
  expect(at("running")).toBe(1500);
  expect(at("queued")).toBe(1500);
  expect(at(undefined)).toBe(1500);
  expect(at("success")).toBe(false);
  expect(at("failed")).toBe(false);
  expect(at("canceled")).toBe(false);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npx vitest run src/hooks/queries.test.ts`
Expected: FAIL — `jobQueryOptions` is not exported.

- [ ] **Step 3: Implement the factory + type**

In `web/src/api/types.ts`, append:

```ts
// Reported by ApplyAllButton for each enqueued apply job so the panel can list
// them and resolve each to its service. serviceId drives the row's label.
export interface AppliedJob {
  jobId: number;
  serviceId: number;
}
```

In `web/src/hooks/queries.ts`, replace the `useJob` definition (lines ~50-60) with:

```ts
// Shared query options for a single job, so useJob (one job) and useQueries
// (a bulk apply's N jobs) build identical keys/fetchers/polling. Terminal job
// statuses per store/jobs.go: success|failed|canceled — stop polling once the
// job finishes, keep polling while queued|running.
export function jobQueryOptions(id: number) {
  return {
    queryKey: keys.job(id),
    queryFn: () => apiFetch<Job>(`/api/jobs/${id}`),
    refetchInterval: (q: { state: { data?: Job } }) => {
      const s = q.state.data?.status;
      return s && ["success", "failed", "canceled"].includes(s) ? false : 1500;
    },
  };
}
export const useJob = (id: number, enabled = true) =>
  useQuery({ ...jobQueryOptions(id), enabled });
```

Confirm `Job` is imported in `queries.ts` (it is used by the current `useJob`; keep the existing import).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/hooks/queries.test.ts`
Expected: PASS (3 tests).

- [ ] **Step 5: Typecheck**

Run: `cd web && npm run typecheck`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/api/types.ts web/src/hooks/queries.ts web/src/hooks/queries.test.ts
git commit -m "feat(apply): extract jobQueryOptions factory and AppliedJob type"
```

---

### Task 2: Extract `JobLogView` from `ApplyPanel`

Split the panel body (status line, log box, terminal-status invalidation, auto-close countdown, in-place rollback swap) into a standalone `JobLogView`. `ApplyPanel` keeps its section chrome + title and delegates the body. Existing `ApplyPanel.test.tsx` is the regression guard — it must stay green unchanged.

**Files:**
- Create: `web/src/components/JobLogView.tsx`
- Modify: `web/src/components/ApplyPanel.tsx` (delegate body)
- Test: `web/src/components/JobLogView.test.tsx` (create), `web/src/components/ApplyPanel.test.tsx` (unchanged, must pass)

**Interfaces:**
- Consumes: `useJob`, `useJobLog`, `RollbackButton`, `keys`.
- Produces: `JobLogView` props `{ jobId: number; readOnly?: boolean; autoClose?: boolean; onClose?: () => void }`. It renders the `<StatusLine>`, the `apply-log` box, and (when `!readOnly` and the job failed) the rollback button. `autoClose` gates the 4s success countdown -> `onClose`. It owns the internal `setJobId` rollback swap and the terminal-status invalidation.
- Produces: `ApplyPanel` keeps the same public props `{ jobId, onClose, readOnly? }` and identical rendered DOM.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/JobLogView.test.tsx`:

```tsx
import { afterEach, expect, test, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { JobLogView } from "./JobLogView";
import { __setEventSourceFactory } from "@/hooks/useJobLog";

class FakeES {
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  url: string;
  static last: FakeES | null = null;
  constructor(url: string) { this.url = url; FakeES.last = this; }
  emit(data: string) { this.onmessage?.({ data } as MessageEvent); }
  close() {}
}

afterEach(() => __setEventSourceFactory(null));

test("streams log lines and shows the job status", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 5, type: "apply", status: "running", scope: "service", exit_code: null, error: "" })),
  );
  renderWithClient(<JobLogView jobId={5} />);
  expect(FakeES.last?.url).toContain("/api/jobs/5/logs");
  act(() => FakeES.last!.emit(JSON.stringify({ stream: "stdout", line: "Pulling web…" })));
  expect(screen.getByText("Pulling web…")).toBeInTheDocument();
});

test("does NOT auto-close on success when autoClose is false", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 6, type: "apply", status: "success", scope: "service", exit_code: 0, error: "" })),
  );
  const onClose = vi.fn();
  renderWithClient(<JobLogView jobId={6} autoClose={false} onClose={onClose} />);
  await waitFor(() => expect(screen.getByText(/^Applied/)).toBeInTheDocument());
  // Give the 4s window a beat; it must never fire when autoClose is off.
  await new Promise((r) => setTimeout(r, 100));
  expect(onClose).not.toHaveBeenCalled();
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npx vitest run src/components/JobLogView.test.tsx`
Expected: FAIL — cannot resolve `./JobLogView`.

- [ ] **Step 3: Create `JobLogView.tsx`**

Move the body logic out of `ApplyPanel.tsx`. Create `web/src/components/JobLogView.tsx`:

```tsx
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useJobLog } from "@/hooks/useJobLog";
import { useJob } from "@/hooks/queries";
import { keys } from "@/api/keys";
import { RollbackButton } from "@/components/RollbackButton";

const FAILED_STATUSES = new Set(["failed", "canceled"]);
const TERMINAL_STATUSES = new Set(["success", "failed", "canceled"]);
const AUTO_CLOSE_SUCCESS_MS = 4000;

const SUCCESS_LABELS: Record<string, string> = {
  apply: "Applied",
  rollback: "Rolled back",
  start: "Started",
  stop: "Stopped",
  restart: "Restarted",
  remove: "Removed",
  self_update: "Update started",
};

function StatusLine({ status, type, error, closingIn }: { status?: string; type?: string; error?: string; closingIn?: number }) {
  if (status === "success") {
    const suffix = closingIn !== undefined ? ` · closing in ${closingIn}s` : "";
    const label = (type && SUCCESS_LABELS[type]) || "Done";
    if (type === "rollback") {
      return <p className="text-sm font-medium text-warning">{label}{suffix}</p>;
    }
    return <p className="text-sm font-medium text-success">{label}{suffix}</p>;
  }
  if (status && FAILED_STATUSES.has(status)) {
    return (
      <p role="alert" className="text-sm font-medium text-danger">
        {error || (status === "canceled" ? "Canceled" : "Job failed")}
      </p>
    );
  }
  return <p className="text-sm text-muted-foreground">Health gate: waiting…</p>;
}

export interface JobLogViewProps {
  jobId: number;
  // readOnly renders a pure log viewer (no rollback, no invalidation): the
  // history/jobs screens inspect a past job.
  readOnly?: boolean;
  // When true, a successful job dismisses itself after AUTO_CLOSE_SUCCESS_MS
  // and calls onClose (single-job panel behavior). Bulk rows pass false; the
  // BulkApplyPanel owns the batch's lifecycle.
  autoClose?: boolean;
  onClose?: () => void;
}

// One job's live status + log + in-place rollback. Extracted from ApplyPanel so
// both the single-job panel and each BulkApplyPanel row render identically.
export function JobLogView({ jobId: initialJobId, readOnly = false, autoClose = false, onClose }: JobLogViewProps) {
  const [jobId, setJobId] = useState(initialJobId);
  const { lines } = useJobLog(jobId);
  const job = useJob(jobId, true);
  const status = job.data?.status;
  const jobType = job.data?.type;
  const logRef = useRef<HTMLDivElement>(null);
  const qc = useQueryClient();

  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  useEffect(() => {
    if (readOnly || !status || !TERMINAL_STATUSES.has(status)) return;
    void qc.invalidateQueries({ queryKey: keys.projects });
    void qc.invalidateQueries({ queryKey: keys.updates });
    void qc.invalidateQueries({ queryKey: keys.jobs });
  }, [readOnly, status, jobId, qc]);

  const [closingIn, setClosingIn] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (!autoClose || readOnly || status !== "success") return;
    const full = AUTO_CLOSE_SUCCESS_MS / 1000;
    const tick = setInterval(
      () => setClosingIn((s) => (s === undefined ? full - 1 : s > 1 ? s - 1 : s)),
      1000,
    );
    const t = onClose ? setTimeout(onClose, AUTO_CLOSE_SUCCESS_MS) : undefined;
    return () => {
      if (t) clearTimeout(t);
      clearInterval(tick);
      setClosingIn(undefined);
    };
  }, [autoClose, readOnly, status, onClose]);
  const closingInShown = autoClose && status === "success" && !readOnly ? (closingIn ?? AUTO_CLOSE_SUCCESS_MS / 1000) : undefined;

  return (
    <>
      <StatusLine status={status} type={jobType} error={job.data?.error} closingIn={closingInShown} />
      <div
        ref={logRef}
        data-testid="apply-log"
        className="mt-2 max-h-64 overflow-auto rounded-md bg-muted p-3 font-mono text-xs leading-relaxed text-foreground"
      >
        {lines.length === 0 ? (
          <p className="opacity-60">Waiting for log output…</p>
        ) : (
          lines.map((l, i) => (
            <div key={i} className={l.stream === "stderr" ? "text-danger" : undefined}>
              {l.line}
            </div>
          ))
        )}
      </div>
      {!readOnly && status && FAILED_STATUSES.has(status) && (
        <div className="mt-3 flex justify-end">
          <RollbackButton originalJobId={jobId} onRollback={setJobId} />
        </div>
      )}
    </>
  );
}
```

NOTE: the `TITLES` map and `panelTitle` stay in `ApplyPanel` (Step 4), NOT in `JobLogView` — the row header owns the job title, the body does not.

- [ ] **Step 4: Rewrite `ApplyPanel.tsx` to delegate**

Replace `web/src/components/ApplyPanel.tsx` with the chrome-only wrapper (keeps `TITLES`, `panelTitle`, header, and job number; body comes from `JobLogView`):

```tsx
import { JobLogView } from "@/components/JobLogView";
import { Button } from "@/components/ui/button";
import { useJob } from "@/hooks/queries";

export interface ApplyPanelProps {
  jobId: number;
  onClose: () => void;
  readOnly?: boolean;
}

const TITLES: Record<string, string> = {
  apply: "Applying update",
  rollback: "Rolling back",
  start: "Starting",
  stop: "Stopping",
  restart: "Restarting",
  remove: "Removing",
  self_update: "Updating dockbrr",
};

function panelTitle(readOnly: boolean, type?: string) {
  if (readOnly) return "Job log";
  return (type && TITLES[type]) || "Running job";
}

export function ApplyPanel({ jobId, onClose, readOnly = false }: ApplyPanelProps) {
  const job = useJob(jobId, true);
  return (
    <section
      aria-label="Apply progress"
      className="fixed inset-x-0 bottom-0 z-40 mx-auto w-full max-w-3xl rounded-t-lg border border-border bg-card p-4 shadow-lg"
    >
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-sm font-medium">
          {panelTitle(readOnly, job.data?.type)} (job #{jobId})
        </h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close apply panel">
          Close
        </Button>
      </header>
      <JobLogView jobId={jobId} readOnly={readOnly} autoClose={!readOnly} onClose={onClose} />
    </section>
  );
}
```

NOTE: `ApplyPanel` now calls `useJob(jobId)` only for the title's job type; `JobLogView` independently calls `useJob(jobId)` for the body. Both share the same cache key (`keys.job(jobId)`), so this is one network poll, not two.

- [ ] **Step 5: Run the full component suite**

Run: `cd web && npx vitest run src/components/JobLogView.test.tsx src/components/ApplyPanel.test.tsx`
Expected: PASS — the new JobLogView tests and ALL 8 existing ApplyPanel tests (title, status, log, rollback swap, auto-close, countdown, lifecycle/self_update titles).

- [ ] **Step 6: Typecheck**

Run: `cd web && npm run typecheck`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add web/src/components/JobLogView.tsx web/src/components/JobLogView.test.tsx web/src/components/ApplyPanel.tsx
git commit -m "refactor(apply): extract JobLogView from ApplyPanel"
```

---

### Task 3: `ApplyAllButton` reports every enqueued job

Stop collapsing N enqueues to the first response. Await all applies, mark each service busy, and report the full `AppliedJob[]` set once.

**Files:**
- Modify: `web/src/components/BulkActions.tsx:80-140` (`ApplyAllButton`)
- Test: `web/src/components/BulkActions.test.tsx` (update `onApplied` expectations + add a reporting test)

**Interfaces:**
- Consumes: `useApply().mutateAsync` returning `{ job_id: number }`, `markServiceBusy`, `AppliedJob`.
- Produces: `ApplyAllButton` prop `onApplied: (jobs: AppliedJob[]) => void` (type changed from `(jobId: number) => void`).

- [ ] **Step 1: Write the failing test**

Add to `web/src/components/BulkActions.test.tsx`:

```tsx
test("Apply all reports every enqueued job id, not just the first", async () => {
  server.use(
    http.post("/api/updates/:id/apply", ({ params }) =>
      HttpResponse.json({ job_id: Number(params.id) * 10 })),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  const onApplied = vi.fn();
  try {
    renderWithClient(
      <ApplyAllButton
        updates={[makeUpdate({ id: 1, service_id: 10 }), makeUpdate({ id: 2, service_id: 11 })]}
        onApplied={onApplied}
        scopeNoun="across all projects"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /apply all/i }));
    await waitFor(() => expect(onApplied).toHaveBeenCalledTimes(1));
    expect(onApplied).toHaveBeenCalledWith([
      { jobId: 10, serviceId: 10 },
      { jobId: 20, serviceId: 11 },
    ]);
  } finally {
    confirmSpy.mockRestore();
  }
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npx vitest run src/components/BulkActions.test.tsx`
Expected: FAIL — `onApplied` called with a number (first job) / type error, not the array.

- [ ] **Step 3: Rewrite `ApplyAllButton`'s type + onClick**

In `web/src/components/BulkActions.tsx`:

Add the import at the top (with the other `@/api/types` import):

```tsx
import type { AppliedJob, Update } from "@/api/types";
```

Change the prop type in the `ApplyAllButton` signature:

```tsx
  onApplied: (jobs: AppliedJob[]) => void;
```

Replace the `onClick` body (the `let opened = false; for (...) apply.mutate(...)` block) with:

```tsx
      onClick={async (e) => {
        e.stopPropagation();
        if (pending.length === 0) return;
        const n = pending.length;
        const anySelf = pending.some((u) => u.is_self);
        const base = `Apply ${n} available update${n > 1 ? "s" : ""} ${scopeNoun}? Each affected service is recreated individually.`;
        const msg = anySelf
          ? `${base} This includes dockbrr itself, which will restart and briefly disconnect this page.`
          : base;
        if (!window.confirm(msg)) return;
        // Enqueue every apply, then report the full set once so the panel can
        // list all jobs. allSettled: one failed enqueue POST must not drop the
        // rest. Order is preserved (map index == pending index).
        const results = await Promise.allSettled(
          pending.map((u) => apply.mutateAsync({ id: u.id, scope: "service" })),
        );
        const jobs: AppliedJob[] = [];
        results.forEach((r, i) => {
          if (r.status !== "fulfilled") return;
          const serviceId = pending[i].service_id;
          markServiceBusy(serviceId, r.value.job_id, "apply");
          jobs.push({ jobId: r.value.job_id, serviceId });
        });
        if (jobs.length > 0) onApplied(jobs);
      }}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/components/BulkActions.test.tsx`
Expected: PASS — the new reporting test plus the 3 existing confirm/cancel tests.

- [ ] **Step 5: Typecheck**

Run: `cd web && npm run typecheck`
Expected: errors at the call sites in `dashboard.tsx`, `project.$id.tsx`, and `DashboardTable.tsx` (they still pass `(jobId: number) => void`). These are fixed in Task 5 — note them and continue. Do NOT fix them here.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/BulkActions.tsx web/src/components/BulkActions.test.tsx
git commit -m "feat(apply): ApplyAllButton reports all enqueued job ids"
```

---

### Task 4: `BulkApplyPanel` + `JobRow`

Render the multi-job panel: aggregate header, one expandable row per job, per-row rollback, auto-close only when every job succeeded.

**Files:**
- Create: `web/src/components/BulkApplyPanel.tsx` (holds `BulkApplyPanel` + internal `JobRow`)
- Test: `web/src/components/BulkApplyPanel.test.tsx` (create)

**Interfaces:**
- Consumes: `jobQueryOptions` (Task 1), `useQueries`, `JobLogView` (Task 2), `AppliedJob` (Task 1), `Button`, lucide `ChevronRight`/`ChevronDown`/`Loader2`/`Check`/`X`.
- Produces: `BulkApplyPanel` props `{ jobs: AppliedJob[]; serviceNames: Map<number, string>; onClose: () => void }`.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/BulkApplyPanel.test.tsx`:

```tsx
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { BulkApplyPanel } from "./BulkApplyPanel";
import { __setEventSourceFactory } from "@/hooks/useJobLog";

class FakeES {
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  url: string;
  static last: FakeES | null = null;
  constructor(url: string) { this.url = url; FakeES.last = this; }
  close() {}
}

// Every test mounts a JobLogView via the auto-expanded row, which opens an
// EventSource — always route it through the fake so no real ES/network is hit.
beforeEach(() => __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource));
afterEach(() => __setEventSourceFactory(null));

const names = new Map<number, string>([[10, "web"], [11, "db"]]);
const jobs = [{ jobId: 100, serviceId: 10 }, { jobId: 101, serviceId: 11 }];

function jobHandler(map: Record<number, string>) {
  return http.get("/api/jobs/:id", ({ params }) =>
    HttpResponse.json({
      id: Number(params.id),
      type: "apply",
      status: map[Number(params.id)] ?? "running",
      scope: "service",
      exit_code: null,
      error: "",
    }));
}

test("header counts done/failed and lists a labeled row per job", async () => {
  server.use(jobHandler({ 100: "success", 101: "running" }));
  renderWithClient(<BulkApplyPanel jobs={jobs} serviceNames={names} onClose={vi.fn()} />);
  await waitFor(() => expect(screen.getByText(/1\/2 done/)).toBeInTheDocument());
  expect(screen.getByText(/0 failed/)).toBeInTheDocument();
  expect(screen.getByText("web")).toBeInTheDocument();
  expect(screen.getByText("db")).toBeInTheDocument();
});

test("auto-closes only when every job succeeds", async () => {
  vi.useFakeTimers();
  try {
    server.use(jobHandler({ 100: "success", 101: "success" }));
    const onClose = vi.fn();
    renderWithClient(<BulkApplyPanel jobs={jobs} serviceNames={names} onClose={onClose} />);
    await vi.waitFor(() => expect(screen.getByText(/2\/2 done/)).toBeInTheDocument());
    expect(onClose).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(4000);
    expect(onClose).toHaveBeenCalled();
  } finally {
    vi.useRealTimers();
  }
});

test("stays open when a job failed", async () => {
  vi.useFakeTimers();
  try {
    server.use(jobHandler({ 100: "success", 101: "failed" }));
    const onClose = vi.fn();
    renderWithClient(<BulkApplyPanel jobs={jobs} serviceNames={names} onClose={onClose} />);
    await vi.waitFor(() => expect(screen.getByText(/1 failed/)).toBeInTheDocument());
    await vi.advanceTimersByTimeAsync(6000);
    expect(onClose).not.toHaveBeenCalled();
  } finally {
    vi.useRealTimers();
  }
});

test("expanding a row subscribes to that job's log", async () => {
  server.use(jobHandler({ 100: "running", 101: "running" }));
  renderWithClient(<BulkApplyPanel jobs={jobs} serviceNames={names} onClose={vi.fn()} />);
  const dbRow = (await screen.findByText("db")).closest("li")!;
  await userEvent.click(within(dbRow).getByRole("button"));
  await waitFor(() => expect(FakeES.last?.url).toContain("/api/jobs/101/logs"));
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npx vitest run src/components/BulkApplyPanel.test.tsx`
Expected: FAIL — cannot resolve `./BulkApplyPanel`.

- [ ] **Step 3: Create `BulkApplyPanel.tsx`**

```tsx
import { useEffect, useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Check, Loader2, X } from "lucide-react";
import { jobQueryOptions } from "@/hooks/queries";
import { JobLogView } from "@/components/JobLogView";
import { Button } from "@/components/ui/button";
import type { AppliedJob, Job } from "@/api/types";

const TERMINAL = new Set(["success", "failed", "canceled"]);
const FAILED = new Set(["failed", "canceled"]);
const AUTO_CLOSE_SUCCESS_MS = 4000;

export interface BulkApplyPanelProps {
  jobs: AppliedJob[];
  // serviceId -> display name, resolved by the route from its cached projects.
  serviceNames: Map<number, string>;
  onClose: () => void;
}

function StatusIcon({ status }: { status?: string }) {
  if (status === "success") return <Check className="h-4 w-4 text-success" aria-label="success" />;
  if (status && FAILED.has(status)) return <X className="h-4 w-4 text-danger" aria-label="failed" />;
  return <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-label="running" />;
}

function JobRow({ job, name, data, defaultOpen }: { job: AppliedJob; name: string; data?: Job; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const status = data?.status;
  return (
    <li className="border-t border-border first:border-t-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 py-2 text-left text-sm"
      >
        {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        <span className="font-medium">{name}</span>
        <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          {status ?? "queued"}
          <StatusIcon status={status} />
        </span>
      </button>
      {open && (
        <div className="pb-3 pl-6">
          <JobLogView jobId={job.jobId} autoClose={false} />
        </div>
      )}
    </li>
  );
}

// Live panel for a batch apply (2+ jobs). Polls every original apply job for the
// aggregate + auto-close decision; each row expands to its own JobLogView (log +
// in-place rollback). Auto-closes only when EVERY apply succeeded.
export function BulkApplyPanel({ jobs, serviceNames, onClose }: BulkApplyPanelProps) {
  const results = useQueries({ queries: jobs.map((j) => jobQueryOptions(j.jobId)) });
  const statuses = results.map((r) => (r.data as Job | undefined)?.status);
  const done = statuses.filter((s) => s && TERMINAL.has(s)).length;
  const failed = statuses.filter((s) => s && FAILED.has(s)).length;
  const allSucceeded = jobs.length > 0 && statuses.every((s) => s === "success");

  // Auto-expand the first still-running row (else the first row) so a live log
  // shows immediately, fixing the "sat on a queued job" symptom.
  const firstRunningIndex = Math.max(0, statuses.findIndex((s) => s === "running"));

  useEffect(() => {
    if (!allSucceeded) return;
    const t = setTimeout(onClose, AUTO_CLOSE_SUCCESS_MS);
    return () => clearTimeout(t);
  }, [allSucceeded, onClose]);

  return (
    <section
      aria-label="Apply progress"
      className="fixed inset-x-0 bottom-0 z-40 mx-auto w-full max-w-3xl rounded-t-lg border border-border bg-card p-4 shadow-lg"
    >
      <header className="mb-2 flex items-center justify-between">
        <h2 className="text-sm font-medium">
          Applying {jobs.length} update{jobs.length > 1 ? "s" : ""} — {done}/{jobs.length} done, {failed} failed
        </h2>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close apply panel">
          Close
        </Button>
      </header>
      <ul className="max-h-80 overflow-auto">
        {jobs.map((j, i) => (
          <JobRow key={j.jobId} job={j} name={serviceNames.get(j.serviceId) ?? `service #${j.serviceId}`} data={results[i].data as Job | undefined} defaultOpen={i === firstRunningIndex} />
        ))}
      </ul>
    </section>
  );
}
```

NOTE: `defaultOpen` seeds `JobRow`'s open state once (on mount); it does not re-open a row the user later collapses, which is the desired behavior. The test "expanding a row subscribes to that job's log" clicks the `db` row (index 1) explicitly, so it does not depend on which row auto-opened.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/components/BulkApplyPanel.test.tsx`
Expected: PASS (4 tests).

- [ ] **Step 5: Typecheck**

Run: `cd web && npm run typecheck`
Expected: no NEW errors in `BulkApplyPanel.tsx` (route call-site errors from Task 3 persist until Task 5).

- [ ] **Step 6: Commit**

```bash
git add web/src/components/BulkApplyPanel.tsx web/src/components/BulkApplyPanel.test.tsx
git commit -m "feat(apply): add BulkApplyPanel for multi-job applies"
```

---

### Task 5: Wire routes + table to branch single vs bulk

Thread the batch callback from every `ApplyAllButton` (global + per-project-in-table) to the routes, which hold a discriminated panel state and render `ApplyPanel` (single) or `BulkApplyPanel` (2+).

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (add `onAppliedBatch` prop; thread to `ProjectBulkActions` -> `ApplyAllButton`)
- Modify: `web/src/routes/dashboard.tsx`
- Modify: `web/src/routes/project.$id.tsx`
- Test: `web/src/components/DashboardTable.test.tsx` (add job-status stubs to the two Apply-all tests so the opened panel's polling has a handler)

**Interfaces:**
- Consumes: `AppliedJob`, `ApplyPanel`, `BulkApplyPanel`, `ApplyAllButton` (new `onApplied` array type from Task 3).
- Produces: `DashboardTableProps.onAppliedBatch: (jobs: AppliedJob[]) => void` (row `onApplied: (jobId: number) => void` is unchanged).

- [ ] **Step 1: Add `onAppliedBatch` to `DashboardTable` and thread it**

In `web/src/components/DashboardTable.tsx`:

Add to `DashboardTableProps`:

```tsx
  /** Called with every job id after a project Apply-all enqueues, so the caller can open the bulk panel. */
  onAppliedBatch: (jobs: AppliedJob[]) => void;
```

Add `AppliedJob` to the `@/api/types` import.

Thread it through: the component destructures `onAppliedBatch` and passes it to `ProjectBulkActions` (add the prop to `ProjectBulkActions`'s signature/type — mirror `onApplied`), which passes `onApplied={onAppliedBatch}` to its `<ApplyAllButton>` (replacing the current `onApplied={onApplied}` on line ~421). The per-row `ActionsCell` `onApplied(res.job_id)` calls stay untouched.

- [ ] **Step 2: Branch the panel in `dashboard.tsx`**

Replace the `appliedJobId` single-number state with a discriminated union and render both panels.

Add import:

```tsx
import { BulkApplyPanel } from "@/components/BulkApplyPanel";
import type { AppliedJob } from "@/api/types";
```

Replace `const [appliedJobId, setAppliedJobId] = useState<number | null>(null);` with:

```tsx
type PanelState =
  | { kind: "single"; jobId: number }
  | { kind: "bulk"; jobs: AppliedJob[] }
  | null;
const [panel, setPanel] = useState<PanelState>(null);

// A batch of exactly 1 renders the plain single panel (no bulk chrome).
const openBatch = (jobs: AppliedJob[]) =>
  setPanel(jobs.length === 1 ? { kind: "single", jobId: jobs[0].jobId } : { kind: "bulk", jobs });
```

Build the serviceId->name map from the already-loaded projects (place near `applicableUpdates`):

```tsx
const serviceNames = new Map<number, string>(
  projects.flatMap((p) => p.services.map((s) => [s.id, s.name] as [number, string])),
);
```

Update the JSX wiring:
- `<ApplyAllButton ... onApplied={openBatch} />` (global button, replaces `onApplied={setAppliedJobId}`)
- `<DashboardTable ... onApplied={(jobId) => setPanel({ kind: "single", jobId })} onAppliedBatch={openBatch} />` (replaces `onApplied={setAppliedJobId}`)
- `ReviewDrawer`'s `onApplied={(jobId) => { setPanel({ kind: "single", jobId }); setSelected(null); }}`

Replace the trailing panel block:

```tsx
      {panel?.kind === "single" && (
        <ApplyPanel key={panel.jobId} jobId={panel.jobId} onClose={() => setPanel(null)} />
      )}
      {panel?.kind === "bulk" && (
        <BulkApplyPanel jobs={panel.jobs} serviceNames={serviceNames} onClose={() => setPanel(null)} />
      )}
```

- [ ] **Step 3: Branch the panel in `project.$id.tsx`**

Apply the same pattern. Add the `BulkApplyPanel` + `AppliedJob` imports. Replace `appliedJobId` state with the `PanelState`/`openBatch` pair (identical to Step 2). Build `serviceNames` from `project.services` (guard for `project` possibly undefined — use `project?.services ?? []`). Wire the top `<ApplyAllButton onApplied={openBatch} />` (line ~92), the `<DashboardTable onApplied={(jobId) => setPanel({ kind: "single", jobId })} onAppliedBatch={openBatch} />` (line ~114), the `ReviewDrawer` (line ~146), and replace the single `<ApplyPanel>` render (line ~154) with the single+bulk branch from Step 2.

- [ ] **Step 4: Stub job status in the Apply-all DashboardTable tests**

The two Apply-all tests now open a panel that polls `GET /api/jobs/:id`; with `onUnhandledRequest: "error"` that must be stubbed. In `web/src/components/DashboardTable.test.tsx`, add to the `server.use(...)` of BOTH "Apply all enqueues one service-scope apply per pending update…" (line ~226) and "global Check all runs a full scan; global Apply all…" (line ~435):

```tsx
    http.get("/api/jobs/:id", ({ params }) =>
      HttpResponse.json({ id: Number(params.id), type: "apply", status: "running", scope: "service", exit_code: null, error: "" })),
```

(The "gone service" test at line ~290 applies a single update -> single `ApplyPanel`, which already polls `GET /api/jobs/:id`; add the same stub there if it is not already present.)

- [ ] **Step 5: Run the full web suite**

Run: `cd web && npm test`
Expected: PASS — all suites, including the updated DashboardTable and BulkActions tests.

- [ ] **Step 6: Typecheck + build**

Run: `cd web && npm run typecheck && npm run build`
Expected: no type errors; build succeeds.

- [ ] **Step 7: Commit**

```bash
git add web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx web/src/routes/dashboard.tsx web/src/routes/project.\$id.tsx
git commit -m "feat(apply): open BulkApplyPanel for multi-job applies from both routes"
```

---

## Self-Review Notes

- **Spec coverage:** JobLogView extraction (Task 2), BulkApplyPanel+JobRow with expandable logs (Task 4), ApplyAllButton full-set reporting (Task 3), aggregate+auto-close from original ids (Task 4), per-row rollback via reused JobLogView (Task 2+4), service-label resolution from cached data (Task 5 `serviceNames`), batch-of-1 -> single panel (Task 5 `openBatch`), auto-expand first running row (Task 4 Step 3 note), no backend changes (all tasks). Covered.
- **Manual verification after Task 5:** `mise run dev`, trigger "Apply all" across a multi-service project, confirm the panel lists every job, rows expand to live logs, a failed row offers rollback, and the panel auto-closes only on full success.
- **Known trade-off (from spec):** a row's collapsed status badge keys off the original apply job; after an in-row rollback the expanded log follows the rollback while the badge/aggregate still count the apply as failed. Intended — keeps the panel open until the user dismisses.
