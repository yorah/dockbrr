import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { AddProjectDialog } from "@/components/AddProjectDialog";

const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
  new Response(JSON.stringify({ id: 7, name: "media" }), { status: 200, headers: { "content-type": "application/json" } }),
);

afterEach(() => {
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderDialog(onOpenChange = vi.fn()) {
  vi.stubGlobal("fetch", fetchMock);
  const client = makeQueryClient();
  render(
    <QueryClientProvider client={client}>
      <AddProjectDialog open onOpenChange={onOpenChange} />
    </QueryClientProvider>,
  );
  return { onOpenChange };
}

describe("AddProjectDialog", () => {
  it("posts the project and closes on success", async () => {
    const user = userEvent.setup();
    const { onOpenChange } = renderDialog();

    await user.type(screen.getByLabelText(/^name$/i), "media");
    await user.type(screen.getByLabelText(/^working directory$/i), "/srv/media");
    await user.type(screen.getByLabelText(/^compose files$/i), "docker-compose.yml, override.yml");
    await user.click(screen.getByRole("button", { name: /add project/i }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "POST");
      expect(JSON.parse((post![1] as RequestInit).body as string)).toEqual({
        name: "media",
        working_dir: "/srv/media",
        config_files: ["docker-compose.yml", "override.yml"],
      });
    });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("does not submit without a name and working directory", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: /add project/i }));
    expect(fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "POST")).toBeUndefined();
  });
});
