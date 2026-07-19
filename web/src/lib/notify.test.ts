import { afterEach, beforeEach, expect, test, vi } from "vitest";

const calls: { kind: string; message: string; opts?: { id?: unknown; duration?: number } }[] = [];
vi.mock("sonner", () => ({
  toast: {
    success: (m: string, o?: { id?: unknown; duration?: number }) => {
      calls.push({ kind: "success", message: m, opts: o });
      return calls.length;
    },
    error: (m: string, o?: { id?: unknown; duration?: number }) => {
      calls.push({ kind: "error", message: m, opts: o });
      return calls.length;
    },
    info: (m: string, o?: { id?: unknown; duration?: number }) => {
      calls.push({ kind: "info", message: m, opts: o });
      return calls.length;
    },
  },
}));

import { notify, TOAST_DURATION_MS } from "./notify";

beforeEach(() => {
  calls.length = 0;
  vi.useFakeTimers();
});
afterEach(() => vi.useRealTimers());

test("toast starts with the full countdown and ticks down in place", () => {
  notify.success("Check complete");
  expect(calls[0].message).toBe(`Check complete (${TOAST_DURATION_MS / 1000}s)`);
  expect(calls[0].opts?.duration).toBe(TOAST_DURATION_MS);

  vi.advanceTimersByTime(1000);
  expect(calls[1].message).toBe(`Check complete (${TOAST_DURATION_MS / 1000 - 1}s)`);
  // Update targets the SAME toast (id passed), with the remaining duration.
  expect(calls[1].opts?.id).toBeDefined();
  expect(calls[1].opts!.duration!).toBeLessThanOrEqual(TOAST_DURATION_MS - 1000);
});

test("ticking stops once the duration is spent", () => {
  notify.error("Boom", 2000);
  vi.advanceTimersByTime(10_000);
  // Initial + at most one update at t=1s; nothing after expiry.
  expect(calls.length).toBe(2);
  expect(calls[1].message).toBe("Boom (1s)");
});
