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
