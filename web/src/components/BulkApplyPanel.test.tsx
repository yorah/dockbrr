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
