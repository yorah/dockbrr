import { beforeEach, expect, test, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { screen, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { __resetBusyServices } from "@/hooks/useBusyServices";
import { ApplyAllButton } from "./BulkActions";
import type { Update } from "@/api/types";

beforeEach(() => __resetBusyServices());

function makeUpdate(overrides: Partial<Update> = {}): Update {
  return {
    id: 1,
    service_id: 10,
    from_digest: "sha256:a",
    to_digest: "sha256:b",
    from_version: "",
    to_version: "",
    tag: "1.1",
    severity: "minor",
    changelog_url: "",
    changelog_text: "",
    status: "available",
    detected_at: "2026-07-04T00:00:00Z",
    is_self: false,
    ...overrides,
  };
}

test("Apply all warns that dockbrr itself is included when any pending update is_self", async () => {
  server.use(http.post("/api/updates/:id/apply", () => HttpResponse.json({ job_id: 1 })));
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
  try {
    renderWithClient(
      <ApplyAllButton
        updates={[makeUpdate({ id: 1, service_id: 10, is_self: false }), makeUpdate({ id: 2, service_id: 11, is_self: true })]}
        onApplied={vi.fn()}
        scopeNoun="across all projects"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /apply all/i }));
    expect(confirmSpy).toHaveBeenCalledWith(expect.stringContaining("dockbrr itself"));
  } finally {
    confirmSpy.mockRestore();
  }
});

test("Apply all does not mention dockbrr itself when no pending update is_self", async () => {
  server.use(http.post("/api/updates/:id/apply", () => HttpResponse.json({ job_id: 1 })));
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
  try {
    renderWithClient(
      <ApplyAllButton
        updates={[makeUpdate({ id: 1, service_id: 10, is_self: false })]}
        onApplied={vi.fn()}
        scopeNoun="across all projects"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /apply all/i }));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    const message = confirmSpy.mock.calls[0][0] as string;
    expect(message).not.toContain("dockbrr itself");
  } finally {
    confirmSpy.mockRestore();
  }
});

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

test("Apply all skips every mutation when the confirm is cancelled", async () => {
  let applyCalls = 0;
  server.use(
    http.post("/api/updates/:id/apply", () => {
      applyCalls += 1;
      return HttpResponse.json({ job_id: 1 });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
  try {
    renderWithClient(
      <ApplyAllButton
        updates={[makeUpdate({ id: 1, service_id: 10, is_self: true })]}
        onApplied={vi.fn()}
        scopeNoun="across all projects"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /apply all/i }));
    await waitFor(() => expect(confirmSpy).toHaveBeenCalled());
    expect(applyCalls).toBe(0);
  } finally {
    confirmSpy.mockRestore();
  }
});
