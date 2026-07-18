import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { CheckStatusIcon } from "./CheckStatusIcon";

describe("CheckStatusIcon", () => {
  test("local renders the local icon distinct from not_found", () => {
    render(<CheckStatusIcon status="local" />);
    expect(screen.getByLabelText("Built locally")).toBeInTheDocument();
  });

  test("not_found keeps its own label", () => {
    render(<CheckStatusIcon status="not_found" />);
    expect(screen.getByLabelText("Image not in registry")).toBeInTheDocument();
  });

  test("unknown status renders nothing", () => {
    const { container } = render(<CheckStatusIcon status="ok" />);
    expect(container).toBeEmptyDOMElement();
  });
});
