import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { Filters } from "./Filters";
import type { FilterState } from "@/hooks/useDashboardRows";

const baseValue: FilterState = {
  onlyUpdates: false,
  project: "",
  status: "",
  search: "",
  showRemoved: false,
};

test("renders the Show removed switch unchecked by default", () => {
  const onChange = vi.fn();
  render(<Filters value={baseValue} onChange={onChange} />);
  const sw = screen.getByRole("switch", { name: /show removed/i });
  expect(sw).toHaveAttribute("data-state", "unchecked");
});

test("toggling Show removed calls onChange with showRemoved flipped", async () => {
  const onChange = vi.fn();
  render(<Filters value={baseValue} onChange={onChange} />);
  const sw = screen.getByRole("switch", { name: /show removed/i });
  await userEvent.click(sw);
  expect(onChange).toHaveBeenCalledWith({ ...baseValue, showRemoved: true });
});

test("offers no project filter, since projects are picked from the sidebar instead", () => {
  render(<Filters value={baseValue} onChange={vi.fn()} />);
  expect(screen.queryByLabelText(/filter by project/i)).toBeNull();
  expect(screen.getByLabelText(/filter by status/i)).toBeInTheDocument();
});

test("toggling an already-checked Show removed flips it back off", async () => {
  const onChange = vi.fn();
  render(
    <Filters value={{ ...baseValue, showRemoved: true }} onChange={onChange} />,
  );
  const sw = screen.getByRole("switch", { name: /show removed/i });
  expect(sw).toHaveAttribute("data-state", "checked");
  await userEvent.click(sw);
  expect(onChange).toHaveBeenCalledWith({ ...baseValue, showRemoved: false });
});
