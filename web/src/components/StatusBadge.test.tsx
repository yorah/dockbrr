import { render, screen } from "@testing-library/react";
import { StatusBadge, computeStatus } from "./StatusBadge";
import type { Service } from "@/api/types";

const svc = (o: Partial<Service> = {}): Service => ({
  id: 1, name: "web", image_ref: "x:1", current_digest: "sha256:a",
  state: "running", pinned: false, drifted: false, healthcheck: false, auto_update_enabled: null,
  check_status: "ok", last_checked: "", ...o,
});

test("pinned wins", () => {
  expect(computeStatus(svc({ pinned: true }), { open: true })).toBe("pinned");
});
test("update available", () => {
  expect(computeStatus(svc(), { open: true })).toBe("update-available");
});
test("up to date", () => {
  expect(computeStatus(svc(), { open: false })).toBe("up-to-date");
});
test("updating wins over everything", () => {
  expect(computeStatus(svc({ pinned: true }), { open: true }, { updating: true })).toBe("updating");
});
test("error when no update and state is error", () => {
  expect(computeStatus(svc({ state: "error" }), undefined)).toBe("error");
});
test("stopped state wins over pinned and updates", () => {
  expect(computeStatus(svc({ state: "exited", pinned: true }), { open: true })).toBe("stopped");
});
test.each([
  ["exited", "stopped"],
  ["dead", "stopped"],
  ["restarting", "restarting"],
  ["gone", "gone"],
])("state %s → status %s", (state, want) => {
  expect(computeStatus(svc({ state }), undefined)).toBe(want);
});
test("pinned still wins over update, but a stopped pinned service reads stopped", () => {
  expect(computeStatus(svc({ pinned: true, state: "exited" }), { open: true })).toBe("stopped");
});
test("dismissed update yields the dismissed status", () => {
  expect(computeStatus(svc(), { open: false, dismissed: true })).toBe("dismissed");
});
test("pinned wins over a dismissed update", () => {
  expect(computeStatus(svc({ pinned: true }), { open: false, dismissed: true })).toBe("pinned");
});
test("drifted takes precedence over pinned", () => {
  expect(
    computeStatus(svc({ pinned: true, drifted: true }), undefined),
  ).toBe("drifted");
});
test("pinned when not drifted", () => {
  expect(
    computeStatus(svc({ pinned: true, drifted: false }), undefined),
  ).toBe("pinned");
});
test("a stopped state wins over a dismissed update", () => {
  expect(computeStatus(svc({ state: "exited" }), { open: false, dismissed: true })).toBe("stopped");
});
test("rolled-back update yields the rolled-back status", () => {
  expect(computeStatus(svc(), { open: false, rolledBack: true })).toBe("rolled-back");
});
test("renders a grey Rolled back label", () => {
  render(<StatusBadge status="rolled-back" />);
  expect(screen.getByText(/rolled back/i)).toBeInTheDocument();
});
test("renders a label", () => {
  render(<StatusBadge status="update-available" />);
  expect(screen.getByText(/update available/i)).toBeInTheDocument();
});
