import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import { makeQueryClient } from "@/api/queryClient";
import { routeTree } from "@/router";

export function renderWithClient(ui: ReactNode) {
  const client = makeQueryClient();
  const result = render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
  return { client, ...result };
}

/**
 * Render the whole app (AuthGate + AppLayout shell + routes) at `initialPath`.
 * A fresh router per call keeps navigation state from leaking between tests.
 */
export function renderApp(initialPath = "/") {
  const client = makeQueryClient();
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  });
  const result = render(
    <QueryClientProvider client={client}>
      {/* The app's `Register` interface types the singleton router; a per-test
          router has the same route tree but is structurally a distinct type. */}
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  );
  return { client, ...result };
}
