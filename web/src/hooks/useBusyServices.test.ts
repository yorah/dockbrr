import { beforeEach, afterEach, describe, expect, test, vi } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { __resetBusyServices, clearJobBusy, markServiceBusy, useBusyServices } from "./useBusyServices";

beforeEach(() => __resetBusyServices());
afterEach(() => __resetBusyServices());

describe("useBusyServices", () => {
  test("marking a service busy exposes it in the map", () => {
    const { result } = renderHook(() => useBusyServices());
    expect(result.current.get(10)).toBeUndefined();
    act(() => markServiceBusy(10, 100, "apply"));
    expect(result.current.get(10)).toBe("apply");
  });

  test("clearJobBusy clears every entry carrying that job id", () => {
    const { result } = renderHook(() => useBusyServices());
    act(() => {
      markServiceBusy(10, 100, "apply");
      markServiceBusy(11, 100, "apply"); // e.g. project-wide apply-all, same job
      markServiceBusy(12, 200, "start"); // unrelated job, must survive
    });
    act(() => clearJobBusy(100));
    expect(result.current.get(10)).toBeUndefined();
    expect(result.current.get(11)).toBeUndefined();
    expect(result.current.get(12)).toBe("start");
  });

  test("clearJobBusy for an unknown job id is a no-op", () => {
    const { result } = renderHook(() => useBusyServices());
    act(() => markServiceBusy(10, 100, "apply"));
    act(() => clearJobBusy(999));
    expect(result.current.get(10)).toBe("apply");
  });

  test("a stranded entry self-clears after the fallback timeout, so a dropped SSE connection can't spin forever", () => {
    vi.useFakeTimers();
    try {
      const { result } = renderHook(() => useBusyServices());
      act(() => markServiceBusy(10, 100, "start"));
      expect(result.current.get(10)).toBe("start");
      act(() => vi.advanceTimersByTime(10 * 60 * 1000 - 1));
      expect(result.current.get(10)).toBe("start");
      act(() => vi.advanceTimersByTime(1));
      expect(result.current.get(10)).toBeUndefined();
    } finally {
      vi.useRealTimers();
    }
  });

  test("re-marking a service resets its action and its timeout", () => {
    vi.useFakeTimers();
    try {
      const { result } = renderHook(() => useBusyServices());
      act(() => markServiceBusy(10, 100, "start"));
      act(() => vi.advanceTimersByTime(9 * 60 * 1000)); // most of the way to the old timeout
      act(() => markServiceBusy(10, 101, "restart"));
      expect(result.current.get(10)).toBe("restart");
      // If the first timer weren't cleared, it would have fired by now.
      act(() => vi.advanceTimersByTime(2 * 60 * 1000));
      expect(result.current.get(10)).toBe("restart");
    } finally {
      vi.useRealTimers();
    }
  });

  test("returns the same map reference across renders when nothing changed", () => {
    const { result, rerender } = renderHook(() => useBusyServices());
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
  });
});
