import { render, screen } from "@testing-library/react";
import { SeverityDelta, SEVERITY_COLOR } from "./SeverityDelta";

test.each([
  ["major", /text-danger/],
  ["minor", /text-warning/],
  ["patch", /text-info/],
  ["digest-only", /text-muted-foreground/],
])("colors %s", (sev, cls) => {
  render(<SeverityDelta severity={sev} />);
  const el = screen.getByText(sev);
  // toHaveClass does not accept asymmetric matchers cleanly here, so assert
  // both the stable data attribute and the color utility class directly.
  expect(el).toHaveAttribute("data-severity", sev);
  expect(el.className).toMatch(cls);
});

test("SEVERITY_COLOR has an explicit own-property entry for every known severity", () => {
  // digest-only's mapped color is the SAME value as the component's `??`
  // fallback, so without this assertion, deleting the "digest-only" entry
  // from the map would still pass every render test above. Pin the exact
  // key set so a deleted entry fails here instead.
  expect(Object.keys(SEVERITY_COLOR).sort()).toEqual(["digest-only", "major", "minor", "patch"]);
  for (const key of ["major", "minor", "patch", "digest-only"]) {
    expect(Object.prototype.hasOwnProperty.call(SEVERITY_COLOR, key)).toBe(true);
  }
});
