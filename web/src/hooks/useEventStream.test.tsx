import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { makeQueryClient } from "@/api/queryClient";
import { keys } from "@/api/keys";
import { __resetBusyServices, markServiceBusy, useBusyServices } from "@/hooks/useBusyServices";
import { useEventStream, __setEventSourceFactory } from "./useEventStream";

beforeEach(() => __resetBusyServices());

class FakeES {
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  onopen: ((e: Event) => void) | null = null;
  url: string;
  closed = false;
  static last: FakeES | null = null;
  static created = 0;
  constructor(url: string) { this.url = url; FakeES.last = this; FakeES.created += 1; }
  emit(data: string) { this.onmessage?.({ data } as MessageEvent); }
  close() { this.closed = true; }
}

afterEach(() => __setEventSourceFactory(null));

function wrapper(client = makeQueryClient()) {
  const invalidated: unknown[][] = [];
  const orig = client.invalidateQueries.bind(client);
  client.invalidateQueries = ((arg: any) => { invalidated.push(arg?.queryKey); return orig(arg); }) as typeof client.invalidateQueries;
  const W = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { W, client, invalidated };
}

describe("useEventStream", () => {
  test("opens the stream against /api/events/stream", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W } = wrapper();
    renderHook(() => useEventStream(), { wrapper: W });
    expect(FakeES.last?.url).toContain("/api/events/stream");
  });

  test("detected invalidates updates + the service's events", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W, invalidated } = wrapper();
    renderHook(() => useEventStream(), { wrapper: W });
    act(() => FakeES.last!.emit(JSON.stringify({ type: "detected", service_id: 3 })));
    expect(invalidated).toContainEqual(keys.updates);
    expect(invalidated).toContainEqual(keys.serviceEvents(3));
  });

  test("job_finished invalidates updates, projects, and the job", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W, invalidated } = wrapper();
    renderHook(() => useEventStream(), { wrapper: W });
    act(() => FakeES.last!.emit(JSON.stringify({ type: "job_finished", job_id: 42 })));
    expect(invalidated).toContainEqual(keys.updates);
    expect(invalidated).toContainEqual(keys.projects);
    expect(invalidated).toContainEqual(keys.job(42));
  });

  test("job_finished clears the busy marker for services enqueued under that job", async () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W } = wrapper();
    renderHook(() => useEventStream(), { wrapper: W });
    const busy = renderHook(() => useBusyServices());
    act(() => markServiceBusy(10, 42, "apply"));
    expect(busy.result.current.get(10)).toBe("apply");
    act(() => FakeES.last!.emit(JSON.stringify({ type: "job_finished", job_id: 42 })));
    // Clearing is deliberately chained AFTER the query refetches resolve (the
    // row must show its post-job state before its buttons re-enable), so it is
    // asynchronous here.
    await waitFor(() => expect(busy.result.current.get(10)).toBeUndefined());
  });

  test("reconciled invalidates projects", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W, invalidated } = wrapper();
    renderHook(() => useEventStream(), { wrapper: W });
    act(() => FakeES.last!.emit(JSON.stringify({ type: "reconciled" })));
    expect(invalidated).toContainEqual(keys.projects);
  });

  test("scanned invalidates status, updates, and projects", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W, invalidated } = wrapper();
    renderHook(() => useEventStream(), { wrapper: W });
    act(() => FakeES.last!.emit(JSON.stringify({ type: "scanned" })));
    expect(invalidated).toContainEqual(keys.status);
    expect(invalidated).toContainEqual(keys.updates);
    expect(invalidated).toContainEqual(keys.projects);
  });

  test("ignores malformed frames", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W, invalidated } = wrapper();
    renderHook(() => useEventStream(), { wrapper: W });
    act(() => FakeES.last!.emit("not json"));
    expect(invalidated).toHaveLength(0);
  });

  test("closes the source on unmount", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W } = wrapper();
    const { unmount } = renderHook(() => useEventStream(), { wrapper: W });
    const es = FakeES.last!;
    unmount();
    expect(es.closed).toBe(true);
  });

  test("closes the source on error", () => {
    vi.useFakeTimers();
    try {
      __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
      const { W } = wrapper();
      const { unmount } = renderHook(() => useEventStream(), { wrapper: W });
      const es = FakeES.last!;
      act(() => es.onerror?.({} as Event));
      expect(es.closed).toBe(true);
      unmount(); // clears the pending reconnect timer
    } finally {
      vi.useRealTimers();
    }
  });

  test("reconnects with backoff after a transient error", () => {
    vi.useFakeTimers();
    try {
      __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
      const { W } = wrapper();
      FakeES.created = 0;
      const { unmount } = renderHook(() => useEventStream(), { wrapper: W });
      expect(FakeES.created).toBe(1);
      // Transient error → source closed, reconnect scheduled (not immediate).
      act(() => FakeES.last!.onerror?.({} as Event));
      expect(FakeES.created).toBe(1);
      // After the first backoff (1s) a fresh source is opened.
      act(() => vi.advanceTimersByTime(1000));
      expect(FakeES.created).toBe(2);
      expect(FakeES.last!.closed).toBe(false);
      unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  test("stops reconnecting after unmount", () => {
    vi.useFakeTimers();
    try {
      __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
      const { W } = wrapper();
      FakeES.created = 0;
      const { unmount } = renderHook(() => useEventStream(), { wrapper: W });
      act(() => FakeES.last!.onerror?.({} as Event));
      unmount();
      act(() => vi.advanceTimersByTime(60000));
      expect(FakeES.created).toBe(1); // no reconnect after teardown
    } finally {
      vi.useRealTimers();
    }
  });

  test("disabled opens nothing", () => {
    FakeES.last = null;
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { W } = wrapper();
    renderHook(() => useEventStream(false), { wrapper: W });
    expect(FakeES.last).toBeNull();
  });
});
