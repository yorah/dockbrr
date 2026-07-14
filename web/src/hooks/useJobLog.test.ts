import { afterEach, describe, expect, test } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useJobLog, __setEventSourceFactory } from "./useJobLog";

class FakeES {
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  url: string;
  closed = false;
  static last: FakeES | null = null;
  constructor(url: string) { this.url = url; FakeES.last = this; }
  emit(data: string) { this.onmessage?.({ data } as MessageEvent); }
  close() { this.closed = true; }
}

afterEach(() => __setEventSourceFactory(null));

describe("useJobLog", () => {
  test("appends parsed {stream,line} events", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { result } = renderHook(() => useJobLog(7));
    expect(FakeES.last?.url).toContain("/api/jobs/7/logs");
    act(() => FakeES.last!.emit(JSON.stringify({ stream: "stdout", line: "Pulling web…" })));
    act(() => FakeES.last!.emit(JSON.stringify({ stream: "stdout", line: "done" })));
    expect(result.current.lines.map((l) => l.line)).toEqual(["Pulling web…", "done"]);
  });

  test("closes the source on unmount", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { unmount } = renderHook(() => useJobLog(7));
    const es = FakeES.last!;
    unmount();
    expect(es.closed).toBe(true);
  });

  test("closes the old source and opens a new one when jobId changes", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { rerender } = renderHook(({ id }) => useJobLog(id), {
      initialProps: { id: 7 },
    });
    const first = FakeES.last!;
    rerender({ id: 8 });
    const second = FakeES.last!;
    expect(first.closed).toBe(true);
    expect(second.url).toContain("/api/jobs/8/logs");
    expect(second).not.toBe(first);
  });

  test("marks closed after an error on the source", () => {
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    const { result } = renderHook(() => useJobLog(7));
    const es = FakeES.last!;
    act(() => es.onerror?.({} as Event));
    expect(result.current.closed).toBe(true);
    expect(es.closed).toBe(true);
  });

  test("null jobId opens nothing", () => {
    FakeES.last = null;
    __setEventSourceFactory((url) => new FakeES(url) as unknown as EventSource);
    renderHook(() => useJobLog(null));
    expect(FakeES.last).toBeNull();
  });
});
