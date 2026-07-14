import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { Changelog } from "./Changelog";

test("renders markdown to elements (heading + link)", () => {
  render(<Changelog markdown={"# Release 1.2\n\n[notes](https://example.com/rel)"} />);
  expect(screen.getByRole("heading", { name: /release 1\.2/i })).toBeInTheDocument();
  const link = screen.getByRole("link", { name: /notes/i });
  expect(link).toHaveAttribute("href", "https://example.com/rel");
  expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  expect(link).toHaveAttribute("target", "_blank");
});

test("strips <script> and event handlers (rehype-sanitize)", () => {
  const evil = 'Hello <script>window.__pwned=1</script> <img src=x onerror="window.__pwned=1"> world';
  const { container } = render(<Changelog markdown={evil} />);
  expect(container.querySelector("script")).toBeNull();
  expect(container.innerHTML).not.toContain("onerror");
  expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined();
});

test("drops javascript: URLs", () => {
  const { container } = render(<Changelog markdown={"[click](javascript:alert(1))"} />);
  // rehype-sanitize strips the disallowed href. Assert unconditionally that no
  // anchor survives with a javascript: scheme (an <a> with the href removed is
  // harmless; an <a> that kept it would not be).
  const anchors = Array.from(container.querySelectorAll("a"));
  for (const a of anchors) {
    expect(a.getAttribute("href") ?? "").not.toContain("javascript:");
  }
  // And nothing in the rendered HTML carries the scheme either.
  expect(container.innerHTML).not.toContain("javascript:");
});

test("shows a fallback message when no markdown is available", () => {
  render(<Changelog markdown="" />);
  expect(screen.getByText(/no changelog available/i)).toBeInTheDocument();
});
