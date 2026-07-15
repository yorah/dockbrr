import { expect, test, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { render, screen, waitFor, within } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { makeQueryClient } from "@/api/queryClient";
import { router } from "@/router";

function renderDashboardWithRouter() {
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
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
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  await userEvent.click(screen.getByRole("button", { name: "app" })); // collapse (exact: avoids the "Apply all updates in app" button)
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

  const button = await screen.findByRole("button", { name: /last applied changelog for web/i });
  await userEvent.click(button);

  // The read-only drawer opens with the cached markdown; no Apply control.
  await waitFor(() => expect(screen.getByText("What's new")).toBeInTheDocument());
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

  const button = await screen.findByRole("button", { name: /^changelog for web$/i });
  expect(screen.queryByRole("button", { name: /last applied changelog for web/i })).not.toBeInTheDocument();
  await userEvent.click(button);

  await waitFor(() => expect(screen.getByText("Pending notes")).toBeInTheDocument());
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

  // Named project's service is visible; the loose service is hidden by default.
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  expect(screen.queryByText("busybox:latest")).not.toBeInTheDocument();

  // The Loose group header shows the count; expanding reveals the loose service.
  // Scoped to <main>: the sidebar (Task 5) also renders its own "Loose (1)" toggle.
  const toggle = within(screen.getByRole("main")).getByRole("button", { name: /loose \(1\)/i });
  await userEvent.click(toggle);
  await waitFor(() => expect(screen.getByText("busybox:latest")).toBeInTheDocument());
});
