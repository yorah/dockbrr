import { readCsrfToken } from "./csrf";
export { readCsrfToken };

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}
export class UnauthorizedError extends ApiError {
  constructor(message = "unauthorized") {
    super(401, message);
    this.name = "UnauthorizedError";
  }
}

const MUTATING = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export interface ApiOpts {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

export async function apiFetch<T = unknown>(path: string, opts: ApiOpts = {}): Promise<T> {
  const method = (opts.method ?? "GET").toUpperCase();
  const headers = new Headers();
  if (opts.body !== undefined) headers.set("Content-Type", "application/json");
  if (MUTATING.has(method)) headers.set("X-CSRF-Token", readCsrfToken());

  const res = await fetch(path, {
    method,
    credentials: "include",
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  });

  if (res.status === 401) throw new UnauthorizedError();
  if (res.status === 204) return undefined as T;
  const isJSON = res.headers.get("Content-Type")?.includes("application/json");
  const payload = isJSON ? await res.json().catch(() => null) : await res.text();
  if (!res.ok) {
    const msg = (isJSON && payload && (payload as { error?: string }).error) || res.statusText;
    throw new ApiError(res.status, String(msg));
  }
  return payload as T;
}
