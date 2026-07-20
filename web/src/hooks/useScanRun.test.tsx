import { renderHook, act } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useScanRun, setScanRun, __resetScanRun } from "./useScanRun";

afterEach(() => __resetScanRun());

describe("useScanRun", () => {
  it("defaults to not running", () => {
    const { result } = renderHook(() => useScanRun());
    expect(result.current).toEqual({ running: false, done: 0, total: 0 });
  });

  it("reflects setScanRun updates", () => {
    const { result } = renderHook(() => useScanRun());
    act(() => setScanRun({ running: true, done: 2, total: 5 }));
    expect(result.current).toEqual({ running: true, done: 2, total: 5 });
  });

  it("keeps a stable snapshot reference when value is unchanged", () => {
    const { result, rerender } = renderHook(() => useScanRun());
    const first = result.current;
    act(() => setScanRun({ running: false, done: 0, total: 0 }));
    rerender();
    expect(result.current).toBe(first);
  });
});
