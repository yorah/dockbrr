import type { ReactNode } from "react";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { makeQueryClient } from "@/api/queryClient";
import { keys } from "@/api/keys";
import { useApply, useLifecycle, useProjectScan, useRemoveContainer, useScanAll, useServiceCheck } from "./mutations";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() }, Toaster: () => null }));
import { toast } from "sonner";

beforeEach(() => {
  vi.clearAllMocks();
});

function wrapper(client = makeQueryClient()) {
  const W = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { W, client };
}

describe("useApply", () => {
  test("posts scope and invalidates updates + projects", async () => {
    server.use(http.post("/api/updates/5/apply", () => HttpResponse.json({ job_id: 42 })));
    const { W, client } = wrapper();
    const invalidated: unknown[][] = [];
    const orig = client.invalidateQueries.bind(client);
    client.invalidateQueries = ((arg: any) => { invalidated.push(arg?.queryKey); return orig(arg); }) as typeof client.invalidateQueries;

    const { result } = renderHook(() => useApply(), { wrapper: W });
    const res = await result.current.mutateAsync({ id: 5, scope: "service" });
    expect(res.job_id).toBe(42);
    await waitFor(() => {
      expect(invalidated).toContainEqual(keys.updates);
      expect(invalidated).toContainEqual(keys.projects);
    });
  });

  test("surfaces server errors as a toast", async () => {
    server.use(http.post("/api/updates/9/apply", () => HttpResponse.json({ error: "boom" }, { status: 500 })));
    const { W } = wrapper();
    const { result } = renderHook(() => useApply(), { wrapper: W });
    result.current.mutate({ id: 9, scope: "service" });
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });
});

describe("useServiceCheck", () => {
  test("posts to the service check endpoint, no success toast (SSE drives the UI)", async () => {
    server.use(http.post("/api/services/7/check", () => HttpResponse.json({ running: true, total: 1 })));
    const { W } = wrapper();
    const { result } = renderHook(() => useServiceCheck(), { wrapper: W });
    result.current.mutate(7);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(toast.success).not.toHaveBeenCalled();
  });

  test("surfaces errors as toasts", async () => {
    server.use(
      http.post("/api/services/7/check", () => HttpResponse.json({ error: "registry down" }, { status: 502 })),
    );
    const { W } = wrapper();
    const { result } = renderHook(() => useServiceCheck(), { wrapper: W });
    result.current.mutate(7);
    await waitFor(() => expect(result.current.isError).toBe(true));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/^registry down/), expect.anything()));
  });

  test("swallows a 409 (a scan-run is already in flight) without an error toast", async () => {
    server.use(
      http.post("/api/services/7/check", () => HttpResponse.json({ error: "scan already running" }, { status: 409 })),
    );
    const { W } = wrapper();
    const { result } = renderHook(() => useServiceCheck(), { wrapper: W });
    result.current.mutate(7);
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("useProjectScan", () => {
  test("posts project_id to /api/scan", async () => {
    let body: unknown;
    server.use(
      http.post("/api/scan", async ({ request }) => {
        body = await request.json();
        return HttpResponse.json({ running: true, total: 2 });
      }),
    );
    const { W } = wrapper();
    const { result } = renderHook(() => useProjectScan(), { wrapper: W });
    result.current.mutate(3);
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(body).toEqual({ project_id: 3 });
  });
});

describe("useScanAll", () => {
  test("posts to /api/scan with no body", async () => {
    let sawBody = false;
    server.use(
      http.post("/api/scan", async ({ request }) => {
        const text = await request.text();
        sawBody = text.length > 0;
        return HttpResponse.json({ running: true, total: 5 });
      }),
    );
    const { W } = wrapper();
    const { result } = renderHook(() => useScanAll(), { wrapper: W });
    result.current.mutate();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(sawBody).toBe(false);
  });
});

describe("useLifecycle", () => {
  test("posts action and invalidates projects", async () => {
    server.use(http.post("/api/services/3/lifecycle", () => HttpResponse.json({ job_id: 11 })));
    const { W, client } = wrapper();
    const invalidated: unknown[][] = [];
    const orig = client.invalidateQueries.bind(client);
    client.invalidateQueries = ((arg: any) => { invalidated.push(arg?.queryKey); return orig(arg); }) as typeof client.invalidateQueries;

    const { result } = renderHook(() => useLifecycle(), { wrapper: W });
    const res = await result.current.mutateAsync({ serviceId: 3, action: "restart" });
    expect(res.job_id).toBe(11);
    await waitFor(() => expect(invalidated).toContainEqual(keys.projects));
  });

  test("surfaces server errors as a toast", async () => {
    server.use(http.post("/api/services/3/lifecycle", () => HttpResponse.json({ error: "boom" }, { status: 500 })));
    const { W } = wrapper();
    const { result } = renderHook(() => useLifecycle(), { wrapper: W });
    result.current.mutate({ serviceId: 3, action: "stop" });
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });
});

describe("useRemoveContainer", () => {
  test("posts remove and invalidates projects", async () => {
    server.use(http.post("/api/services/3/remove", () => HttpResponse.json({ job_id: 12 })));
    const { W, client } = wrapper();
    const invalidated: unknown[][] = [];
    const orig = client.invalidateQueries.bind(client);
    client.invalidateQueries = ((arg: any) => { invalidated.push(arg?.queryKey); return orig(arg); }) as typeof client.invalidateQueries;

    const { result } = renderHook(() => useRemoveContainer(), { wrapper: W });
    const res = await result.current.mutateAsync(3);
    expect(res.job_id).toBe(12);
    await waitFor(() => expect(invalidated).toContainEqual(keys.projects));
  });

  test("surfaces server errors as a toast", async () => {
    server.use(http.post("/api/services/3/remove", () => HttpResponse.json({ error: "boom" }, { status: 500 })));
    const { W } = wrapper();
    const { result } = renderHook(() => useRemoveContainer(), { wrapper: W });
    result.current.mutate(3);
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
  });
});
