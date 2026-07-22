import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { UpdatesSettings } from "@/components/settings/UpdatesSettings";
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
  // gone_grace_seconds sits on its server-side default so the "default" hint
  // has a populated case to dim.
  defaults: { poll_interval_seconds: "900", gone_grace_seconds: "86400" },
};

const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
  if (init?.method === "PUT") return new Response(null, { status: 204 });
  return new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } });
});

afterEach(() => {
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderPage() {
  vi.stubGlobal("fetch", fetchMock);
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <UpdatesSettings />
    </QueryClientProvider>,
  );
}

describe("UpdatesSettings", () => {
  it("shows a loading skeleton (not a blank page) before settings arrive", async () => {
    let resolveFetch!: (r: Response) => void;
    const pending = new Promise<Response>((r) => { resolveFetch = r; });
    vi.stubGlobal("fetch", vi.fn(() => pending));
    const client = makeQueryClient();
    render(
      <QueryClientProvider client={client}>
        <UpdatesSettings />
      </QueryClientProvider>,
    );

    expect(screen.getByRole("status", { name: /loading/i })).toBeInTheDocument();

    resolveFetch(new Response(JSON.stringify(SETTINGS), { status: 200, headers: { "content-type": "application/json" } }));
    expect(await screen.findByLabelText(/health timeout/i)).toBeInTheDocument();
  });

  it("renders only the update/apply fields", async () => {
    renderPage();
    expect(await screen.findByLabelText(/health timeout/i)).toHaveValue(60);
    expect(screen.getByLabelText(/health poll interval/i)).toHaveValue(3);
    expect(screen.getByLabelText(/write updates back to compose/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/auto-remove gone/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/gone-removal grace/i)).toHaveValue(86400);
    expect(screen.getByLabelText(/job history retention/i)).toHaveValue(30);
    // Scanning-page fields must not leak onto this page. Anchored: a bare
    // /poll interval/i also matches this page's own "Health poll interval"
    // input/label.
    expect(screen.queryByLabelText(/^poll interval/i)).not.toBeInTheDocument();
  });

  it("saves a toggled switch as a string boolean", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByLabelText(/write updates back to compose/i));
    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ write_back_compose: "true" });
    });
  });

  // Carried over from GeneralSettings.test.tsx ("job retention is editable
  // and saved on its own").
  it("job retention is editable and saved on its own", async () => {
    const user = userEvent.setup();
    renderPage();
    const retention = await screen.findByLabelText(/job history retention/i);
    expect(retention).toHaveValue(30);
    await user.clear(retention);
    await user.type(retention, "7");

    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ job_retention_days: "7" });
    });
  });

  // Carried over from GeneralSettings.test.tsx ("shows Unsaved changes and
  // enables Save only after an edit"): toggling the auto-remove switch marks
  // the form dirty, and toggling back to the persisted value clears it.
  it("shows Unsaved changes only while a field diverges from the persisted value", async () => {
    const user = userEvent.setup();
    renderPage();

    const save = await screen.findByRole("button", { name: /save/i });
    expect(save).toBeDisabled();
    expect(screen.queryByText(/unsaved changes/i)).toBeNull();

    const autoRemove = screen.getByRole("switch", { name: /auto-remove gone/i });
    await user.click(autoRemove);
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument();
    expect(save).toBeEnabled();

    await user.click(autoRemove);
    expect(screen.queryByText(/unsaved changes/i)).toBeNull();
    expect(save).toBeDisabled();
  });

  // Carried over from GeneralSettings.test.tsx ("auto-remove switch shows
  // populated defaults and marks dirty on toggle").
  it("shows the populated default on gone-removal grace and dims/undims it", async () => {
    const user = userEvent.setup();
    renderPage();

    const graceInput = (await screen.findByLabelText(/gone-removal grace/i)) as HTMLInputElement;
    expect(graceInput.value).toBe("86400");
    expect(graceInput).toHaveClass("text-muted-foreground");
    expect(screen.getByText("default")).toBeInTheDocument();

    await user.clear(graceInput);
    await user.type(graceInput, "3600");
    expect(graceInput).not.toHaveClass("text-muted-foreground");
    expect(screen.queryByText("default")).not.toBeInTheDocument();
  });

  // Carried over from GeneralSettings.test.tsx ("write-back switch toggles
  // and marks the form dirty").
  it("write-back switch toggles and marks the form dirty", async () => {
    const user = userEvent.setup();
    renderPage();
    const sw = await screen.findByRole("switch", { name: /write updates back/i });
    expect(sw).toHaveAttribute("data-state", "unchecked");
    await user.click(sw);
    expect(sw).toHaveAttribute("data-state", "checked");
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument();
  });

  it("default-auto-update switch toggles and marks the form dirty", async () => {
    const user = userEvent.setup();
    renderPage();
    const sw = await screen.findByRole("switch", { name: /auto-update newly discovered/i });
    expect(sw).toHaveAttribute("data-state", "unchecked");
    await user.click(sw);
    expect(sw).toHaveAttribute("data-state", "checked");
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument();
  });

  it("saves default_auto_update_enabled as a string boolean", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByLabelText(/auto-update newly discovered/i));
    await user.click(screen.getByRole("button", { name: /save/i }));
    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ default_auto_update_enabled: "true" });
    });
  });
});
