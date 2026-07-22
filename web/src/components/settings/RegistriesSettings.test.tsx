import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { makeQueryClient } from "@/api/queryClient";
import { RegistriesSettings } from "@/components/settings/RegistriesSettings";
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
  github_token_set: true,
  restart_required: [],
  defaults: {},
};

// Flipped by the "Not set" case; the API only ever exposes this boolean, never the token.
let tokenSet = true;

const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
  if (init?.method === "PUT") return new Response(null, { status: 204 });
  if (String(url).includes("/api/registries")) {
    return new Response("[]", { status: 200, headers: { "content-type": "application/json" } });
  }
  return new Response(
    // github_token is a decoy: the real API never sends a token value, only the
    // github_token_set boolean. It's injected here to prove the UI ignores it:
    // if the component ever read a token off the response, this would leak into
    // the input's value and/or the page text, and the test below would catch it.
    JSON.stringify({ ...SETTINGS, github_token_set: tokenSet, github_token: "ghp_leak" }),
    {
      status: 200,
      headers: { "content-type": "application/json" },
    },
  );
});

afterEach(() => {
  tokenSet = true;
  fetchMock.mockClear();
  vi.unstubAllGlobals();
});

function renderPage() {
  vi.stubGlobal("fetch", fetchMock);
  const client = makeQueryClient();
  return render(
    <QueryClientProvider client={client}>
      <RegistriesSettings />
    </QueryClientProvider>,
  );
}

describe("RegistriesSettings, GitHub token", () => {
  it("never prefills the token, only reports whether one is set", async () => {
    renderPage();
    const input = await screen.findByLabelText(/github token/i);
    // waitFor: the placeholder flips to "Set" only once the settings query resolves.
    await waitFor(() => expect(input).toHaveAttribute("placeholder", "Set"));
    expect(input).toHaveValue("");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAttribute("autocomplete", "off");
    // The mocked response includes a decoy token value (github_token: "ghp_leak")
    // alongside github_token_set: true. The UI must never read it: not into the
    // input's value, and not anywhere in the rendered page text.
    expect(input).toHaveValue("");
    expect(document.body.textContent).not.toMatch(/ghp_leak/);
  });

  it("shows 'Not set' when no token is stored", async () => {
    tokenSet = false;
    renderPage();
    const input = await screen.findByLabelText(/github token/i);
    // waitFor: placeholder starts neutral ("") while the settings query is in
    // flight, and only settles to "Not set" once it resolves.
    await waitFor(() => expect(input).toHaveAttribute("placeholder", "Not set"));
    expect(input).toHaveValue("");
  });

  it("sends github_token only when a value is typed, then clears the field", async () => {
    const user = userEvent.setup();
    renderPage();
    const input = await screen.findByLabelText(/github token/i);
    const saveToken = screen.getByRole("button", { name: /save token/i });

    // Untouched: nothing to send.
    expect(saveToken).toBeDisabled();

    await user.type(input, "ghp_secret");
    await user.click(saveToken);

    await waitFor(() => {
      const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PUT");
      expect(JSON.parse((put![1] as RequestInit).body as string)).toEqual({ github_token: "ghp_secret" });
    });
    await waitFor(() => expect(input).toHaveValue(""));
  });

  it("never sends an empty github_token (whitespace cannot blank the stored token)", async () => {
    const user = userEvent.setup();
    renderPage();
    const input = await screen.findByLabelText(/github token/i);
    const saveToken = screen.getByRole("button", { name: /save token/i });

    await user.type(input, "   ");
    expect(saveToken).toBeDisabled();
    // Click anyway: the button is disabled so this is a no-op, but it pins that
    // no PUT fires even on a click attempt, not just that none happened to fire.
    await user.click(saveToken);
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit)?.method === "PUT")).toBe(false);
  });

  it("keeps the registry-credentials UI intact inside its card", async () => {
    renderPage();
    expect(await screen.findByLabelText("Host")).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add registry" })).toBeInTheDocument();
  });
});
