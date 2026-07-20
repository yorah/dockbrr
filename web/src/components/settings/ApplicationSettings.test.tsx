import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { keys } from "@/api/keys";
import { ApplicationSettings } from "@/components/settings/ApplicationSettings";
import type { SystemInfo, SelfUpdate } from "@/api/types";

const FULL: SystemInfo = {
  version: "0.1.0-dev",
  commit: "b563d4be1234",
  commit_dirty: false,
  build_date: "2026-06-29T21:48:15Z",
  go_version: "go1.26.4",
  platform: "linux/amd64",
  started_at: "2026-07-12T12:00:00Z",
  docker: { reachable: true, version: "27.3.1", api_version: "1.47" },
  db_path: "/config/dockbrr.db",
  bind_addr: "0.0.0.0:3625",
  data_dir: "/config",
  auth: { username: "admin", method: "password" },
};

const NO_UPDATE: SelfUpdate = { current: "0.1.0-dev", update_available: false, checked_at: "2026-07-13T14:30:00Z" };

function renderPage(info: SystemInfo, self: SelfUpdate = NO_UPDATE) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
      const body = url.includes("/api/updates/self") ? self : info;
      return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
    }),
  );
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <ApplicationSettings />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  // Freeze the clock so the uptime assertion is exact. shouldAdvanceTime lets
  // real elapsed ms tick the fake clock forward automatically, since this repo's
  // testing-library can't detect vitest fake timers (it only recognizes a
  // global `jest`), so without this the mocked setTimeout/setInterval starve
  // React's scheduler (jsdom has no MessageChannel to fall back on) and
  // findByText/waitFor hang forever. The drift is sub-second, well under the
  // minute resolution the uptime string renders at.
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(new Date("2026-07-13T14:30:00Z"));
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("ApplicationSettings", () => {
  it("renders build, runtime, docker, storage and auth facts", async () => {
    renderPage(FULL);
    expect(await screen.findByText("0.1.0-dev")).toBeInTheDocument();
    expect(screen.getByText("b563d4b")).toBeInTheDocument(); // short commit
    expect(screen.getByText("go1.26.4")).toBeInTheDocument();
    expect(screen.getByText("linux/amd64")).toBeInTheDocument();
    expect(screen.getByText("/config/dockbrr.db")).toBeInTheDocument();
    expect(screen.getByText("0.0.0.0:3625")).toBeInTheDocument();
    expect(screen.getByText("27.3.1")).toBeInTheDocument();
    expect(screen.getByText("admin")).toBeInTheDocument();
  });

  it("derives uptime from started_at", async () => {
    renderPage(FULL); // started 2026-07-12T12:00:00Z, now 2026-07-13T14:30:00Z
    expect(await screen.findByText("1d 2h 30m")).toBeInTheDocument();
  });

  it("shows a dash when build stamps are absent", async () => {
    renderPage({ ...FULL, commit: "", build_date: "", started_at: undefined });
    // COMMIT, BUILD DATE and UPTIME all degrade to "-".
    expect(await screen.findAllByText("-")).toHaveLength(3);
  });

  it("shows 'up to date' status under the version", async () => {
    renderPage(FULL, { current: "0.1.0-dev", update_available: false, checked_at: "2026-07-13T14:30:00Z" });
    expect(await screen.findByText(/Up to date/i)).toBeInTheDocument();
  });

  it("shows an available version when an update exists", async () => {
    renderPage(FULL, { current: "0.1.0-dev", latest: "0.2.0", update_available: true, checked_at: "2026-07-13T14:30:00Z" });
    expect(await screen.findByText(/0\.2\.0 available/i)).toBeInTheDocument();
  });

  it("checks for updates with force=true on click", async () => {
    renderPage(FULL);
    const btn = await screen.findByRole("button", { name: /check for updates/i });
    fireEvent.click(btn);
    await waitFor(() =>
      expect(vi.mocked(fetch)).toHaveBeenCalledWith(
        expect.stringContaining("/api/updates/self?force=true"),
        expect.anything(),
      ),
    );
  });

  it("reports an unreachable daemon without a version row", async () => {
    renderPage({ ...FULL, docker: { reachable: false } });
    expect(await screen.findByText("Unreachable")).toBeInTheDocument();
    expect(screen.queryByText("27.3.1")).not.toBeInTheDocument();
  });

  it("invalidates settings, registries, and log config after a successful import", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (init?.method === "POST" && url === "/api/settings/import") {
          return new Response(null, { status: 204 });
        }
        return new Response(JSON.stringify(FULL), { status: 200, headers: { "content-type": "application/json" } });
      }),
    );
    const client = makeQueryClient();
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    const { container } = render(
      <QueryClientProvider client={client}>
        <ApplicationSettings />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("0.1.0-dev")).toBeInTheDocument();

    const input = container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = new File([JSON.stringify({ log_level: "debug" })], "settings.json", {
      type: "application/json",
    });
    Object.defineProperty(input, "files", { value: [file] });
    fireEvent.change(input);

    await waitFor(() => {
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: keys.settings });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: keys.registries });
      // The imported log_level is validated + applied server-side; the Logs
      // page reads this query, so it must be refreshed too or it shows stale.
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: keys.logConfig });
    });
  });
});
