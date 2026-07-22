import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { delay, http, HttpResponse } from "msw";
import { afterEach, describe, expect, it, test } from "vitest";
import { server } from "@/test/msw";
import { makeQueryClient } from "@/api/queryClient";
import { ScanProgress } from "./ScanProgress";
import { setScanRun, __resetScanRun } from "@/hooks/useScanRun";

afterEach(() => __resetScanRun());

// The Cancel button's mutation (useScanAbort) needs a QueryClientProvider;
// mirrors the wrapper pattern in mutations.test.tsx / DashboardTable.test.tsx.
function makeWrapper() {
  const client = makeQueryClient();
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

describe("ScanProgress", () => {
  it("renders nothing when idle", () => {
    const { container } = render(<ScanProgress />, { wrapper: makeWrapper() });
    expect(container).toBeEmptyDOMElement();
  });

  it("shows a determinate count while running", () => {
    setScanRun({ running: true, done: 4, total: 12 });
    render(<ScanProgress />, { wrapper: makeWrapper() });
    expect(screen.getByText(/4\s*\/\s*12/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "4");
  });

  test("Cancel button aborts the scan via DELETE /api/scan and disables itself", async () => {
    let deleted = false;
    server.use(
      // A small delay keeps the mutation observably pending: with an instant
      // mock response, isPending flips true->false within the same microtask
      // flush the test would poll on, so "disabled" could never be caught.
      http.delete("/api/scan", async () => {
        deleted = true;
        await delay(20);
        return new HttpResponse(null, { status: 204 });
      }),
    );
    setScanRun({ running: true, done: 2, total: 10 });
    render(<ScanProgress />, { wrapper: makeWrapper() });

    const cancel = screen.getByRole("button", { name: /cancel/i });
    fireEvent.click(cancel);

    await waitFor(() => expect(cancel).toBeDisabled());
    await waitFor(() => expect(deleted).toBe(true));
  });
});
