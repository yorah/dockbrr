import { expect, test, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { ComposeModal } from "./ComposeModal";

test("renders each file's path label and content in a <pre>, and shows an error for a failing file", async () => {
  server.use(
    http.get("/api/projects/1/compose", () =>
      HttpResponse.json({
        files: [
          { path: "docker-compose.yml", content: "services:\n  web:\n    image: nginx\n" },
          { path: "docker-compose.override.yml", content: "", error: "permission denied" },
        ],
      }),
    ),
  );

  renderWithClient(
    <ComposeModal projectId={1} projectName="app" open onOpenChange={vi.fn()} />,
  );

  expect(await screen.findByText("docker-compose.yml")).toBeInTheDocument();
  expect(screen.getByText(/services:/)).toBeInTheDocument();
  expect(screen.getByText(/image: nginx/)).toBeInTheDocument();

  expect(screen.getByText("docker-compose.override.yml")).toBeInTheDocument();
  expect(screen.getByText("permission denied")).toBeInTheDocument();
});
