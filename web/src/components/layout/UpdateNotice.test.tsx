import { beforeEach, describe, expect, test, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { delay, http, HttpResponse } from "msw";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() }, Toaster: () => null }));
import { toast } from "sonner";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { TooltipProvider } from "@/components/ui/tooltip";
import { UpdateNotice, DISMISS_KEY } from "./UpdateNotice";
import { clearDismissedUpdate } from "@/hooks/useDismissedUpdate";

const available = {
  current: "0.4.2",
  latest: "v0.5.0",
  html_url: "https://github.com/yorah/dockbrr/releases/tag/v0.5.0",
  update_available: true,
};

beforeEach(() => {
  localStorage.clear();
  vi.mocked(toast.error).mockClear();
  vi.mocked(toast.success).mockClear();
});

describe("UpdateNotice", () => {
  test("renders the card and a View Release link when an update is available", async () => {
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);

    expect(await screen.findByText(/update available/i)).toBeInTheDocument();
    expect(screen.getByText(/v0\.5\.0 is now available/i)).toBeInTheDocument();
    const link = screen.getByRole("link", { name: /view release/i });
    expect(link).toHaveAttribute("href", available.html_url);
    expect(link).toHaveAttribute("target", "_blank");
  });

  test("renders nothing when no update is available", async () => {
    server.use(http.get("/api/updates/self", () =>
      HttpResponse.json({ ...available, update_available: false })));
    const { container } = renderWithClient(<UpdateNotice collapsed={false} />);
    // Give the query a tick; the card must never appear.
    await waitFor(() => expect(screen.queryByText(/update available/i)).not.toBeInTheDocument());
    expect(container).toBeEmptyDOMElement();
  });

  test("dismiss hides the card and records the tag in localStorage", async () => {
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);

    const dismiss = await screen.findByRole("button", { name: /dismiss/i });
    await userEvent.click(dismiss);

    expect(localStorage.getItem(DISMISS_KEY)).toBe("v0.5.0");
    expect(screen.queryByText(/update available/i)).not.toBeInTheDocument();
  });

  test("stays hidden when the latest tag was already dismissed", async () => {
    localStorage.setItem(DISMISS_KEY, "v0.5.0");
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);
    await waitFor(() => expect(screen.queryByText(/update available/i)).not.toBeInTheDocument());
  });

  test("reappears when a newer tag ships after an old dismissal", async () => {
    localStorage.setItem(DISMISS_KEY, "v0.4.9"); // dismissed an older release
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);
    expect(await screen.findByText(/update available/i)).toBeInTheDocument();
  });

  test("an external dismissal clear re-shows the card without a remount", async () => {
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(<UpdateNotice collapsed={false} />);

    const dismiss = await screen.findByRole("button", { name: /dismiss/i });
    await userEvent.click(dismiss);
    expect(screen.queryByText(/update available/i)).not.toBeInTheDocument();

    clearDismissedUpdate();

    expect(await screen.findByText(/update available/i)).toBeInTheDocument();
  });

  test("Update now shows the self-update confirm, and posts to the apply endpoint only when confirmed", async () => {
    let posted = false;
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json(available)),
      http.post("/api/updates/self/apply", async () => {
        posted = true;
        await delay(50);
        return HttpResponse.json({ job_id: 5 });
      }),
      http.get("/api/jobs/5", () => HttpResponse.json({ id: 5, type: "self_update", status: "running" })),
    );
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    try {
      renderWithClient(<UpdateNotice collapsed={false} />);

      const btn = await screen.findByRole("button", { name: /update now/i });
      await userEvent.click(btn);

      expect(confirmSpy).toHaveBeenCalledWith(expect.stringContaining("Update dockbrr itself?"));
      expect(await screen.findByRole("button", { name: /updating/i })).toBeDisabled();
      await waitFor(() => expect(posted).toBe(true));
    } finally {
      confirmSpy.mockRestore();
    }
  });

  test("stays disabled while the enqueued self_update job runs, past the POST", async () => {
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json(available)),
      http.post("/api/updates/self/apply", () => HttpResponse.json({ job_id: 7 })),
      http.get("/api/jobs/7", () => HttpResponse.json({ id: 7, type: "self_update", status: "running" })),
    );
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    try {
      renderWithClient(<UpdateNotice collapsed={false} />);
      await userEvent.click(await screen.findByRole("button", { name: /update now/i }));

      // POST has resolved (job_id in hand) yet the job is still running: the
      // button must not flip back to an armed "Update now".
      const btn = await screen.findByRole("button", { name: /updating/i });
      await waitFor(() => expect(btn).toBeDisabled());
      expect(screen.queryByRole("button", { name: /^update now$/i })).not.toBeInTheDocument();
    } finally {
      confirmSpy.mockRestore();
    }
  });

  test("surfaces a failed self_update as an error toast and re-arms the button", async () => {
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json(available)),
      http.post("/api/updates/self/apply", () => HttpResponse.json({ job_id: 8 })),
      http.get("/api/jobs/8", () =>
        HttpResponse.json({ id: 8, type: "self_update", status: "failed", error: "pull ghcr.io/yorah/dockbrr:0.5.0 failed: manifest unknown" })),
    );
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    try {
      renderWithClient(<UpdateNotice collapsed={false} />);
      await userEvent.click(await screen.findByRole("button", { name: /update now/i }));

      await waitFor(() =>
        expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("manifest unknown"), expect.anything()));
      // Cleared job id re-enables the button so the user can retry later.
      await waitFor(() => expect(screen.getByRole("button", { name: /update now/i })).toBeEnabled());
    } finally {
      confirmSpy.mockRestore();
    }
  });

  test("stays disabled after the job succeeds, so a quick reclick can't re-fire it before the reload", async () => {
    let posts = 0;
    let polls = 0;
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json(available)),
      http.post("/api/updates/self/apply", () => {
        posts++;
        return HttpResponse.json({ job_id: 9 });
      }),
      http.get("/api/jobs/9", () => {
        polls++;
        // First poll still running; then terminal success. Success lands the
        // instant the swap helper is spawned, while the old server is briefly
        // still alive and before the SSE reconnect reloads the page.
        return HttpResponse.json({ id: 9, type: "self_update", status: polls === 1 ? "running" : "success" });
      }),
    );
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    try {
      renderWithClient(<UpdateNotice collapsed={false} />);
      await userEvent.click(await screen.findByRole("button", { name: /update now/i }));
      await waitFor(() => expect(posts).toBe(1));

      // Wait for the job poll to reach terminal success (2nd poll fires ~1.5s
      // after the first per useJob's interval, hence the extended timeout).
      await waitFor(() => expect(polls).toBeGreaterThanOrEqual(2), { timeout: 3000 });

      // Success is in. A fast user reclicking before the server restarts the
      // page must not enqueue a second self_update: the button stays disabled,
      // so the reclick is a no-op and posts stays at 1.
      await userEvent.click(screen.getByRole("button", { name: /updating|update now/i }));
      expect(posts).toBe(1);
      expect(screen.queryByRole("button", { name: /^update now$/i })).not.toBeInTheDocument();
    } finally {
      confirmSpy.mockRestore();
    }
  });

  test("Update now does not apply when the self-update confirm is cancelled", async () => {
    let posted = false;
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json(available)),
      http.post("/api/updates/self/apply", () => {
        posted = true;
        return HttpResponse.json({ job_id: 5 });
      }),
    );
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    try {
      renderWithClient(<UpdateNotice collapsed={false} />);

      const btn = await screen.findByRole("button", { name: /update now/i });
      await userEvent.click(btn);

      expect(confirmSpy).toHaveBeenCalled();
      expect(posted).toBe(false);
    } finally {
      confirmSpy.mockRestore();
    }
  });

  test("collapsed variant renders an icon-only link, no card text", async () => {
    server.use(http.get("/api/updates/self", () => HttpResponse.json(available)));
    renderWithClient(
      <TooltipProvider delayDuration={300}>
        <UpdateNotice collapsed={true} />
      </TooltipProvider>,
    );
    const link = await screen.findByRole("link", { name: /update available/i });
    expect(link).toHaveAttribute("href", available.html_url);
    expect(screen.queryByText(/view release/i)).not.toBeInTheDocument();
  });
});
