import { setupServer } from "msw/node";
import { http, HttpResponse } from "msw";

// Default handlers other tests override with server.use(...). The shell
// (sidebar + topbar) mounts projects/updates/jobs/status on every route, so
// these need a default or every app-tree test 404s on them.
export const handlers = [
  http.get("/api/setup/status", () => HttpResponse.json({ needs_setup: false })),
  http.get("/api/auth/me", () => HttpResponse.json({ username: "admin" })),
  http.get("/api/projects", () => HttpResponse.json([])),
  http.get("/api/updates", () => HttpResponse.json([])),
  http.get("/api/updates/last-applied", () => HttpResponse.json([])),
  http.get("/api/updates/self", () =>
    HttpResponse.json({
      current: "0.0.0-test",
      latest: "0.0.0-test",
      html_url: "https://github.com/yorah/dockbrr/releases/latest",
      update_available: false,
    })),
  http.get("/api/jobs", () => HttpResponse.json([])),
  http.get("/api/status", () =>
    HttpResponse.json({
      last_check_all: "",
      poll_interval_seconds: 300,
      docker_reachable: true,
      version: "0.0.0-test",
    })),
];

export const server = setupServer(...handlers);
