import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { makeQueryClient } from "@/api/queryClient";
import { ScanningSettings } from "@/components/settings/ScanningSettings";
import type { Settings } from "@/api/types";

// Local mock so the "concurrency applies after restart" toast can be asserted
// without a real <Toaster/> mounted (nothing else in this file needs sonner).
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

const SETTINGS: Settings = {
  poll_interval_seconds: "900",
  scan_on_start: "true",
  concurrency: "4",
  health_timeout_seconds: "60",
  health_poll_seconds: "3",
  write_back_compose: "false",
  auto_remove_gone: "false",
  default_auto_update_enabled: "false",
  gone_grace_seconds: "86400",
  job_retention_days: "30",
  github_token_set: false,
  restart_required: [],
  // poll_interval_seconds and concurrency both sit on their server-side
  // default, so the "default" hint is exercised for both.
  defaults: { poll_interval_seconds: "900", concurrency: "4" },
};

const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
  if (init?.method === "PUT") return new Response(null, { status: 204 });
  return new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } });
});

afterEach(() => {
  fetchMock.mockClear();
  vi.mocked(toast.info).mockClear();
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
  it("shows a loading skeleton (not a blank page) before settings arrive", async () => {
    let resolveFetch!: (r: Response) => void;
    const pending = new Promise<Response>((r) => { resolveFetch = r; });
    vi.stubGlobal("fetch", vi.fn(() => pending));
    const client = makeQueryClient();
    render(
      <QueryClientProvider client={client}>
        <ScanningSettings />
      </QueryClientProvider>,
    );

    expect(screen.getByRole("status", { name: /loading/i })).toBeInTheDocument();

    resolveFetch(new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } }));
    expect(await screen.findByLabelText(/^poll interval/i)).toBeInTheDocument();
  });

  it("renders only the scanning fields", async () => {
    renderPage();
    // Anchored: the "scan on launch" help text also mentions "a full poll
    // interval" in prose, which a bare /poll interval/i would also match.
    expect(await screen.findByLabelText(/^poll interval/i)).toHaveValue(900);
    expect(screen.getByLabelText(/scan on launch/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/concurrency/i)).toHaveValue(4);
    // Updates-page fields must not leak onto this page.
    expect(screen.queryByLabelText(/health timeout/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/job history retention/i)).not.toBeInTheDocument();
  });

  it("saves only the changed key and shows an unsaved-changes hint", async () => {
    const user = userEvent.setup();
    renderPage();
    const poll = await screen.findByLabelText(/^poll interval/i);
    await user.clear(poll);
    await user.type(poll, "600");
    expect(screen.getByRole("status")).toHaveTextContent(/unsaved changes/i);

    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ poll_interval_seconds: "600" });
    });
  });

  // Carried over from GeneralSettings.test.tsx ("Save sends a PUT with only
  // the changed field"): concurrency is a Scanning-page field now.
  it("sends a PUT with only the changed field when concurrency changes", async () => {
    const user = userEvent.setup();
    renderPage();
    const concurrency = await screen.findByLabelText(/concurrency/i);
    await user.clear(concurrency);
    await user.type(concurrency, "8");

    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ concurrency: "8" });
    });

    // Changing concurrency specifically warns that it needs a restart.
    await waitFor(() => expect(toast.info).toHaveBeenCalledWith(expect.stringMatching(/^Concurrency applies after restart/), expect.anything()));
  });

  // Carried over from GeneralSettings.test.tsx ("dims a field left at its
  // default and undims it once edited away"), scoped to Scanning's fields.
  it("dims a field left at its default and undims it once edited away", async () => {
    const user = userEvent.setup();
    renderPage();

    const concurrency = await screen.findByLabelText(/concurrency/i);
    expect(concurrency).toHaveClass("text-muted-foreground");
    expect(screen.getAllByText("default")).toHaveLength(2); // poll interval + concurrency

    await user.clear(concurrency);
    await user.type(concurrency, "8");
    expect(concurrency).not.toHaveClass("text-muted-foreground");
    expect(screen.getAllByText("default")).toHaveLength(1);
  });

  // Carried over from GeneralSettings.test.tsx ("scan-on-launch toggles and
  // saves on its own").
  it("scan-on-launch toggles and saves on its own", async () => {
    const user = userEvent.setup();
    renderPage();

    const toggle = await screen.findByRole("switch", { name: /scan on launch/i });
    expect(toggle).toHaveAttribute("aria-checked", "true");

    await user.click(toggle);
    await user.click(screen.getByRole("button", { name: /save/i }));

    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ scan_on_start: "false" });
    });
  });
});
