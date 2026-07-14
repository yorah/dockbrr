import { beforeEach, describe, expect, test, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { makeQueryClient } from "@/api/queryClient";
import { router } from "@/router";

// Regression coverage for a bug that a bare AuthGate + LoginScreen harness
// couldn't catch: with the full app tree mounted, /api/projects and
// /api/updates are also active queries. Clearing the whole cache in one
// batch (qc.clear()) let those queries' refetch/401 handling interfere with
// the me/setup-status observers, so AuthGate never flipped to LoginScreen:
// the dashboard just showed "Failed to load dashboard data" and stayed put.
describe("full app tree: auth transitions", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {},
    }));
  });

  test("logout shows the login screen even with dashboard queries active", async () => {
    let authed = true;
    server.use(
      http.get("/api/setup/status", () => HttpResponse.json({ needs_setup: false })),
      http.get("/api/auth/me", () =>
        authed ? HttpResponse.json({ username: "admin" }) : new HttpResponse(null, { status: 401 })),
      http.post("/api/auth/logout", () => { authed = false; return new HttpResponse(null, { status: 204 }); }),
      http.get("/api/projects", () => authed ? HttpResponse.json([]) : new HttpResponse(null, { status: 401 })),
      http.get("/api/updates", () => authed ? HttpResponse.json([]) : new HttpResponse(null, { status: 401 })),
    );
    const client = makeQueryClient();
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByRole("button", { name: /logout/i })).toBeInTheDocument());
    screen.getByRole("button", { name: /logout/i }).click();
    await waitFor(() => expect(screen.getByRole("heading", { name: /sign in/i })).toBeInTheDocument(), { timeout: 3000 });
  });
});
