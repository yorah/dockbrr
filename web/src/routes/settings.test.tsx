import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderApp } from "@/test/utils";
import { rowActiveClass, rowClass } from "@/components/layout/SidebarNav";

// rowClass and rowActiveClass share several structural tokens (flex, gap-3, ...);
// only the tokens unique to rowActiveClass actually distinguish "active" styling.
const distinguishingActiveClasses = rowActiveClass
  .split(" ")
  .filter((cls) => cls && !rowClass.split(" ").includes(cls));

// Every settings page fetches; answer each endpoint with a benign payload so the
// test asserts routing, not data.
const fetchMock = vi.fn(async (url: string) => {
  const u = String(url);
  const json = (body: unknown) =>
    new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
  if (u.includes("/api/setup/status")) return json({ needs_setup: false });
  if (u.includes("/api/auth/me")) return json({ username: "admin" });
  if (u.includes("/api/system/info")) {
    return json({
      version: "0.1.0-dev", commit: "", commit_dirty: false, build_date: "",
      go_version: "go1.26.4", platform: "linux/amd64",
      docker: { reachable: true }, db_path: "/data/dockbrr.db", bind_addr: ":3625", data_dir: "/data",
      auth: { username: "admin", method: "password" },
    });
  }
  if (u.includes("/api/settings")) {
    return json({
      poll_interval_seconds: "900", scan_on_start: "true", concurrency: "4",
      health_timeout_seconds: "60", health_poll_seconds: "3",
      write_back_compose: "false", auto_remove_gone: "false", gone_grace_seconds: "86400",
      job_retention_days: "30", github_token_set: false, restart_required: [], defaults: {},
    });
  }
  if (u.includes("/api/logs/config")) return json({ path: "/data/dockbrr.log", level: "info", maxSizeMB: 10, maxBackups: 3 });
  return json([]); // projects, updates, registries, log files
});

afterEach(() => {
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderSettings(path = "/settings") {
  vi.stubGlobal("fetch", fetchMock);
  return renderApp(path);
}

describe("settings routing", () => {
  it("redirects /settings to the Application page", async () => {
    renderSettings("/settings");
    expect(await screen.findByRole("heading", { name: "Build" })).toBeInTheDocument();
  });

  it("deep-links straight to a sub-page", async () => {
    renderSettings("/settings/scanning");
    expect(await screen.findByLabelText(/^poll interval/i)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Build" })).not.toBeInTheDocument();
  });

  it("navigates between sections from the sub-nav", async () => {
    const user = userEvent.setup();
    renderSettings("/settings/application");
    await screen.findByRole("heading", { name: "Build" });

    await user.click(screen.getByRole("link", { name: "Security" }));
    await waitFor(() => expect(screen.getByLabelText(/current password/i)).toBeInTheDocument());
  });

  it("renders all 7 settings sections in the sub-nav", async () => {
    renderSettings("/settings/application");
    await screen.findByRole("heading", { name: "Build" });

    const nav = screen.getByRole("navigation", { name: "Settings sections" });
    const links = within(nav).getAllByRole("link");
    expect(links).toHaveLength(7);
    expect(links.map((l) => l.textContent)).toEqual([
      "Application",
      "Scanning",
      "Updates",
      "Auto-update",
      "Registries",
      "Security",
      "Logs",
    ]);
  });

  it("marks the active section in the sub-nav and leaves the rest inactive", async () => {
    renderSettings("/settings/application");
    await screen.findByRole("heading", { name: "Build" });

    const nav = screen.getByRole("navigation", { name: "Settings sections" });
    const activeLink = within(nav).getByRole("link", { name: "Application" });
    const inactiveLink = within(nav).getByRole("link", { name: "Security" });

    expect(activeLink).toHaveAttribute("aria-current", "page");
    expect(activeLink).toHaveClass(...distinguishingActiveClasses);

    expect(inactiveLink).not.toHaveAttribute("aria-current");
    for (const cls of distinguishingActiveClasses) {
      expect(inactiveLink).not.toHaveClass(cls);
    }
  });

  it("does not offer an Add project section", async () => {
    renderSettings("/settings");
    await screen.findByRole("heading", { name: "Build" });
    expect(screen.queryByRole("link", { name: /add project/i })).not.toBeInTheDocument();
  });
});
