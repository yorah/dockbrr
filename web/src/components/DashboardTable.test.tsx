import { beforeEach, expect, test, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { render, screen, waitFor, within } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { delay, http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { makeQueryClient } from "@/api/queryClient";
import { router } from "@/router";
import { __resetBusyServices } from "@/hooks/useBusyServices";

// The busy store is module-level state; without this, a busy id marked in one
// test leaks into the next and spuriously disables its buttons.
beforeEach(() => __resetBusyServices());

function renderDashboardWithRouter() {
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

// Top-level projects load collapsed on the dashboard now. Waits for the project
// header button to appear (data loaded), then clicks it to reveal the services.
// The accessible name is the bare project name - distinct from "Apply all
// updates in <name>" and "Check all services in <name>".
async function expandProject(name: string) {
  await userEvent.click(await screen.findByRole("button", { name }));
}

test("lists services and toggles a project group", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.27",
              current_digest: "sha256:a",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  // Collapsed by default: the header is present, the service is not.
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
  // Expand -> service visible.
  await userEvent.click(screen.getByRole("button", { name: "app" }));
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  // Collapse again -> service hidden.
  await userEvent.click(screen.getByRole("button", { name: "app" }));
  await waitFor(() => expect(screen.queryByText("web")).not.toBeInTheDocument());
});

test("service name links to the service detail page", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.27",
              current_digest: "sha256:a",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  const link = screen.getByRole("link", { name: "web" });
  expect(link).toHaveAttribute("href", "/service/10");
});

test("shows last-checked time and a warning for rate-limited services", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.27",
              current_digest: "sha256:a",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
              check_status: "rate_limited",
              last_checked: new Date(Date.now() - 90_000).toISOString(),
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  expect(screen.getByText(/2m ago/i)).toBeInTheDocument();
  expect(screen.getByLabelText(/rate.?limited/i)).toBeInTheDocument();
});

test("a pinned service's digest-pinned image ref renders short, not the full sha256 string", async () => {
  const refDigest = "sha256:" + "a".repeat(64);
  const currentDigest = "sha256:" + "b".repeat(64);
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: `nginx@${refDigest}`,
              current_digest: currentDigest,
              state: "running",
              pinned: true,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  expect(screen.queryByText(`nginx@${refDigest}`)).not.toBeInTheDocument();
  expect(screen.queryByText(refDigest)).not.toBeInTheDocument();
  expect(screen.getByText("sha256:" + "a".repeat(12))).toBeInTheDocument();
});

test("shows an Unmanaged badge on the project header when the project is flagged unmanaged", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          unmanaged: true,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.27",
              current_digest: "sha256:a",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  // The header button's accessible name concatenates the project name and the
  // "Unmanaged" badge text with no separator ("appUnmanaged"), so
  // expandProject's exact-name match won't find it here. Case-sensitive
  // prefix match: an /i regex would also catch "Apply all updates in app"
  // (starts with "App").
  await userEvent.click(await screen.findByRole("button", { name: /^app/ }));
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  expect(screen.getByText("Unmanaged")).toBeInTheDocument();
});

test("Apply all enqueues one service-scope apply per pending update, never a project-scope job", async () => {
  const applied: Array<{ id: string; scope: string }> = [];
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.30",
              current_digest: "sha256:a",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
            {
              id: 11,
              name: "cache",
              image_ref: "redis:8.8",
              current_digest: "sha256:c",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        { id: 100, service_id: 10, status: "available", tag: "1.31.2", to_digest: "sha256:a2", from_digest: "sha256:a", severity: "minor" },
        { id: 101, service_id: 11, status: "available", tag: "8.9", to_digest: "sha256:c2", from_digest: "sha256:c", severity: "minor" },
      ]),
    ),
    http.post("/api/updates/:id/apply", async ({ request, params }) => {
      const body = (await request.json()) as { scope: string };
      applied.push({ id: String(params.id), scope: body.scope });
      return HttpResponse.json({ job_id: Number(params.id) });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    renderDashboardWithRouter();
    await expandProject("app");
    await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: /apply all updates in app/i }));
    await waitFor(() => expect(applied).toHaveLength(2));
    // Both pending updates applied at SERVICE scope: a project-scope `up`
    // would revert the already-applied redis sibling.
    expect(applied.every((a) => a.scope === "service")).toBe(true);
    expect(new Set(applied.map((a) => a.id))).toEqual(new Set(["100", "101"]));
  } finally {
    confirmSpy.mockRestore();
  }
});

test("Apply all excludes a gone service's pending update, even with Show removed on", async () => {
  const applied: Array<{ id: string; scope: string }> = [];
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            { id: 10, name: "web", image_ref: "nginx:1.30", current_digest: "sha256:a", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null },
            { id: 11, name: "cache", image_ref: "redis:8.8", current_digest: "sha256:c", state: "gone", pinned: false, healthcheck: false, auto_update_enabled: null },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        { id: 100, service_id: 10, status: "available", tag: "1.31.2", to_digest: "sha256:a2", from_digest: "sha256:a", severity: "minor" },
        { id: 101, service_id: 11, status: "available", tag: "8.9", to_digest: "sha256:c2", from_digest: "sha256:c", severity: "minor" },
      ]),
    ),
    http.post("/api/updates/:id/apply", async ({ request, params }) => {
      const body = (await request.json()) as { scope: string };
      applied.push({ id: String(params.id), scope: body.scope });
      return HttpResponse.json({ job_id: Number(params.id) });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    renderDashboardWithRouter();
    await expandProject("app");
    // Toggle "Show removed" so the gone row (and its otherwise-hidden Apply
    // button) is actually on screen. The guard must hold even then.
    await userEvent.click(await screen.findByRole("switch", { name: /show removed/i }));
    await waitFor(() => expect(screen.getByText("cache")).toBeInTheDocument());

    const goneRow = screen.getByText("cache").closest("tr")!;
    expect(within(goneRow).getByRole("button", { name: /apply update to cache/i })).toBeDisabled();

    await userEvent.click(screen.getByRole("button", { name: /apply all updates in app/i }));
    await waitFor(() => expect(applied).toHaveLength(1));
    expect(applied).toEqual([{ id: "100", scope: "service" }]);
  } finally {
    confirmSpy.mockRestore();
  }
});

function twoServiceProject() {
  return http.get("/api/projects", () =>
    HttpResponse.json([
      {
        id: 1,
        name: "app",
        kind: "compose",
        working_dir: "/srv",
        auto_update_enabled: false,
        services: [
          { id: 10, name: "web", image_ref: "nginx:1.30", current_digest: "sha256:a", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null },
          { id: 11, name: "cache", image_ref: "redis:8.8", current_digest: "sha256:c", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null },
        ],
      },
    ]),
  );
}

test("project Check all fans out a check for every service in the project", async () => {
  const checked: string[] = [];
  server.use(
    twoServiceProject(),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/services/:id/check", ({ params }) => {
      checked.push(String(params.id));
      return HttpResponse.json({ ok: true });
    }),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  await userEvent.click(screen.getByRole("button", { name: /check all services in app/i }));
  await waitFor(() => expect(new Set(checked)).toEqual(new Set(["10", "11"])));
});

test("global Check all runs a full scan; global Apply all applies each available update at service scope", async () => {
  let scanCalls = 0;
  const applied: Array<{ id: string; scope: string }> = [];
  server.use(
    twoServiceProject(),
    http.get("/api/updates", () =>
      HttpResponse.json([
        { id: 100, service_id: 10, status: "available", tag: "1.31.2", to_digest: "sha256:a2", from_digest: "sha256:a", severity: "minor" },
      ]),
    ),
    // Global "Check all" runs a single server-side sweep (POST /api/scan),
    // unlike the per-project button, which fans out one check per service.
    http.post("/api/scan", () => {
      scanCalls += 1;
      return HttpResponse.json({ status: "checked" });
    }),
    http.post("/api/updates/:id/apply", async ({ request, params }) => {
      const body = (await request.json()) as { scope: string };
      applied.push({ id: String(params.id), scope: body.scope });
      return HttpResponse.json({ job_id: Number(params.id) });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    renderDashboardWithRouter();
    await expandProject("app");
    await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: /^check all services$/i }));
    await waitFor(() => expect(scanCalls).toBe(1));
    await userEvent.click(screen.getByRole("button", { name: /apply all available updates/i }));
    await waitFor(() => expect(applied).toHaveLength(1));
    expect(applied[0]).toEqual({ id: "100", scope: "service" });
  } finally {
    confirmSpy.mockRestore();
  }
});

test("project row auto-update switch reflects the flag, toggles it, and does not collapse the group", async () => {
  const puts: Array<{ id: string; enabled: boolean }> = [];
  server.use(
    twoServiceProject(),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.put("/api/projects/:id/auto-update", async ({ request, params }) => {
      const body = (await request.json()) as { enabled: boolean };
      puts.push({ id: String(params.id), enabled: body.enabled });
      return HttpResponse.json({ ok: true });
    }),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());

  const auto = screen.getByRole("switch", { name: /auto-update app/i });
  expect(auto).toHaveAttribute("data-state", "unchecked"); // project.auto_update_enabled = false

  await userEvent.click(auto);
  await waitFor(() => expect(puts).toEqual([{ id: "1", enabled: true }]));
  // The project header row collapses on click, so the switch must stop propagation.
  expect(screen.getByText("web")).toBeInTheDocument();
});

test("changelog column falls back to the last applied update once nothing is pending", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.28",
              current_digest: "sha256:c",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.get("/api/updates/last-applied", () =>
      HttpResponse.json([
        {
          id: 42,
          service_id: 10,
          from_digest: "sha256:b",
          to_digest: "sha256:c",
          from_version: "1.27",
          to_version: "1.28",
          tag: "1.28",
          severity: "minor",
          changelog_url: "https://example.test/rel/1.28",
          changelog_text: "## What's new\n\n- faster",
          status: "applied",
          detected_at: "2026-07-01T00:00:00Z",
        },
      ]),
    ),
  );
  renderDashboardWithRouter();
  await expandProject("app");

  const button = await screen.findByRole("button", { name: /last applied changelog for web/i });
  await userEvent.click(button);

  // The read-only drawer opens with the cached markdown; no Apply control.
  await waitFor(() => expect(screen.getByText("What's new")).toBeInTheDocument());
  expect(screen.queryByRole("button", { name: /^apply$/i })).not.toBeInTheDocument();
});

test("changelog eye reads 'Current version' for an up-to-date service's current row", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "ghcr.io/acme/web:1.2.3",
              current_digest: "sha256:cur",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.get("/api/updates/last-applied", () =>
      HttpResponse.json([
        {
          id: 77,
          service_id: 10,
          from_digest: "sha256:cur",
          to_digest: "sha256:cur",
          from_version: "1.2.3",
          to_version: "1.2.3",
          tag: "1.2.3",
          severity: "current",
          changelog_url: "https://github.com/acme/web/releases/tag/1.2.3",
          changelog_text: "## 1.2.3\n\n- shipped",
          status: "current",
          detected_at: "2026-07-01T00:00:00Z",
        },
      ]),
    ),
  );
  renderDashboardWithRouter();
  await expandProject("app");

  const button = await screen.findByRole("button", {
    name: /current version changelog for web/i,
  });
  await userEvent.click(button);
  await waitFor(() => expect(screen.getByText("1.2.3")).toBeInTheDocument());
  expect(screen.queryByRole("button", { name: /^apply$/i })).not.toBeInTheDocument();
});

test("changelog column falls back to the last applied changelog when the PENDING update has no changelog of its own", async () => {
  // Reproduces the real bug: a service was applied at 1.28 with a cached
  // changelog, then drifted to 1.29 but the new update's changelog could not
  // be resolved (non-GitHub image, no token, rate limit): changelog_text and
  // changelog_url both empty. Resolving "changelog ~ pending update presence"
  // would blank the cell; resolving by content must still show the history.
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.29",
              current_digest: "sha256:c",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        {
          id: 200,
          service_id: 10,
          status: "available",
          tag: "1.29",
          to_digest: "sha256:d",
          from_digest: "sha256:c",
          severity: "minor",
          changelog_url: "",
          changelog_text: "",
        },
      ]),
    ),
    http.get("/api/updates/last-applied", () =>
      HttpResponse.json([
        {
          id: 42,
          service_id: 10,
          from_digest: "sha256:b",
          to_digest: "sha256:c",
          from_version: "1.27",
          to_version: "1.28",
          tag: "1.28",
          severity: "minor",
          changelog_url: "https://example.test/rel/1.28",
          changelog_text: "## What's new\n\n- faster",
          status: "applied",
          detected_at: "2026-07-01T00:00:00Z",
        },
      ]),
    ),
  );
  renderDashboardWithRouter();
  await expandProject("app");

  const button = await screen.findByRole("button", { name: /last applied changelog for web/i });
  await userEvent.click(button);

  await waitFor(() => expect(screen.getByText("What's new")).toBeInTheDocument());
});

test("changelog column shows the pending changelog (non-muted) when the pending update has its own changelog", async () => {
  // Coverage gap the review flagged: with BOTH a pending update carrying a
  // changelog and a last-applied one, the pending path must still win.
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.29",
              current_digest: "sha256:c",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        {
          id: 200,
          service_id: 10,
          status: "available",
          tag: "1.29",
          to_digest: "sha256:d",
          from_digest: "sha256:c",
          severity: "minor",
          changelog_url: "https://example.test/rel/1.29",
          changelog_text: "## Pending notes\n\n- new stuff",
        },
      ]),
    ),
    http.get("/api/updates/last-applied", () =>
      HttpResponse.json([
        {
          id: 42,
          service_id: 10,
          from_digest: "sha256:b",
          to_digest: "sha256:c",
          from_version: "1.27",
          to_version: "1.28",
          tag: "1.28",
          severity: "minor",
          changelog_url: "https://example.test/rel/1.28",
          changelog_text: "## What's new\n\n- faster",
          status: "applied",
          detected_at: "2026-07-01T00:00:00Z",
        },
      ]),
    ),
  );
  renderDashboardWithRouter();
  await expandProject("app");

  const button = await screen.findByRole("button", { name: /^changelog for web$/i });
  expect(screen.queryByRole("button", { name: /last applied changelog for web/i })).not.toBeInTheDocument();
  await userEvent.click(button);

  await waitFor(() => expect(screen.getByText("Pending notes")).toBeInTheDocument());
});

test("changelog eye is enabled for a rate-limited pending update with no changelog content", async () => {
  // Scan sets changelog_status to "rate_limited" but leaves changelog_text and
  // changelog_url empty. The eye must still open so ChangelogDrawer can render
  // the "add a token" hint, rather than being disabled like a plain no-content miss.
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.29",
              current_digest: "sha256:c",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        {
          id: 200,
          service_id: 10,
          status: "available",
          tag: "1.29",
          to_digest: "sha256:d",
          from_digest: "sha256:c",
          severity: "minor",
          changelog_url: "",
          changelog_text: "",
          changelog_status: "rate_limited",
        },
      ]),
    ),
    http.get("/api/updates/last-applied", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");

  const button = await screen.findByRole("button", { name: /^changelog for web$/i });
  expect(button).not.toBeDisabled();

  await userEvent.click(button);

  await waitFor(() => expect(screen.getByText(/github rate limit reached/i)).toBeInTheDocument());
});

test("changelog eye stays disabled for a pending update with no changelog and no rate limit", async () => {
  // Guards the non-rate-limited miss case: empty changelog_text/url and no
  // changelog_status must still leave the eye disabled (no drawer to open).
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.29",
              current_digest: "sha256:c",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        {
          id: 200,
          service_id: 10,
          status: "available",
          tag: "1.29",
          to_digest: "sha256:d",
          from_digest: "sha256:c",
          severity: "minor",
          changelog_url: "",
          changelog_text: "",
        },
      ]),
    ),
    http.get("/api/updates/last-applied", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");

  const button = await screen.findByRole("button", { name: /^changelog for web$/i });
  expect(button).toBeDisabled();
});

test("action menu is state-aware and wires Stop to the lifecycle endpoint", async () => {
  const calls: Array<{ id: string; action: string }> = [];
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.27",
              current_digest: "sha256:a",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
            {
              id: 11,
              name: "cache",
              image_ref: "redis:8.8",
              current_digest: "sha256:c",
              state: "exited",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/services/:id/lifecycle", async ({ request, params }) => {
      const body = (await request.json()) as { action: string };
      calls.push({ id: String(params.id), action: body.action });
      return HttpResponse.json({ job_id: 555 });
    }),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());

  const webRow = screen.getByText("web").closest("tr")!;
  const cacheRow = screen.getByText("cache").closest("tr")!;

  // Running row: Stop + Restart, no Start.
  expect(within(webRow).getByRole("button", { name: /^stop web$/i })).toBeInTheDocument();
  expect(within(webRow).getByRole("button", { name: /^restart web$/i })).toBeInTheDocument();
  expect(within(webRow).queryByRole("button", { name: /^start web$/i })).not.toBeInTheDocument();
  expect(within(webRow).getByRole("button", { name: /^logs for web$/i })).toBeInTheDocument();

  // Stopped row: Start, no Stop/Restart.
  expect(within(cacheRow).getByRole("button", { name: /^start cache$/i })).toBeInTheDocument();
  expect(within(cacheRow).queryByRole("button", { name: /^stop cache$/i })).not.toBeInTheDocument();
  expect(within(cacheRow).queryByRole("button", { name: /^restart cache$/i })).not.toBeInTheDocument();
  expect(within(cacheRow).getByRole("button", { name: /^logs for cache$/i })).toBeInTheDocument();

  await userEvent.click(within(webRow).getByRole("button", { name: /^stop web$/i }));
  await waitFor(() => expect(calls).toEqual([{ id: "10", action: "stop" }]));
});

test("a gone service's row offers only Logs, no lifecycle buttons", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.27",
              current_digest: "sha256:a",
              state: "gone",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  // "gone" rows (and their project header) are hidden entirely by default;
  // toggle "Show removed" first so the project row exists to expand.
  await userEvent.click(await screen.findByRole("switch", { name: /show removed/i }));
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());

  const main = within(screen.getByRole("main"));
  expect(main.getByRole("button", { name: /^logs for web$/i })).toBeInTheDocument();
  expect(main.queryByRole("button", { name: /^start web$/i })).not.toBeInTheDocument();
  expect(main.queryByRole("button", { name: /^stop web$/i })).not.toBeInTheDocument();
  expect(main.queryByRole("button", { name: /^restart web$/i })).not.toBeInTheDocument();
});

test("hides auto-named projects behind a collapsed Loose group on the dashboard", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 2, name: "adoring_saha", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 20, name: "adoring_saha", image_ref: "busybox:latest", current_digest: "sha256:b", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");

  // Named project's service is visible; the loose service is hidden by default.
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  expect(screen.queryByText("busybox:latest")).not.toBeInTheDocument();

  // The Loose group header shows the count; expanding reveals the loose service.
  // Scoped to <main>: the sidebar (Task 5) also renders its own "Loose (1)" toggle.
  const toggle = within(screen.getByRole("main")).getByRole("button", { name: /loose \(1\)/i });
  await userEvent.click(toggle);
  await waitFor(() => expect(screen.getByText("busybox:latest")).toBeInTheDocument());
});

test("Loose header 'Remove stopped containers' removes every stopped loose container on confirm, skipping running ones", async () => {
  const removed: string[] = [];
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 2, name: "adoring_saha", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 20, name: "adoring_saha", image_ref: "busybox:latest", current_digest: "sha256:b", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 3, name: "sleepy_lamarr", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 21, name: "sleepy_lamarr", image_ref: "redis:8.8", current_digest: "sha256:c", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 4, name: "brave_turing", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 22, name: "brave_turing", image_ref: "alpine:3.20", current_digest: "sha256:d", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/services/:id/remove", ({ params }) => {
      removed.push(String(params.id));
      return HttpResponse.json({ job_id: 999 });
    }),
  );
  renderDashboardWithRouter();

  await waitFor(() => expect(screen.getByRole("main")).toBeInTheDocument());
  const main = within(screen.getByRole("main"));
  const bulk = await waitFor(() => main.getByRole("button", { name: /remove stopped containers/i }));
  expect(bulk).not.toBeDisabled(); // two stopped loose containers exist

  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    await userEvent.click(bulk);
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    const message = confirmSpy.mock.calls[0][0] as string;
    expect(message).toContain("adoring_saha");
    expect(message).toContain("sleepy_lamarr");
    expect(message).not.toContain("brave_turing"); // running loose is skipped

    await waitFor(() => expect(new Set(removed)).toEqual(new Set(["20", "21"])));
  } finally {
    confirmSpy.mockRestore();
  }
});

test("Loose header 'Remove stopped containers' is disabled when every loose container is running", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 4, name: "brave_turing", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 22, name: "brave_turing", image_ref: "alpine:3.20", current_digest: "sha256:d", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await waitFor(() => expect(screen.getByRole("main")).toBeInTheDocument());
  const main = within(screen.getByRole("main"));
  const bulk = await waitFor(() => main.getByRole("button", { name: /remove stopped containers/i }));
  expect(bulk).toBeDisabled();
});

test("Loose header 'Remove stopped containers' disables while a removal is in flight", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 2, name: "adoring_saha", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 20, name: "adoring_saha", image_ref: "busybox:latest", current_digest: "sha256:b", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    // Never resolves: the mutation stays pending so isPending holds true.
    http.post("/api/services/:id/remove", async () => {
      await delay("infinite");
      return HttpResponse.json({ job_id: 1 });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    renderDashboardWithRouter();
    await waitFor(() => expect(screen.getByRole("main")).toBeInTheDocument());
    const main = within(screen.getByRole("main"));
    const bulk = await waitFor(() => main.getByRole("button", { name: /remove stopped containers/i }));
    expect(bulk).not.toBeDisabled();
    await userEvent.click(bulk);
    // Second click must not re-enqueue: the button is disabled while pending.
    await waitFor(() => expect(bulk).toBeDisabled());
  } finally {
    confirmSpy.mockRestore();
  }
});

test("offers a per-row Remove button for a stopped standalone container and removes it on confirm", async () => {
  const removed: string[] = [];
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 5, name: "my-standalone", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 30, name: "grafana", image_ref: "grafana:11", current_digest: "sha256:g", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/services/:id/remove", ({ params }) => {
      removed.push(String(params.id));
      return HttpResponse.json({ job_id: 999 });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    renderDashboardWithRouter();
    await expandProject("my-standalone");
    await waitFor(() => expect(screen.getByText("grafana")).toBeInTheDocument());
    const row = screen.getByText("grafana").closest("tr")!;
    await userEvent.click(within(row).getByRole("button", { name: /^remove grafana$/i }));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(removed).toEqual(["30"]));
  } finally {
    confirmSpy.mockRestore();
  }
});

test("project row shows the health indicator: amber update-count badge, green dot when clean", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 2, name: "db", kind: "compose", working_dir: "/srv/db",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 20, name: "postgres", image_ref: "postgres:16", current_digest: "sha256:c", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        { id: 100, service_id: 10, from_digest: "sha256:a", to_digest: "sha256:b", from_version: "1.27", to_version: "1.28", tag: "1.28", severity: "minor", changelog_url: "", changelog_text: "", status: "available", detected_at: "2026-07-16T00:00:00Z" },
      ]),
    ),
  );
  renderDashboardWithRouter();
  await waitFor(() => expect(screen.getByRole("main")).toBeInTheDocument());
  const main = within(screen.getByRole("main"));
  const appHeader = within(await waitFor(() => main.getByText("app").closest("tr") as HTMLElement));
  const dbHeader = within(main.getByText("db").closest("tr") as HTMLElement);
  // Project with an open update: count badge + "updates available" dot.
  expect(appHeader.getByText("1")).toBeInTheDocument();
  expect(appHeader.getByRole("img", { name: /update.*updates available/i })).toBeInTheDocument();
  // Clean project: no badge, healthy dot.
  expect(dbHeader.queryByText("1")).not.toBeInTheDocument();
  expect(dbHeader.getByRole("img", { name: /^healthy$/i })).toBeInTheDocument();
});

test("surfaces reverse-resolved versions for a floating tag, not just ':latest'", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 10, name: "backrest", image_ref: "garethgeorge/backrest:latest", current_digest: "sha256:9c9966b5c285", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () =>
      HttpResponse.json([
        {
          id: 100, service_id: 10, from_digest: "sha256:9c9966b5c285", to_digest: "sha256:b85297975428",
          from_version: "v1.13.0", to_version: "v1.14.1", tag: "latest", severity: "minor",
          changelog_url: "", changelog_text: "", status: "available", detected_at: "2026-07-16T00:00:00Z",
        },
      ]),
    ),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByRole("main")).toBeInTheDocument());
  const main = within(screen.getByRole("main"));
  // Running version (from_version) shows under the floating ref.
  await waitFor(() => expect(main.getByText("v1.13.0")).toBeInTheDocument());
  // Target version leads the Latest column, tag kept as a secondary hint.
  expect(main.getByText("v1.14.1")).toBeInTheDocument();
  expect(main.getByText("(latest)")).toBeInTheDocument();
});

test("per-row Remove button disables while its removal is in flight, so it can't re-enqueue", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 5, name: "my-standalone", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 30, name: "grafana", image_ref: "grafana:11", current_digest: "sha256:g", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    // Never resolves: the enqueue stays pending, but the button must stay
    // disabled off the local removing marker regardless of enqueue timing.
    http.post("/api/services/:id/remove", async () => {
      await delay("infinite");
      return HttpResponse.json({ job_id: 1 });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    renderDashboardWithRouter();
    await expandProject("my-standalone");
    await waitFor(() => expect(screen.getByText("grafana")).toBeInTheDocument());
    // Re-query each time: TanStack re-renders the cell, replacing the DOM node.
    const btn = () => screen.getByRole("button", { name: /^remove grafana$/i });
    expect(btn()).not.toBeDisabled();
    await userEvent.click(btn());
    await waitFor(() => expect(btn()).toBeDisabled());
  } finally {
    confirmSpy.mockRestore();
  }
});

test("shows the Compose button only on compose project headers, not standalone ones", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 5, name: "my-standalone", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 30, name: "grafana", image_ref: "grafana:11", current_digest: "sha256:g", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  // Scope to <main>: the sidebar project list also renders "app"/"my-standalone".
  const main = within(screen.getByRole("main"));
  const composeHeader = main.getByText("app").closest("tr")!;
  const standaloneHeader = main.getByText("my-standalone").closest("tr")!;
  expect(within(composeHeader).getByRole("button", { name: /compose/i })).toBeInTheDocument();
  expect(within(standaloneHeader).queryByRole("button", { name: /compose/i })).not.toBeInTheDocument();
});

test("no per-row Remove button for a running standalone or a stopped compose service", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 5, name: "run-standalone", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 30, name: "grafana", image_ref: "grafana:11", current_digest: "sha256:g", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("run-standalone");
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("grafana")).toBeInTheDocument());
  const runRow = screen.getByText("grafana").closest("tr")!;
  const composeRow = screen.getByText("web").closest("tr")!;
  expect(within(runRow).queryByRole("button", { name: /^remove grafana$/i })).not.toBeInTheDocument();
  expect(within(composeRow).queryByRole("button", { name: /^remove web$/i })).not.toBeInTheDocument();
});

test("dashboard loads every top-level project collapsed", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, auto_named: false,
          services: [{
            id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
            state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
          }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  // Header present (data loaded) but the service row is hidden.
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
});

test("an active search reveals a service under an otherwise-collapsed project", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, auto_named: false,
          services: [{
            id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
            state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
          }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
  // Filter on -> service visible without expanding the header.
  await userEvent.type(screen.getByLabelText("Search"), "web");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
});

test("a manually-expanded project stays expanded across a refetch", async () => {
  // The projects payload must actually CHANGE between the first fetch and the
  // refetch triggered below. If it comes back byte-identical, React Query's
  // default structural sharing (see web/src/api/queryClient.ts) returns the
  // SAME `projects.data` reference, `rows` (memoized on `[projects.data, ...]`
  // in useDashboardRows) keeps a stable identity, and the seed effect (dep
  // `[rows, defaultCollapsed]`) never re-runs - the test would pass even if the
  // seenProjects guard were deleted. A second top-level project ("db") is added
  // on the refetch so the effect has something new to seed, which lets this
  // test assert BOTH invariants: the guard holds for the already-expanded
  // project, and the effect genuinely re-ran and collapsed the newcomer.
  let fetchCount = 0;
  server.use(
    http.get("/api/projects", () => {
      fetchCount += 1;
      const app = {
        id: 1, name: "app", kind: "compose", working_dir: "/srv",
        auto_update_enabled: false, auto_named: false,
        services: [{
          id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
          state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
        }],
      };
      if (fetchCount === 1) return HttpResponse.json([app]);
      const db = {
        id: 2, name: "db", kind: "compose", working_dir: "/srv/db",
        auto_update_enabled: false, auto_named: false,
        services: [{
          id: 20, name: "postgres", image_ref: "postgres:16", current_digest: "sha256:c",
          state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
        }],
      };
      return HttpResponse.json([app, db]);
    }),
    http.get("/api/updates", () => HttpResponse.json([])),
    // Backs the global "Check all services" button: its mutation invalidates
    // the projects query on success (see useScanAll), the same mechanism the
    // "global Check all" test above uses to prove a scan ran.
    http.post("/api/scan", () => HttpResponse.json({ status: "checked" })),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  // Force a projects refetch (mirror the mechanism used by the "Check all"
  // tests): the global button is named exactly "Check all services", distinct
  // from the per-project "Check all services in app" button.
  await userEvent.click(screen.getByRole("button", { name: /^check all services$/i }));
  // The new project's header must appear before asserting on its collapsed
  // state, otherwise a false negative could just mean the refetch hasn't
  // landed yet.
  await waitFor(() => expect(screen.getByRole("button", { name: "db" })).toBeInTheDocument());
  // Invariant 1: the seed effect must NOT re-collapse a project the user
  // manually expanded (the seenProjects guard holds).
  expect(screen.getByText("web")).toBeInTheDocument();
  // Invariant 2: the newly-appeared top-level project IS seeded collapsed,
  // proving the seed effect actually re-ran rather than the test passing
  // vacuously because nothing re-triggered it.
  expect(screen.queryByText("postgres")).not.toBeInTheDocument();
});

test("a header click while a filter is active does not change the post-filter collapse state", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, auto_named: false,
          services: [{
            id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
            state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
          }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
  // Filter on: the service is force-shown regardless of collapse state.
  const search = screen.getByLabelText("Search");
  await userEvent.type(search, "web");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  // Click the header while filtering. This must be a no-op: without the guard
  // it would silently drop "app" from `collapsed`, changing the state the user
  // returns to once the filter clears.
  await userEvent.click(screen.getByRole("button", { name: "app" }));
  // Clear the filter: the project must return to its pre-filter collapsed state,
  // so the service is hidden again. (Fails if the header click mutated collapsed.)
  await userEvent.clear(search);
  await waitFor(() => expect(screen.queryByText("web")).not.toBeInTheDocument());
});

test("a project that disappears and reappears re-collapses at the default", async () => {
  // A second project ("keep") is always present so the dashboard never empties
  // and the global "Check all services" button stays available to drive
  // refetches. "app" is absent from the SECOND fetch (deleted) and back on the
  // third. Without pruning `seenProjects`, the reappeared "app" would stay in
  // the seen set and resurface EXPANDED (the user had expanded it); the prune
  // makes it fresh again so it re-collapses at the default.
  let fetchCount = 0;
  const app = {
    id: 1, name: "app", kind: "compose", working_dir: "/srv",
    auto_update_enabled: false, auto_named: false,
    services: [{
      id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
      state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
    }],
  };
  const keep = {
    id: 2, name: "keep", kind: "compose", working_dir: "/srv/keep",
    auto_update_enabled: false, auto_named: false,
    services: [{
      id: 20, name: "keepsvc", image_ref: "redis:7", current_digest: "sha256:d",
      state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
    }],
  };
  server.use(
    http.get("/api/projects", () => {
      fetchCount += 1;
      if (fetchCount === 2) return HttpResponse.json([keep]); // "app" deleted
      return HttpResponse.json([app, keep]);                  // present otherwise
    }),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/scan", () => HttpResponse.json({ status: "checked" })),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  // Refetch #2: "app" disappears from the payload (no filter active, so the
  // seed effect prunes its id from `seenProjects`).
  await userEvent.click(screen.getByRole("button", { name: /^check all services$/i }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "app" })).not.toBeInTheDocument());
  // Refetch #3: "app" comes back. It must return COLLAPSED, not expanded.
  await userEvent.click(screen.getByRole("button", { name: /^check all services$/i }));
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
});

// The lifecycle/apply mutations only enqueue a job; the busy store keeps the
// row's job-backed buttons disabled (initiator spinning) until job_finished
// clears it. Clicking Stop must grey out Stop AND Restart for that row, and
// clearJobBusy (what the SSE handler calls) must re-enable both.
test("stopping a service disables its lifecycle buttons until the job clears", async () => {
  const { clearJobBusy } = await import("@/hooks/useBusyServices");
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1,
          name: "app",
          kind: "compose",
          working_dir: "/srv",
          auto_update_enabled: false,
          services: [
            {
              id: 10,
              name: "web",
              image_ref: "nginx:1.27",
              current_digest: "sha256:a",
              state: "running",
              pinned: false,
              healthcheck: false,
              auto_update_enabled: null,
            },
          ],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/services/10/lifecycle", () => HttpResponse.json({ job_id: 77 })),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  const stop = await screen.findByRole("button", { name: "Stop web" });
  await userEvent.click(stop);
  // Enqueue resolved; without the busy store both buttons would re-enable here.
  await waitFor(() => expect(screen.getByRole("button", { name: "Stop web" })).toBeDisabled());
  expect(screen.getByRole("button", { name: "Restart web" })).toBeDisabled();
  // job_finished for job 77 (what useEventStream calls) re-enables the row.
  clearJobBusy(77);
  await waitFor(() => expect(screen.getByRole("button", { name: "Stop web" })).toBeEnabled());
  expect(screen.getByRole("button", { name: "Restart web" })).toBeEnabled();
});
