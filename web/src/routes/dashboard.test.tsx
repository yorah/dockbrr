import { beforeEach, describe, expect, test, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderApp } from "@/test/utils";

const service = {
  id: 10, name: "plex", image_ref: "plex:1", current_digest: "sha256:a", state: "running",
  pinned: false, drifted: false, healthcheck: true, auto_update_enabled: null,
  check_status: "ok", last_checked: "2026-07-12T10:00:00Z",
};
const project = {
  id: 1, name: "media", kind: "compose", working_dir: "/srv/media",
  auto_update_enabled: false, unmanaged: false, services: [service],
};

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {},
  }));
});

describe("dashboard route", () => {
  test("action-row 'Add project' and empty-state 'Add your first project' are distinct triggers", async () => {
    // Default handlers answer /api/projects with [], zero projects, so the
    // empty state's CTA renders alongside the action row's own button.
    renderApp("/");
    const main = within(await screen.findByRole("main"));
    expect(await main.findByRole("button", { name: "Add project" })).toBeInTheDocument();
    expect(main.getByRole("button", { name: "Add your first project" })).toBeInTheDocument();
  });

  test("the action-row Add project button opens the AddProjectDialog", async () => {
    server.use(http.get("/api/projects", () => HttpResponse.json([project])));
    const user = userEvent.setup();
    renderApp("/");

    const main = within(await screen.findByRole("main"));
    // With a non-empty project list the empty-state CTA never renders, so
    // this is the only "Add project"-named button on the page.
    await user.click(await main.findByRole("button", { name: "Add project" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Add project" })).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Name")).toBeInTheDocument();
  });

  test("the empty-state 'Add your first project' CTA opens the AddProjectDialog", async () => {
    // Default handlers already return zero projects.
    const user = userEvent.setup();
    renderApp("/");

    const main = within(await screen.findByRole("main"));
    await user.click(await main.findByRole("button", { name: "Add your first project" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Add project" })).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Name")).toBeInTheDocument();
  });
});
