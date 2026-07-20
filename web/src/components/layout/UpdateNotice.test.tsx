import { beforeEach, describe, expect, test, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { delay, http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { TooltipProvider } from "@/components/ui/tooltip";
import { UpdateNotice, DISMISS_KEY } from "./UpdateNotice";

const available = {
  current: "0.4.2",
  latest: "v0.5.0",
  html_url: "https://github.com/yorah/dockbrr/releases/tag/v0.5.0",
  update_available: true,
};

beforeEach(() => {
  localStorage.clear();
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

  test("Update now shows the self-update confirm, and posts to the apply endpoint only when confirmed", async () => {
    let posted = false;
    server.use(
      http.get("/api/updates/self", () => HttpResponse.json(available)),
      http.post("/api/updates/self/apply", async () => {
        posted = true;
        await delay(50);
        return HttpResponse.json({ job_id: 5 });
      }),
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
