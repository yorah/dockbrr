import { beforeEach, describe, expect, test, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { SIDEBAR_STORAGE_KEY, useSidebar } from "@/hooks/useSidebar";

// jsdom has no matchMedia. Install a controllable stub.
let listeners: Array<(e: { matches: boolean }) => void> = [];
function stubMatchMedia(matches: boolean) {
  listeners = [];
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches,
    media: query,
    addEventListener: (_: string, cb: (e: { matches: boolean }) => void) => listeners.push(cb),
    removeEventListener: (_: string, cb: (e: { matches: boolean }) => void) => {
      listeners = listeners.filter((l) => l !== cb);
    },
  }));
}

describe("useSidebar", () => {
  beforeEach(() => {
    localStorage.clear();
    stubMatchMedia(false);
  });

  test("defaults to expanded on a wide viewport", () => {
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(false);
  });

  test("toggle flips the state and persists it", () => {
    const { result } = renderHook(() => useSidebar());
    act(() => result.current.toggle());
    expect(result.current.collapsed).toBe(true);
    expect(localStorage.getItem(SIDEBAR_STORAGE_KEY)).toBe("collapsed");
    act(() => result.current.toggle());
    expect(result.current.collapsed).toBe(false);
    expect(localStorage.getItem(SIDEBAR_STORAGE_KEY)).toBe("expanded");
  });

  test("restores the persisted collapsed state", () => {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, "collapsed");
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(true);
  });

  test("a narrow viewport forces the rail on mount", () => {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, "expanded");
    stubMatchMedia(true);
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(true);
  });

  test("shrinking the viewport collapses, widening does not re-expand", () => {
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(false);
    act(() => listeners.forEach((cb) => cb({ matches: true })));
    expect(result.current.collapsed).toBe(true);
    act(() => listeners.forEach((cb) => cb({ matches: false })));
    expect(result.current.collapsed).toBe(true);
  });

  test("isNarrow is false on mount on a wide viewport", () => {
    const { result } = renderHook(() => useSidebar());
    expect(result.current.isNarrow).toBe(false);
  });

  test("isNarrow is true on mount when matchMedia reports a match", () => {
    stubMatchMedia(true);
    const { result } = renderHook(() => useSidebar());
    expect(result.current.isNarrow).toBe(true);
  });

  test("isNarrow flips when the stubbed change listener fires", () => {
    const { result } = renderHook(() => useSidebar());
    expect(result.current.isNarrow).toBe(false);
    act(() => listeners.forEach((cb) => cb({ matches: true })));
    expect(result.current.isNarrow).toBe(true);
    act(() => listeners.forEach((cb) => cb({ matches: false })));
    expect(result.current.isNarrow).toBe(false);
  });
});
