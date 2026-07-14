import { expect, test } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { LogsSettings } from "./LogsSettings";

const config = { path: "/data/logs/dockbrr.log", level: "info", maxSizeMB: 50, maxBackups: 3 };
const filesFixture = [
  { name: "dockbrr.log", modified: "2026-07-12T10:00:00Z", size: 2048 },
  { name: "dockbrr-2026-07-11.log", modified: "2026-07-11T10:00:00Z", size: 1024 },
];

test("renders the config and a row per log file with a download link", async () => {
  server.use(
    http.get("/api/logs/config", () => HttpResponse.json(config)),
    http.get("/api/logs/files", () => HttpResponse.json(filesFixture)),
  );

  renderWithClient(<LogsSettings />);

  expect(await screen.findByText("dockbrr.log")).toBeInTheDocument();
  expect(screen.getByText("dockbrr-2026-07-11.log")).toBeInTheDocument();

  const link = screen.getByLabelText("Download dockbrr.log");
  expect(link).toHaveAttribute("href", "/api/logs/files/dockbrr.log/download");

  const level = screen.getByLabelText("Log level") as HTMLSelectElement;
  expect(level.value).toBe("info");
});

test("changing level PUTs log_level", async () => {
  let captured: unknown = null;
  server.use(
    http.get("/api/logs/config", () => HttpResponse.json(config)),
    http.get("/api/logs/files", () => HttpResponse.json([])),
    http.put("/api/settings", async ({ request }) => {
      captured = await request.json();
      return HttpResponse.json({ status: "saved" });
    }),
  );

  renderWithClient(<LogsSettings />);

  const level = await screen.findByLabelText("Log level");
  await userEvent.selectOptions(level, "debug");

  await waitFor(() => expect(captured).toEqual({ log_level: "debug" }));
});

test("reflects an externally updated log level (e.g. after a settings import)", async () => {
  server.use(
    http.get("/api/logs/config", () => HttpResponse.json(config)),
    http.get("/api/logs/files", () => HttpResponse.json([])),
  );

  const { client } = renderWithClient(<LogsSettings />);

  const level = (await screen.findByLabelText("Log level")) as HTMLSelectElement;
  expect(level.value).toBe("info");

  // Simulate what ApplicationSettings' import does: the server-side level
  // changed (validated + applied by the backend) and the query is invalidated.
  server.use(http.get("/api/logs/config", () => HttpResponse.json({ ...config, level: "debug" })));
  await client.invalidateQueries({ queryKey: ["logs", "config"] });

  await waitFor(() => expect(level.value).toBe("debug"));
});

test("shows empty state when no log files", async () => {
  server.use(
    http.get("/api/logs/config", () => HttpResponse.json(config)),
    http.get("/api/logs/files", () => HttpResponse.json([])),
  );

  renderWithClient(<LogsSettings />);

  expect(await screen.findByText(/no log files yet/i)).toBeInTheDocument();
});
