import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ScanProgress } from "./ScanProgress";
import { setScanRun, __resetScanRun } from "@/hooks/useScanRun";

afterEach(() => __resetScanRun());

describe("ScanProgress", () => {
  it("renders nothing when idle", () => {
    const { container } = render(<ScanProgress />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows a determinate count while running", () => {
    setScanRun({ running: true, done: 4, total: 12 });
    render(<ScanProgress />);
    expect(screen.getByText(/4\s*\/\s*12/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "4");
  });
});
