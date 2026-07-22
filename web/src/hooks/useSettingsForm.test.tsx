import { renderHook, act, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { keys } from "@/api/keys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import type { Settings } from "@/api/types";

const SETTINGS: Settings = {
  poll_interval_seconds: "900",
  scan_on_start: "true",
  concurrency: "4",
  health_timeout_seconds: "60",
  health_poll_seconds: "3",
  write_back_compose: "false",
  auto_remove_gone: "false",
  default_auto_update_enabled: "false",
  gone_grace_seconds: "86400",
  job_retention_days: "30",
  github_token_set: false,
  restart_required: [],
  defaults: { poll_interval_seconds: "900", concurrency: "4" },
};

const fetchMock = vi.fn();
function wrapper({ children }: { children: ReactNode }) {
  const client = makeQueryClient();
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function stubFetch() {
  fetchMock.mockImplementation(async (_url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return new Response(null, { status: 204 });
    return new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
}

afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
});

describe("useSettingsForm", () => {
  it("is not dirty until a field changes, then sends only changed keys", async () => {
    stubFetch();
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds", "concurrency"]), { wrapper });

    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.dirty).toBe(false);

    act(() => result.current.setField("poll_interval_seconds", "600"));
    expect(result.current.dirty).toBe(true);

    act(() => result.current.save());
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ poll_interval_seconds: "600" });
    });
  });

  it("flags a field still matching the server-side default", async () => {
    stubFetch();
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds"]), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.isDefault("poll_interval_seconds")).toBe(true);

    act(() => result.current.setField("poll_interval_seconds", "600"));
    expect(result.current.isDefault("poll_interval_seconds")).toBe(false);
  });

  it("merges `extra` keys into the PUT (used for the write-only GitHub token)", async () => {
    stubFetch();
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds"]), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());

    act(() => result.current.save({ github_token: "ghp_x" }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ github_token: "ghp_x" });
    });
  });

  it("does not clobber a dirty form when a background refetch delivers different server values", async () => {
    stubFetch();
    const client = makeQueryClient();
    function localWrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    }
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds", "concurrency"]), {
      wrapper: localWrapper,
    });
    await waitFor(() => expect(result.current.data).toBeDefined());

    // User starts editing but hasn't saved yet.
    act(() => result.current.setField("poll_interval_seconds", "600"));
    expect(result.current.dirty).toBe(true);

    // A background refetch (e.g. window-focus refetch, or another tab saving)
    // lands DIFFERENT values into the cache while the edit is still pending.
    act(() => {
      client.setQueryData(keys.settings, { ...SETTINGS, poll_interval_seconds: "1200", concurrency: "8" });
    });

    // The user's in-progress edit must survive. It must not be silently wiped.
    expect(result.current.form.poll_interval_seconds).toBe("600");
    expect(result.current.dirty).toBe(true);

    // Let the new server data reach the hook (wait on `data`, not on `form`, so
    // this holds regardless of how re-seeding behaves).
    await waitFor(() => expect(result.current.data?.concurrency).toBe("8"));
    // The UNTOUCHED field must have followed the server to "8". If it stayed
    // pinned to the stale seed ("4"), `changed()` would diff it against the new
    // server value and drag `concurrency: "4"` into the PUT below, silently
    // reverting the 8 the server had just been told.
    expect(result.current.form.concurrency).toBe("8");

    // So the PUT carries ONLY the key the user actually edited.
    act(() => result.current.save());
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ poll_interval_seconds: "600" });
    });
  });

  it("a clean form still picks up new server values from a background refetch", async () => {
    stubFetch();
    const client = makeQueryClient();
    function localWrapper({ children }: { children: ReactNode }) {
      return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    }
    const { result } = renderHook(() => useSettingsForm(["poll_interval_seconds", "concurrency"]), {
      wrapper: localWrapper,
    });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.dirty).toBe(false);

    act(() => {
      client.setQueryData(keys.settings, { ...SETTINGS, poll_interval_seconds: "1200", concurrency: "8" });
    });

    await waitFor(() => expect(result.current.form.poll_interval_seconds).toBe("1200"));
    expect(result.current.form.concurrency).toBe("8");
  });
});
