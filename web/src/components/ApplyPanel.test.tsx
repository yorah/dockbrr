import { afterEach, expect, test, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { ApplyPanel } from "./ApplyPanel";
import { __setEventSourceFactory } from "@/hooks/useJobLog";

class FakeES {
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  url: string;
  closed = false;
  static last: FakeES | null = null;
  constructor(url: string) { this.url = url; FakeES.last = this; }
  emit(data: string) { this.onmessage?.({ data } as MessageEvent); }
  close() { this.closed = true; }
}

afterEach(() => __setEventSourceFactory(null));

test("shows failure + Rollback, streams log lines, and swaps to the rollback job", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", ({ params }) =>
      params.id === "200"
        ? HttpResponse.json({ id: 200, type: "rollback", status: "running", scope: "service", exit_code: null, error: "" })
        : HttpResponse.json({ id: 99, type: "apply", status: "failed", scope: "service", exit_code: 1, error: "health gate: timeout" }),
    ),
    http.post("/api/jobs/99/rollback", () => HttpResponse.json({ job_id: 200 })),
  );

  const onClose = vi.fn();
  renderWithClient(<ApplyPanel jobId={99} onClose={onClose} />);

  // The SSE source opened on the initial (job 99) log endpoint.
  expect(FakeES.last?.url).toContain("/api/jobs/99/logs");

  // Feed a couple of live log lines and assert they render.
  act(() => FakeES.last!.emit(JSON.stringify({ stream: "stdout", line: "Pulling web…" })));
  act(() => FakeES.last!.emit(JSON.stringify({ stream: "stderr", line: "recreate failed" })));
  expect(screen.getByText("Pulling web…")).toBeInTheDocument();
  expect(screen.getByText("recreate failed")).toBeInTheDocument();

  // The failed health gate surfaces the job error and a Rollback button.
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("health gate: timeout"));
  const rollbackBtn = screen.getByRole("button", { name: /rollback/i });

  // Clicking Rollback enqueues a new job and swaps the panel to it.
  await userEvent.click(rollbackBtn);

  await waitFor(() => expect(screen.getByText(/job #200/i)).toBeInTheDocument());
  expect(FakeES.last?.url).toContain("/api/jobs/200/logs");
  // The new (running) job no longer shows the failure/rollback affordances.
  expect(screen.queryByRole("button", { name: /rollback/i })).not.toBeInTheDocument();
  expect(screen.getByText(/health gate: waiting/i)).toBeInTheDocument();
});

test("shows Applied and no rollback when the apply job succeeds", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 7, type: "apply", status: "success", scope: "service", exit_code: 0, error: "" })),
  );
  renderWithClient(<ApplyPanel jobId={7} onClose={vi.fn()} />);
  await waitFor(() => expect(screen.getByText(/^Applied/)).toBeInTheDocument());
  expect(screen.queryByText(/health gate: waiting/i)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /rollback/i })).not.toBeInTheDocument();
});

test("labels a successful rollback job as Rolled back", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 8, type: "rollback", status: "success", scope: "service", exit_code: 0, error: "" })),
  );
  renderWithClient(<ApplyPanel jobId={8} onClose={vi.fn()} />);
  await waitFor(() => expect(screen.getByText(/^Rolled back/)).toBeInTheDocument());
});

test("auto-closes a few seconds after the job succeeds", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 7, type: "apply", status: "success", scope: "service", exit_code: 0, error: "" })),
  );
  const onClose = vi.fn();
  renderWithClient(<ApplyPanel jobId={7} onClose={onClose} />);
  await waitFor(() => expect(screen.getByText(/^Applied/)).toBeInTheDocument());
  expect(onClose).not.toHaveBeenCalled();
  // Success dismisses on its own after AUTO_CLOSE_SUCCESS_MS (4s); failures
  // are covered by the rollback tests above, which rely on the panel staying.
  await waitFor(() => expect(onClose).toHaveBeenCalled(), { timeout: 6000 });
}, 10000);

test("success line shows the closing countdown", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 7, type: "apply", status: "success", scope: "service", exit_code: 0, error: "" })),
  );
  renderWithClient(<ApplyPanel jobId={7} onClose={vi.fn()} />);
  expect(await screen.findByText(/Applied · closing in \ds/)).toBeInTheDocument();
});

test("lifecycle jobs get their own title and success label", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  server.use(
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 9, type: "stop", status: "success", scope: "service", exit_code: 0, error: "" })),
  );
  renderWithClient(<ApplyPanel jobId={9} onClose={vi.fn()} />);
  expect(await screen.findByText(/Stopping \(job #9\)/)).toBeInTheDocument();
  expect(screen.getByText(/^Stopped/)).toBeInTheDocument();
  expect(screen.queryByText(/^Applied/)).not.toBeInTheDocument();
});
