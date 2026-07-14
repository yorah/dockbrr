import { expect, test } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderWithClient } from "@/test/utils";
import { PasswordSettings } from "./PasswordSettings";

test("Save is disabled until new matches confirm and is long enough, then posts", async () => {
  let captured: unknown = null;
  server.use(
    http.post("/api/auth/password", async ({ request }) => {
      captured = await request.json();
      return HttpResponse.json({ ok: true });
    }),
  );

  renderWithClient(<PasswordSettings />);

  const current = screen.getByLabelText(/current password/i);
  const next = screen.getByLabelText(/^new password/i);
  const confirm = screen.getByLabelText(/confirm/i);
  const save = screen.getByRole("button", { name: /save/i });

  expect(save).toBeDisabled();

  await userEvent.type(current, "oldpass");
  await userEvent.type(next, "shortpw");
  await userEvent.type(confirm, "shortpw");
  expect(save).toBeDisabled(); // < 8 chars

  await userEvent.clear(next);
  await userEvent.clear(confirm);
  await userEvent.type(next, "newpassword");
  await userEvent.type(confirm, "mismatch1");
  expect(save).toBeDisabled(); // mismatch

  await userEvent.clear(confirm);
  await userEvent.type(confirm, "newpassword");
  expect(save).toBeEnabled();

  await userEvent.click(save);

  await waitFor(() => expect(captured).toEqual({ current: "oldpass", new: "newpassword" }));
});

test("401 (wrong current password) shows an inline error", async () => {
  server.use(
    http.post("/api/auth/password", () => HttpResponse.json({ error: "unauthorized" }, { status: 401 })),
  );

  renderWithClient(<PasswordSettings />);

  await userEvent.type(screen.getByLabelText(/current password/i), "wrongpass");
  await userEvent.type(screen.getByLabelText(/^new password/i), "newpassword");
  await userEvent.type(screen.getByLabelText(/confirm/i), "newpassword");
  await userEvent.click(screen.getByRole("button", { name: /save/i }));

  await waitFor(() =>
    expect(screen.getByRole("alert")).toHaveTextContent(/current password is incorrect/i),
  );
});

test("a non-401 failure (e.g. 500) shows a generic error, not the wrong-password message", async () => {
  server.use(
    http.post("/api/auth/password", () =>
      HttpResponse.json({ error: "internal server error" }, { status: 500 })),
  );

  renderWithClient(<PasswordSettings />);

  await userEvent.type(screen.getByLabelText(/current password/i), "oldpass1");
  await userEvent.type(screen.getByLabelText(/^new password/i), "newpassword");
  await userEvent.type(screen.getByLabelText(/confirm/i), "newpassword");
  await userEvent.click(screen.getByRole("button", { name: /save/i }));

  await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
  const alert = screen.getByRole("alert");
  expect(alert).not.toHaveTextContent(/current password is incorrect/i);
  expect(alert).toHaveTextContent(/couldn't change password/i);
});
