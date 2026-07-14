import { describe, expect, test } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { AuthGate } from "./AuthGate";

describe("AuthGate", () => {
  test("shows login when /api/auth/me is 401", async () => {
    server.use(
      http.get("/api/setup/status", () => HttpResponse.json({ needs_setup: false })),
      http.get("/api/auth/me", () => new HttpResponse(null, { status: 401 })),
    );
    renderWithClient(<AuthGate><div>SECRET</div></AuthGate>);
    await waitFor(() => expect(screen.getByRole("heading", { name: /sign in/i })).toBeInTheDocument());
    expect(screen.queryByText("SECRET")).not.toBeInTheDocument();
  });

  test("shows setup wizard when needs_setup", async () => {
    server.use(
      http.get("/api/setup/status", () => HttpResponse.json({ needs_setup: true })),
      http.get("/api/auth/me", () => new HttpResponse(null, { status: 401 })),
    );
    renderWithClient(<AuthGate><div>SECRET</div></AuthGate>);
    await waitFor(() => expect(screen.getByRole("heading", { name: /create admin/i })).toBeInTheDocument());
  });

  test("shows an error (not the login screen) when /setup/status fails", async () => {
    server.use(
      http.get("/api/setup/status", () => new HttpResponse(null, { status: 500 })),
      http.get("/api/auth/me", () => new HttpResponse(null, { status: 401 })),
    );
    renderWithClient(<AuthGate><div>SECRET</div></AuthGate>);
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    expect(screen.queryByRole("heading", { name: /sign in/i })).not.toBeInTheDocument();
    expect(screen.queryByText("SECRET")).not.toBeInTheDocument();
  });

  test("renders children when authenticated", async () => {
    server.use(
      http.get("/api/setup/status", () => HttpResponse.json({ needs_setup: false })),
      http.get("/api/auth/me", () => HttpResponse.json({ username: "admin" })),
    );
    renderWithClient(<AuthGate><div>SECRET</div></AuthGate>);
    await waitFor(() => expect(screen.getByText("SECRET")).toBeInTheDocument());
  });

  test("shows children right after a successful login submit, no remount needed", async () => {
    let authed = false;
    server.use(
      http.get("/api/setup/status", () => HttpResponse.json({ needs_setup: false })),
      http.get("/api/auth/me", () =>
        authed ? HttpResponse.json({ username: "admin" }) : new HttpResponse(null, { status: 401 })),
      http.post("/api/auth/login", () => { authed = true; return HttpResponse.json({ ok: true }); }),
    );
    renderWithClient(<AuthGate><div>SECRET</div></AuthGate>);
    await waitFor(() => expect(screen.getByRole("heading", { name: /sign in/i })).toBeInTheDocument());

    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/username/i), "admin");
    await user.type(screen.getByLabelText(/password/i), "admin");
    await user.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() => expect(screen.getByText("SECRET")).toBeInTheDocument());
  });
});
