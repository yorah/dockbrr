import { beforeEach, describe, expect, test, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderApp } from "@/test/utils";
import { SIDEBAR_STORAGE_KEY } from "@/hooks/useSidebar";

const service = { id: 10, name: "plex", image_ref: "plex:1", current_digest: "sha256:a", state: "running", pinned: false, drifted: false, healthcheck: true, auto_update_enabled: null, check_status: "ok", last_checked: "2026-07-12T10:00:00Z" };
const project = { id: 1, name: "media", kind: "compose", working_dir: "/srv/media", auto_update_enabled: false, unmanaged: false, auto_named: false, services: [service] };

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {},
  }));
  server.use(http.get("/api/projects", () => HttpResponse.json([project])));
});

describe("Sidebar", () => {
  test("renders nav, projects, logout and the version", async () => {
    server.use(http.get("/api/status", () => HttpResponse.json({
      last_check_all: "", poll_interval_seconds: 300, docker_reachable: true, version: "1.4.2",
    })));
    renderApp("/");

    expect(await screen.findByRole("link", { name: "Dashboard" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Jobs" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /^media,/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /logout/i })).toBeInTheDocument();
    expect(await screen.findByText("v1.4.2")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /github repository/i })).toHaveAttribute(
      "href",
      "https://github.com/yorah/dockbrr",
    );
  });

  test("shows an update-count badge only when the project has open updates", async () => {
    server.use(http.get("/api/updates", () => HttpResponse.json([
      { id: 1, service_id: 10, from_digest: "sha256:a", to_digest: "sha256:b", from_version: "1.0.0", to_version: "1.1.0", tag: "1.1.0", severity: "minor", changelog_url: "", changelog_text: "", status: "available", detected_at: "2026-07-12T10:00:00Z" },
    ])));
    renderApp("/");
    const link = await screen.findByRole("link", { name: /^media, updates available/ });
    await waitFor(() => expect(link).toHaveTextContent("1"));
  });

  test("collapsing hides the labels and persists the state", async () => {
    const user = userEvent.setup();
    renderApp("/");
    expect(await screen.findByRole("link", { name: "Dashboard" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /collapse sidebar/i }));

    await waitFor(() => expect(screen.queryByText("Projects")).not.toBeInTheDocument());
    expect(localStorage.getItem(SIDEBAR_STORAGE_KEY)).toBe("collapsed");
    // The nav link survives as an icon with its accessible name intact.
    expect(screen.getByRole("link", { name: "Dashboard" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /expand sidebar/i })).toBeInTheDocument();
  });

  test("clicking a project navigates to its page", async () => {
    const user = userEvent.setup();
    renderApp("/");
    await user.click(await screen.findByRole("link", { name: /^media,/ }));
    expect(await screen.findByRole("heading", { name: "media" })).toBeInTheDocument();
  });

  test("with zero projects, the add-project trigger still renders", async () => {
    server.use(http.get("/api/projects", () => HttpResponse.json([])));
    renderApp("/");

    expect(await screen.findByRole("link", { name: "Dashboard" })).toBeInTheDocument();
    const sidebar = within(screen.getByRole("complementary"));
    expect(await sidebar.findByRole("button", { name: /add project/i })).toBeInTheDocument();
    expect(sidebar.queryByRole("link", { name: /^media,/ })).not.toBeInTheDocument();
  });

  test("groups auto-named projects under a collapsed Loose section", async () => {
    const loose = { id: 2, name: "adoring_saha", kind: "standalone", working_dir: "", auto_update_enabled: false, unmanaged: false, auto_named: true, services: [] };
    server.use(http.get("/api/projects", () => HttpResponse.json([project, loose])));
    renderApp("/");

    // Named project is a normal top-level link.
    expect(await screen.findByRole("link", { name: /^media,/ })).toBeInTheDocument();
    // Loose project is hidden until the group is expanded.
    expect(screen.queryByRole("link", { name: /^adoring_saha,/ })).not.toBeInTheDocument();

    const toggle = screen.getByRole("button", { name: /loose/i });
    expect(toggle).toHaveTextContent("Loose (1)");
    await userEvent.click(toggle);

    expect(await screen.findByRole("link", { name: /^adoring_saha,/ })).toBeInTheDocument();
  });
});
