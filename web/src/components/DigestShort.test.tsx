import { render, screen } from "@testing-library/react";
import { DigestShort } from "./DigestShort";

test("truncates to 12 hex chars, keeps full in title", () => {
  render(<DigestShort digest="sha256:0123456789abcdef0000" />);
  const el = screen.getByText("sha256:0123456789ab");
  expect(el).toHaveAttribute("title", "sha256:0123456789abcdef0000");
});
test("renders dash for empty digest", () => {
  render(<DigestShort digest="" />);
  expect(screen.getByText("-")).toBeInTheDocument();
});
