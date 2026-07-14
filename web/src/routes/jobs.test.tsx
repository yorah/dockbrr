import { expect, test } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { JobsScreen } from "./jobs";

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
