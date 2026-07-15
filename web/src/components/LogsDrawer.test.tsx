import { expect, test, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { QueryClientProvider } from "@tanstack/react-query";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { LogsDrawer } from "./LogsDrawer";
import type { Service } from "@/api/types";

const service: Service = {
  id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:c",
  state: "running", pinned: false, drifted: false, healthcheck: false,
  auto_update_enabled: null, check_status: "ok", last_checked: "",
};

test("fetches and renders the log tail for the service on open", async () => {
  server.use(
    http.get("/api/services/10/logs", () => HttpResponse.json({ logs: "line one\nline two" })),
  );

  renderWithClient(<LogsDrawer service={service} open onOpenChange={vi.fn()} />);

  expect(await screen.findByText(/line one/)).toBeInTheDocument();
  expect(screen.getByText(/line two/)).toBeInTheDocument();
});

test("Refresh re-fetches and renders the updated tail", async () => {
  let call = 0;
  server.use(
    http.get("/api/services/10/logs", () => {
      call += 1;
      return HttpResponse.json({ logs: call === 1 ? "first fetch" : "second fetch" });
    }),
  );

  renderWithClient(<LogsDrawer service={service} open onOpenChange={vi.fn()} />);

  expect(await screen.findByText(/first fetch/)).toBeInTheDocument();

  await userEvent.click(screen.getByRole("button", { name: /refresh/i }));

  expect(await screen.findByText(/second fetch/)).toBeInTheDocument();
  expect(screen.queryByText(/first fetch/)).not.toBeInTheDocument();
});

test("shows an error state when the fetch fails", async () => {
  server.use(
    http.get("/api/services/10/logs", () => HttpResponse.json({ error: "boom" }, { status: 500 })),
  );

  renderWithClient(<LogsDrawer service={service} open onOpenChange={vi.fn()} />);

  expect(await screen.findByText(/failed to load logs/i)).toBeInTheDocument();
});

test("renders nothing fetched when no service is set", () => {
  renderWithClient(<LogsDrawer service={null} open={false} onOpenChange={vi.fn()} />);
  expect(screen.queryByText(/failed to load logs/i)).not.toBeInTheDocument();
});

test("ignores a stale response for a service the user has since switched away from", async () => {
  const other: Service = { ...service, id: 11, name: "cache" };
  let resolveSlow: (() => void) | undefined;
  server.use(
    http.get("/api/services/10/logs", () =>
      new Promise((resolve) => {
        resolveSlow = () => resolve(HttpResponse.json({ logs: "slow service-10 logs" }));
      }),
    ),
    http.get("/api/services/11/logs", () => HttpResponse.json({ logs: "fast service-11 logs" })),
  );

  const { client, rerender } = renderWithClient(
    <LogsDrawer service={service} open onOpenChange={vi.fn()} />,
  );

  // Switch to a different service before the first (slow) fetch resolves.
  // Re-wrap with the same QueryClientProvider/client so the query context
  // isn't dropped by the rerender.
  rerender(
    <QueryClientProvider client={client}>
      <LogsDrawer service={other} open onOpenChange={vi.fn()} />
    </QueryClientProvider>,
  );

  expect(await screen.findByText(/fast service-11 logs/)).toBeInTheDocument();

  // Now let the stale service-10 response resolve; it must not clobber the
  // logs currently shown for service-11.
  resolveSlow?.();
  await new Promise((r) => setTimeout(r, 0));
  expect(screen.getByText(/fast service-11 logs/)).toBeInTheDocument();
  expect(screen.queryByText(/slow service-10 logs/)).not.toBeInTheDocument();
});
