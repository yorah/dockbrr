import { render, screen } from "@testing-library/react";
import { RelativeTime, relative, until } from "./RelativeTime";

const NOW = new Date("2026-07-03T12:00:00Z");

test("just now for < 45s", () => {
  expect(relative("2026-07-03T11:59:30Z", NOW)).toBe("just now");
});
test("minutes ago", () => {
  expect(relative("2026-07-03T11:55:00Z", NOW)).toBe("5m ago");
});
test("hours ago", () => {
  expect(relative("2026-07-03T09:00:00Z", NOW)).toBe("3h ago");
});
test("days ago", () => {
  expect(relative("2026-06-30T12:00:00Z", NOW)).toBe("3d ago");
});
test("empty is never", () => {
  expect(relative("", NOW)).toBe("never");
});
test("invalid is never", () => {
  expect(relative("not-a-date", NOW)).toBe("never");
});
test("until: sub-minute is under a minute", () => {
  expect(until("2026-07-03T12:00:30Z", NOW)).toBe("<1m");
});
test("until: minutes", () => {
  expect(until("2026-07-03T12:04:00Z", NOW)).toBe("4m");
});
test("until: hours", () => {
  expect(until("2026-07-03T14:00:00Z", NOW)).toBe("2h");
});
test("until: past deadline is due", () => {
  expect(until("2026-07-03T11:59:00Z", NOW)).toBe("due");
});
test("until: empty or invalid is empty", () => {
  expect(until("", NOW)).toBe("");
  expect(until("not-a-date", NOW)).toBe("");
});
test("renders relative text with full iso as title", () => {
  render(<RelativeTime iso="2026-07-03T11:55:00Z" now={NOW} />);
  const el = screen.getByText("5m ago");
  expect(el).toHaveAttribute("title", "2026-07-03T11:55:00Z");
});
