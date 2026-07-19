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

test("an unreadable file surfaces the mount hint with the file's directory and doc link", async () => {
  server.use(
    http.get("/api/projects/2/compose", () =>
      HttpResponse.json({
        files: [
          {
            path: "/home/you/myproject/compose.yml",
            content: "",
            error: "open /home/you/myproject/compose.yml: no such file or directory",
          },
        ],
      }),
    ),
  );

  renderWithClient(
    <ComposeModal projectId={2} projectName="myproject" open onOpenChange={vi.fn()} />,
  );

  expect(await screen.findByText(/mount the project directory/i)).toBeInTheDocument();
  // Copy-pastable mount derived from the failing file's directory.
  expect(screen.getByText(/-v \/home\/you\/myproject:\/home\/you\/myproject/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /path mapping guide/i })).toHaveAttribute(
    "href",
    "https://github.com/yorah/dockbrr/blob/main/docs/path-mapping.md",
  );
});

test("no mount hint when every file reads fine", async () => {
  server.use(
    http.get("/api/projects/3/compose", () =>
      HttpResponse.json({
        files: [{ path: "/srv/app/compose.yml", content: "services: {}\n" }],
      }),
    ),
  );

  renderWithClient(
    <ComposeModal projectId={3} projectName="app" open onOpenChange={vi.fn()} />,
  );

  expect(await screen.findByText("/srv/app/compose.yml")).toBeInTheDocument();
  expect(screen.queryByText(/mount the project directory/i)).not.toBeInTheDocument();
});
