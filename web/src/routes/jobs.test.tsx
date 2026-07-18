import { afterEach, expect, test } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { JobsScreen } from "./jobs";
import { __setEventSourceFactory } from "@/hooks/useJobLog";

class FakeES {
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  url: string;
  constructor(url: string) { this.url = url; }
  close() {}
}

afterEach(() => __setEventSourceFactory(null));

const finishedJob = {
  id: 2, type: "apply", status: "success", scope: "service", exit_code: 0, error: "",
  project_id: 1, service_id: 10, requested_by: "user",
  created_at: "2026-07-06T10:00:00Z", finished_at: "2026-07-06T10:00:05Z",
};
const runningJob = {
  id: 3, type: "check", status: "running", scope: "service", exit_code: null, error: "",
  project_id: 1, service_id: 10, requested_by: "scheduler",
  created_at: "2026-07-06T11:00:00Z", finished_at: "",
};

test("renders jobs rows with status badges", async () => {
  server.use(
    http.get("/api/jobs", () =>
      HttpResponse.json([
        {
          id: 2, type: "apply", status: "success", scope: "service", exit_code: 0, error: "",
          project_id: 1, service_id: 10, requested_by: "user",
          created_at: "2026-07-06T10:00:00Z", finished_at: "2026-07-06T10:00:05Z",
        },
        {
          id: 1, type: "check", status: "failed", scope: "service", exit_code: 1, error: "boom",
          project_id: 1, service_id: 10, requested_by: "scheduler",
          created_at: "2026-07-06T09:00:00Z", finished_at: "2026-07-06T09:00:02Z",
        },
      ]),
    ),
  );

  renderWithClient(<JobsScreen />);

  await waitFor(() => expect(screen.getByText("apply")).toBeInTheDocument());
  expect(screen.getByText("check")).toBeInTheDocument();
  expect(screen.getByText("success")).toBeInTheDocument();
  expect(screen.getByText("failed")).toBeInTheDocument();
  expect(screen.getByText("user")).toBeInTheDocument();
  expect(screen.getByText("scheduler")).toBeInTheDocument();
});

test("shows the empty state when there are no jobs", async () => {
  server.use(http.get("/api/jobs", () => HttpResponse.json([])));
  renderWithClient(<JobsScreen />);
  await waitFor(() => expect(screen.getByText(/no jobs have run yet/i)).toBeInTheDocument());
});

test("Clear finished is disabled when no job has finished", async () => {
  server.use(http.get("/api/jobs", () => HttpResponse.json([runningJob])));
  renderWithClient(<JobsScreen />);
  await waitFor(() =>
    expect(screen.getByRole("button", { name: /clear finished/i })).toBeDisabled(),
  );
});

test("Clear finished confirms before issuing the DELETE", async () => {
  let deleted = false;
  server.use(
    http.get("/api/jobs", () => HttpResponse.json(deleted ? [runningJob] : [finishedJob, runningJob])),
    http.delete("/api/jobs", () => {
      deleted = true;
      return HttpResponse.json({ deleted: 1 });
    }),
  );

  renderWithClient(<JobsScreen />);

  await userEvent.click(await screen.findByRole("button", { name: /clear finished/i }));
  // The dialog is open, but nothing has been deleted yet.
  expect(await screen.findByText(/cannot be undone/i)).toBeInTheDocument();
  expect(deleted).toBe(false);

  await userEvent.click(screen.getByRole("button", { name: /^clear$/i }));

  await waitFor(() => expect(deleted).toBe(true));
  // The list refetches: the finished job is gone, the running one survives.
  await waitFor(() => expect(screen.queryByText("apply")).toBeNull());
  expect(screen.getByText("check")).toBeInTheDocument();
});

test("Rollback appears only on the latest finished apply per service", async () => {
  server.use(
    http.get("/api/jobs", () =>
      HttpResponse.json([
        // newest-first: id 5 is the latest apply for service 10
        { id: 5, type: "apply", status: "success", scope: "service", exit_code: 0, error: "",
          project_id: 1, service_id: 10, requested_by: "user",
          created_at: "2026-07-06T12:00:00Z", finished_at: "2026-07-06T12:00:05Z" },
        { id: 4, type: "check", status: "success", scope: "service", exit_code: 0, error: "",
          project_id: 1, service_id: 10, requested_by: "scheduler",
          created_at: "2026-07-06T11:30:00Z", finished_at: "2026-07-06T11:30:02Z" },
        // older apply for the same service: no rollback offered
        { id: 2, type: "apply", status: "success", scope: "service", exit_code: 0, error: "",
          project_id: 1, service_id: 10, requested_by: "user",
          created_at: "2026-07-06T10:00:00Z", finished_at: "2026-07-06T10:00:05Z" },
        // apply without a service (project scope): not rollbackable
        { id: 1, type: "apply", status: "success", scope: "project", exit_code: 0, error: "",
          project_id: 1, service_id: null, requested_by: "user",
          created_at: "2026-07-06T09:00:00Z", finished_at: "2026-07-06T09:00:05Z" },
      ]),
    ),
  );

  renderWithClient(<JobsScreen />);

  await waitFor(() => expect(screen.getAllByText("apply").length).toBe(3));
  expect(screen.getAllByRole("button", { name: /rollback/i })).toHaveLength(1);
});

test("Rollback enqueues the job and opens the live panel", async () => {
  __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
  let posted = false;
  server.use(
    http.get("/api/jobs", () => HttpResponse.json([finishedJob])),
    http.post("/api/jobs/2/rollback", () => {
      posted = true;
      return HttpResponse.json({ job_id: 42 });
    }),
    http.get("/api/jobs/:id", () =>
      HttpResponse.json({ id: 42, type: "rollback", status: "running", scope: "service", exit_code: null, error: "" }),
    ),
  );

  renderWithClient(<JobsScreen />);

  await userEvent.click(await screen.findByRole("button", { name: /rollback/i }));

  await waitFor(() => expect(posted).toBe(true));
  // The live panel opened on the new rollback job.
  await waitFor(() => expect(screen.getByText(/rolling back \(job #42\)/i)).toBeInTheDocument());
});

test("cancelling the confirm dialog issues no DELETE", async () => {
  let calls = 0;
  server.use(
    http.get("/api/jobs", () => HttpResponse.json([finishedJob])),
    http.delete("/api/jobs", () => {
      calls++;
      return HttpResponse.json({ deleted: 1 });
    }),
  );

  renderWithClient(<JobsScreen />);

  await userEvent.click(await screen.findByRole("button", { name: /clear finished/i }));
  await userEvent.click(await screen.findByRole("button", { name: /cancel/i }));

  await waitFor(() => expect(screen.queryByText(/cannot be undone/i)).toBeNull());
  expect(calls).toBe(0);
});
