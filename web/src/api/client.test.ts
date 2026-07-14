import { afterEach, describe, expect, test, vi } from "vitest";
import { apiFetch, readCsrfToken, UnauthorizedError, ApiError } from "./client";

function setCookie(v: string) {
  Object.defineProperty(document, "cookie", { writable: true, value: v });
}

describe("apiFetch", () => {
  const realFetch = globalThis.fetch;
  afterEach(() => { globalThis.fetch = realFetch; vi.restoreAllMocks(); });

  test("reads dockbrr_csrf cookie", () => {
    setCookie("foo=1; dockbrr_csrf=abc123; bar=2");
    expect(readCsrfToken()).toBe("abc123");
  });

  test("GET omits the CSRF header and includes credentials", async () => {
    setCookie("dockbrr_csrf=abc123");
    const spy = vi.fn(async (_input: RequestInfo | URL, _init: RequestInit) =>
      new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    globalThis.fetch = spy as unknown as typeof fetch;
    await apiFetch("/api/projects");
    const [, init] = spy.mock.calls[0];
    expect(init.credentials).toBe("include");
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBeNull();
  });

  test("POST attaches X-CSRF-Token from the cookie", async () => {
    setCookie("dockbrr_csrf=abc123");
    const spy = vi.fn(async (_input: RequestInfo | URL, _init: RequestInit) => new Response(null, { status: 204 }));
    globalThis.fetch = spy as unknown as typeof fetch;
    await apiFetch("/api/updates/1/dismiss", { method: "POST" });
    const [, init] = spy.mock.calls[0];
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("abc123");
  });

  test("401 throws UnauthorizedError", async () => {
    globalThis.fetch = (async () => new Response("{}", { status: 401 })) as typeof fetch;
    await expect(apiFetch("/api/auth/me")).rejects.toBeInstanceOf(UnauthorizedError);
  });

  test("non-2xx throws ApiError carrying the message", async () => {
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ error: "boom" }), { status: 400, headers: { "Content-Type": "application/json" } })) as typeof fetch;
    await expect(apiFetch("/api/settings", { method: "PUT", body: {} }))
      .rejects.toMatchObject({ status: 400, message: "boom" } satisfies Partial<ApiError>);
  });
});
