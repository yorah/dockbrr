import "@testing-library/jest-dom/vitest";
import { afterAll, afterEach, beforeAll } from "vitest";
import { cleanup } from "@testing-library/react";
import { server } from "./msw";

// jsdom has no EventSource. Components that open SSE streams (useEventStream in
// AppLayout, useJobLog) are mounted by full-tree tests that don't install the
// __setEventSourceFactory seam, so provide a no-op stub to keep them from
// throwing. Tests that assert stream behavior override the factory seam instead.
if (typeof globalThis.EventSource === "undefined") {
  class NoopEventSource {
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    close() {}
    addEventListener() {}
    removeEventListener() {}
  }
  globalThis.EventSource = NoopEventSource as unknown as typeof EventSource;
}

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  cleanup();
  server.resetHandlers();
  // Components persist UI state (dashboard collapse) in sessionStorage; clear
  // it so one test's expand/collapse choices never leak into the next.
  sessionStorage.clear();
});
afterAll(() => server.close());
