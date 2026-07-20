import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useDismissedUpdate, clearDismissedUpdate, DISMISS_KEY } from "./useDismissedUpdate";

afterEach(() => localStorage.clear());

describe("useDismissedUpdate", () => {
  it("reflects a dismiss and re-renders on an external clear", () => {
    const { result } = renderHook(() => useDismissedUpdate());
    expect(result.current.dismissed).toBeNull();

    act(() => result.current.dismiss("0.8.0"));
    expect(result.current.dismissed).toBe("0.8.0");
    expect(localStorage.getItem(DISMISS_KEY)).toBe("0.8.0");

    act(() => clearDismissedUpdate());
    expect(result.current.dismissed).toBeNull();
  });
});
