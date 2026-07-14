import { beforeEach, describe, expect, test, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderApp } from "@/test/utils";

const svc = (id: number, name: string) => ({
  id, name, image_ref: `${name}:1`, current_digest: "sha256:a", state: "running",
  pinned: false, drifted: false, healthcheck: true, auto_update_enabled: null,
  check_status: "ok", last_checked: "2026-07-12T10:00:00Z",
});
const projects = [
  { id: 1, name: "media", kind: "compose", working_dir: "/srv/media", auto_update_enabled: false, unmanaged: false, services: [svc(10, "plex")] },
  { id: 2, name: "arr", kind: "compose", working_dir: "/srv/arr", auto_update_enabled: false, unmanaged: false, services: [svc(20, "radarr")] },
];

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {},
  }));
  server.use(http.get("/api/projects", () => HttpResponse.json(projects)));
});

describe("project route", () => {
  test("shows only the routed project's services", async () => {
    renderApp("/project/1");
    expect(await screen.findByRole("heading", { name: "media" })).toBeInTheDocument();
    expect(await screen.findByText("plex")).toBeInTheDocument();
    expect(screen.queryByText("radarr")).not.toBeInTheDocument();
  });

  test("hides the project filter (the sidebar is the project switcher)", async () => {
    renderApp("/project/1");
    expect(await screen.findByRole("heading", { name: "media" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Filter by project")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Filter by status")).toBeInTheDocument();
  });

  test("bulk actions are scoped to the project", async () => {
    renderApp("/project/1");
    expect(
      await screen.findByRole("button", { name: 'Check all services in "media"' }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /apply all available updates in media/i })).toBeInTheDocument();
  });

  test("an unknown project id renders a not-found message", async () => {
    renderApp("/project/99");
    expect(await screen.findByText(/project not found/i)).toBeInTheDocument();
  });

  test("a project with no services renders an empty state", async () => {
    server.use(http.get("/api/projects", () => HttpResponse.json([
      { id: 3, name: "empty", kind: "compose", working_dir: "/srv/empty", auto_update_enabled: false, unmanaged: false, services: [] },
    ])));
    renderApp("/project/3");
    expect(await screen.findByText(/no services in this project/i)).toBeInTheDocument();
  });
});
